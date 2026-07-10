package generator

import (
	"fmt"
	"strings"

	"github.com/ariefsam/esb/injector"
	"github.com/ariefsam/esb/naming"
)

// AddProjection generates a multi-aggregate projection worker.
func AddProjection(projectionName string, aggregateNames []string) error {
	moduleName, err := ReadModuleName()
	if err != nil {
		return err
	}

	data := ProjectionData{
		ModuleName:           moduleName,
		PackageName:          naming.PackageName(moduleName),
		ProjectionName:       projectionName,
		ProjectionNamePascal: naming.ToPascalCase(projectionName),
		AggregateNames:       aggregateNames,
		TableName:            naming.ToPlural(projectionName),
	}

	files := []struct {
		tmpl string
		dest string
	}{
		{"projection_multi_row.go.tmpl", "projection/" + projectionName + "_row.go"},
		{"projection_multi_worker.go.tmpl", "projection/" + projectionName + "_worker.go"},
	}

	for _, f := range files {
		if err := renderFile(f.tmpl, f.dest, data); err != nil {
			return fmt.Errorf("generate %s: %w", f.dest, err)
		}
		fmt.Printf("  create  %s\n", f.dest)
	}

	// Inject row into projection/db.go AutoMigrate
	rowEntry := "\t\t&" + data.ProjectionNamePascal + "Row{},"
	if ok, _ := injector.AlreadyContains("projection/db.go", data.ProjectionNamePascal+"Row{}"); !ok {
		if err := injector.InjectAfterMarker("projection/db.go", "// esb:inject:automigrate-models", rowEntry); err != nil {
			fmt.Printf("  warn    %v\n", err)
		} else {
			fmt.Println("  update  projection/db.go")
		}
	}

	// Inject App field + worker construction + return field into wire/wire.go
	workerField := data.ProjectionNamePascal + "ProjectionWorker"
	if ok, _ := injector.AlreadyContains("wire/wire.go", workerField); !ok {
		appField := "\t" + workerField + " *projection." + workerField
		injector.InjectAfterMarker("wire/wire.go", "// esb:inject:app-fields", appField)

		varName := lcFirst(data.ProjectionNamePascal) + "Worker"
		initCode := "\t" + varName + " := projection.New" + data.ProjectionNamePascal + "ProjectionWorker(esClient, db)"
		injector.InjectAfterMarker("wire/wire.go", "// esb:inject:app-init", initCode)

		returnField := "\t\t" + workerField + ": " + varName + ","
		injector.InjectAfterMarker("wire/wire.go", "// esb:inject:app-return-fields", returnField)

		fmt.Println("  update  wire/wire.go")
	}

	// Inject worker into main.go workers slice
	workerEntry := "\t\tapp." + workerField + ","
	if ok, _ := injector.AlreadyContains("main.go", workerField); !ok {
		if err := injector.InjectAfterMarker("main.go", "// esb:inject:projection-workers", workerEntry); err != nil {
			fmt.Printf("  warn    %v\n", err)
		} else {
			fmt.Println("  update  main.go")
		}
	}

	fmt.Printf("\nAggregate names listened: %s\n", strings.Join(aggregateNames, ", "))
	return nil
}
