package generator

import (
	"fmt"
	"strings"

	"github.com/ariefsam/esb/injector"
	"github.com/ariefsam/esb/naming"
)

// AddAggregate generates domain/service/projection files for a new aggregate
// and updates projection/db.go, wire/wire.go, and main.go.
func AddAggregate(aggregateName string) error {
	moduleName, err := ReadModuleName()
	if err != nil {
		return err
	}

	pascal := naming.ToPascalCase(aggregateName)
	data := AggregateData{
		ModuleName:          moduleName,
		PackageName:         naming.PackageName(moduleName),
		AggregateName:       aggregateName,
		AggregateNamePascal: pascal,
		AggregateNameKebab:  naming.ToKebabCase(aggregateName),
		ReceiverName:        strings.ToLower(pascal[:1]),
		TableName:           naming.ToPlural(aggregateName),
	}

	files := []struct {
		tmpl string
		dest string
	}{
		{"domain_aggregate.go.tmpl", "domain/" + aggregateName + ".go"},
		{"service.go.tmpl", "service/" + aggregateName + ".go"},
		{"service_scenario_test.go.tmpl", "service/" + aggregateName + "_scenario_test.go"},
		{"projection_row.go.tmpl", "projection/" + aggregateName + "_row.go"},
		{"projection_worker.go.tmpl", "projection/" + aggregateName + "_worker.go"},
	}

	for _, f := range files {
		if err := renderFile(f.tmpl, f.dest, data); err != nil {
			return fmt.Errorf("generate %s: %w", f.dest, err)
		}
		fmt.Printf("  create  %s\n", f.dest)
	}

	// Inject &<Name>Row{} into projection/db.go AutoMigrate
	rowEntry := "\t\t&" + data.AggregateNamePascal + "Row{},"
	if ok, _ := injector.AlreadyContains("projection/db.go", data.AggregateNamePascal+"Row{}"); !ok {
		if err := injector.InjectAfterMarker("projection/db.go", "// esb:inject:automigrate-models", rowEntry); err != nil {
			fmt.Printf("  warn    %v\n", err)
		} else {
			fmt.Println("  update  projection/db.go")
		}
	}

	// Inject App field into wire/wire.go
	appField := "\t" + data.AggregateNamePascal + "ProjectionWorker *projection." + data.AggregateNamePascal + "ProjectionWorker"
	if ok, _ := injector.AlreadyContains("wire/wire.go", data.AggregateNamePascal+"ProjectionWorker"); !ok {
		if err := injector.InjectAfterMarker("wire/wire.go", "// esb:inject:app-fields", appField); err != nil {
			fmt.Printf("  warn    %v\n", err)
		}

		// Inject construction into NewApp
		initCode := "\t" + strings.ToLower(data.AggregateNamePascal[:1]) + data.AggregateNamePascal[1:] + "Worker := projection.New" + data.AggregateNamePascal + "ProjectionWorker(esClient, db)"
		if err := injector.InjectAfterMarker("wire/wire.go", "// esb:inject:app-init", initCode); err != nil {
			fmt.Printf("  warn    %v\n", err)
		}

		// Inject return field
		returnField := "\t\t" + data.AggregateNamePascal + "ProjectionWorker: " + strings.ToLower(data.AggregateNamePascal[:1]) + data.AggregateNamePascal[1:] + "Worker,"
		if err := injector.InjectAfterMarker("wire/wire.go", "// esb:inject:app-return-fields", returnField); err != nil {
			fmt.Printf("  warn    %v\n", err)
		}

		fmt.Println("  update  wire/wire.go")
	}

	// Inject worker into main.go workers slice
	workerEntry := "\t\tapp." + data.AggregateNamePascal + "ProjectionWorker,"
	if ok, _ := injector.AlreadyContains("main.go", data.AggregateNamePascal+"ProjectionWorker"); !ok {
		if err := injector.InjectAfterMarker("main.go", "// esb:inject:projection-workers", workerEntry); err != nil {
			fmt.Printf("  warn    %v\n", err)
		} else {
			fmt.Println("  update  main.go")
		}
	}

	return nil
}
