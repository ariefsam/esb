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
	routeEntry := "\t// TODO: router.HandleFunc(\"/" + naming.ToKebabCase(handlerName) + "\", app." + data.HandlerNamePascal + "Handler.Handle).Methods(http.MethodPost)"
	if ok, _ := injector.AlreadyContains("server/routes.go", data.HandlerNamePascal+"Handler"); !ok {
		if err := injector.InjectAfterMarker("server/routes.go", "// esb:inject:routes", routeEntry); err != nil {
			fmt.Printf("  warn    %v\n", err)
		} else {
			fmt.Println("  update  server/routes.go")
		}
	}

	// Inject App field into wire/wire.go
	handlerField := data.HandlerNamePascal + "Handler"
	if ok, _ := injector.AlreadyContains("wire/wire.go", handlerField); !ok {
		appField := "\t" + handlerField + " *handler." + handlerField
		injector.InjectAfterMarker("wire/wire.go", "// esb:inject:app-fields", appField)

		varName := lcFirst(data.HandlerNamePascal) + "Handler"
		initCode := "\t" + varName + " := handler.New" + data.HandlerNamePascal + "Handler(" + lcFirst(data.AggregateNamePascal) + "Svc)"
		injector.InjectAfterMarker("wire/wire.go", "// esb:inject:app-init", initCode)

		returnField := "\t\t" + handlerField + ": " + varName + ","
		injector.InjectAfterMarker("wire/wire.go", "// esb:inject:app-return-fields", returnField)

		fmt.Println("  update  wire/wire.go")
	}

	return nil
}
