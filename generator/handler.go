package generator

import (
	"fmt"

	"github.com/ariefsam/esb/injector"
	"github.com/ariefsam/esb/naming"
)

// AddHandler generates an HTTP handler skeleton and updates routes + wire.
func AddHandler(handlerName, aggregateName string) error {
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

	dest := "server/handler/" + handlerName + ".go"
	if err := renderFile("handler.go.tmpl", dest, data); err != nil {
		return fmt.Errorf("generate %s: %w", dest, err)
	}
	fmt.Printf("  create  %s\n", dest)

	// Inject route into server/routes.go
	routeEntry := "\t// TODO: router.HandleFunc(\"/"+naming.ToKebabCase(handlerName)+"\", app."+data.HandlerNamePascal+"Handler.Handle).Methods(http.MethodPost)"
	if ok, _ := injector.AlreadyContains("server/routes.go", data.HandlerNamePascal+"Handler"); !ok {
		if err := injector.InjectAfterMarker("server/routes.go", "// esb:inject:routes", routeEntry); err != nil {
			fmt.Printf("  warn    %v\n", err)
		} else {
			fmt.Println("  update  server/routes.go")
		}
	}

	// wire/wire.go references both the handler package and the aggregate's
	// service, neither of which the base template imports — add them now.
	if err := injector.EnsureImport("wire/wire.go", moduleName+"/server/handler"); err != nil {
		fmt.Printf("  warn    %v\n", err)
	}
	if err := injector.EnsureImport("wire/wire.go", moduleName+"/service"); err != nil {
		fmt.Printf("  warn    %v\n", err)
	}

	// Inject the handler's App field, construction, and return wiring.
	svcVar := lcFirst(data.AggregateNamePascal) + "Svc"
	handlerField := data.HandlerNamePascal + "Handler"
	if ok, _ := injector.AlreadyContains("wire/wire.go", handlerField); !ok {
		appField := "\t" + handlerField + " *handler." + handlerField
		if err := injector.InjectAfterMarker("wire/wire.go", "// esb:inject:app-fields", appField); err != nil {
			fmt.Printf("  warn    %v\n", err)
		}

		varName := lcFirst(data.HandlerNamePascal) + "Handler"
		initCode := "\t" + varName + " := handler.New" + data.HandlerNamePascal + "Handler(" + svcVar + ")"
		if err := injector.InjectAfterMarker("wire/wire.go", "// esb:inject:app-init", initCode); err != nil {
			fmt.Printf("  warn    %v\n", err)
		}

		returnField := "\t\t" + handlerField + ": " + varName + ","
		if err := injector.InjectAfterMarker("wire/wire.go", "// esb:inject:app-return-fields", returnField); err != nil {
			fmt.Printf("  warn    %v\n", err)
		}

		fmt.Println("  update  wire/wire.go")
	}

	// Ensure the aggregate's service is constructed in NewApp so the handler
	// has a dependency to receive. Guarded independently of the handler so
	// multiple handlers on the same aggregate share a single service. Injected
	// at the dedicated app-services marker, which sits physically above
	// app-init in the template, so the service is always declared before any
	// handler that references it (Go requires declare-before-use).
	if ok, _ := injector.AlreadyContains("wire/wire.go", svcVar+" :="); !ok {
		svcInit := "\t" + svcVar + " := service.New" + data.AggregateNamePascal + "Service(eventRepo)"
		if err := injector.InjectAfterMarker("wire/wire.go", "// esb:inject:app-services", svcInit); err != nil {
			fmt.Printf("  warn    %v\n", err)
		} else {
			fmt.Println("  update  wire/wire.go (service)")
		}
	}

	return nil
}
