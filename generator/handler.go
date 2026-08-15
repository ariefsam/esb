package generator

import (
	"fmt"

	"github.com/ariefsam/esb/injector"
	"github.com/ariefsam/esb/naming"
)

// AddHandler generates an HTTP handler skeleton and updates routes + wire.
// Every file write is staged in one transaction so a failure anywhere leaves
// the project untouched instead of partially wired.
func AddHandler(handlerName, aggregateName string) error {
	if err := validateSnakeName("handler name", handlerName); err != nil {
		return err
	}
	if err := validateSnakeName("aggregate name", aggregateName); err != nil {
		return err
	}
	moduleName, err := ReadModuleName()
	if err != nil {
		return err
	}

	data := HandlerData{
		ModuleName:          moduleName,
		PackageName:         naming.PackageName(moduleName),
		HandlerName:         handlerName,
		HandlerNamePascal:   naming.ToPascalCase(handlerName),
		AggregateName:       aggregateName,
		AggregateNamePascal: naming.ToPascalCase(aggregateName),
	}

	tx := injector.NewTx()
	var actions []string

	dest := "server/handler/" + handlerName + ".go"
	content, err := renderTemplate("handler.go.tmpl", data)
	if err != nil {
		return fmt.Errorf("generate %s: %w", dest, err)
	}
	tx.Create(dest, content)
	actions = append(actions, "  create  "+dest)

	// Route into server/routes.go.
	routeEntry := "\t// TODO: router.HandleFunc(\"/" + naming.ToKebabCase(handlerName) + "\", app." + data.HandlerNamePascal + "Handler.Handle).Methods(http.MethodPost)"
	if ok, err := tx.Contains("server/routes.go", data.HandlerNamePascal+"Handler"); err != nil {
		return err
	} else if !ok {
		if err := tx.InjectAfterMarker("server/routes.go", "// esb:inject:routes", routeEntry); err != nil {
			return err
		}
		actions = append(actions, "  update  server/routes.go")
	}

	// wire/wire.go references both the handler package and the aggregate's
	// service, neither of which the base template imports — add them now.
	if err := tx.EnsureImport("wire/wire.go", moduleName+"/server/handler"); err != nil {
		return err
	}
	if err := tx.EnsureImport("wire/wire.go", moduleName+"/service"); err != nil {
		return err
	}

	svcVar := lcFirst(data.AggregateNamePascal) + "Svc"
	handlerField := data.HandlerNamePascal + "Handler"
	if ok, err := tx.Contains("wire/wire.go", handlerField); err != nil {
		return err
	} else if !ok {
		varName := lcFirst(data.HandlerNamePascal) + "Handler"
		if err := tx.InjectAfterMarker("wire/wire.go", "// esb:inject:app-fields", "\t"+handlerField+" *handler."+handlerField); err != nil {
			return err
		}
		if err := tx.InjectAfterMarker("wire/wire.go", "// esb:inject:app-init", "\t"+varName+" := handler.New"+data.HandlerNamePascal+"Handler("+svcVar+")"); err != nil {
			return err
		}
		if err := tx.InjectAfterMarker("wire/wire.go", "// esb:inject:app-return-fields", "\t\t"+handlerField+": "+varName+","); err != nil {
			return err
		}
		actions = append(actions, "  update  wire/wire.go")
	}

	// Ensure the aggregate's service is constructed in NewApp so the handler
	// has a dependency to receive. Guarded independently of the handler so
	// multiple handlers on the same aggregate share a single service. Injected
	// at the dedicated app-services marker, which sits physically above
	// app-init in the template, so the service is always declared before any
	// handler that references it (Go requires declare-before-use).
	if ok, err := tx.Contains("wire/wire.go", svcVar+" :="); err != nil {
		return err
	} else if !ok {
		svcInit := "\t" + svcVar + " := service.New" + data.AggregateNamePascal + "Service(eventRepo)"
		if err := tx.InjectAfterMarker("wire/wire.go", "// esb:inject:app-services", svcInit); err != nil {
			return err
		}
		actions = append(actions, "  update  wire/wire.go (service)")
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	for _, a := range actions {
		fmt.Println(a)
	}
	return nil
}
