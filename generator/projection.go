package generator

import (
	"fmt"
	"strings"

	"github.com/ariefsam/esb/injector"
	"github.com/ariefsam/esb/naming"
)

// AddProjection generates a multi-aggregate projection worker. All writes are
// staged in one transaction so a failure leaves the project untouched.
func AddProjection(projectionName string, aggregateNames []string) error {
	if err := validateSnakeName("projection name", projectionName); err != nil {
		return err
	}
	if len(aggregateNames) == 0 {
		return fmt.Errorf("projection %q needs at least one aggregate", projectionName)
	}
	for _, a := range aggregateNames {
		if err := validateSnakeName("aggregate name", a); err != nil {
			return err
		}
	}
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

	tx := injector.NewTx()
	var actions []string

	files := []struct {
		tmpl string
		dest string
	}{
		{"projection_multi_row.go.tmpl", "projection/" + projectionName + "_row.go"},
		{"projection_multi_worker.go.tmpl", "projection/" + projectionName + "_worker.go"},
	}
	for _, f := range files {
		content, err := renderTemplate(f.tmpl, data)
		if err != nil {
			return fmt.Errorf("generate %s: %w", f.dest, err)
		}
		tx.Create(f.dest, content)
		actions = append(actions, "  create  "+f.dest)
	}

	// Inject row into projection/db.go AutoMigrate.
	rowEntry := "\t\t&" + data.ProjectionNamePascal + "Row{},"
	if ok, err := tx.Contains("projection/db.go", data.ProjectionNamePascal+"Row{}"); err != nil {
		return err
	} else if !ok {
		if err := tx.InjectAfterMarker("projection/db.go", "// esb:inject:automigrate-models", rowEntry); err != nil {
			return err
		}
		actions = append(actions, "  update  projection/db.go")
	}

	// Inject App field + worker construction + return field into wire/wire.go.
	workerType := data.ProjectionNamePascal + "ProjectionWorker"
	if ok, err := tx.Contains("wire/wire.go", workerType); err != nil {
		return err
	} else if !ok {
		workerVar := lcFirst(data.ProjectionNamePascal) + "Worker"
		if err := tx.InjectAfterMarker("wire/wire.go", "// esb:inject:app-fields", "\t"+workerType+" *projection."+workerType); err != nil {
			return err
		}
		if err := tx.InjectAfterMarker("wire/wire.go", "// esb:inject:app-init", "\t"+workerVar+" := projection.New"+workerType+"(eventRepo, db)"); err != nil {
			return err
		}
		if err := tx.InjectAfterMarker("wire/wire.go", "// esb:inject:app-return-fields", "\t\t"+workerType+": "+workerVar+","); err != nil {
			return err
		}
		actions = append(actions, "  update  wire/wire.go")
	}

	// Inject worker into main.go workers slice.
	if ok, err := tx.Contains("main.go", workerType); err != nil {
		return err
	} else if !ok {
		if err := tx.InjectAfterMarker("main.go", "// esb:inject:projection-workers", "\t\tapp."+workerType+","); err != nil {
			return err
		}
		actions = append(actions, "  update  main.go")
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	for _, a := range actions {
		fmt.Println(a)
	}
	fmt.Printf("\nAggregate names listened: %s\n", strings.Join(aggregateNames, ", "))
	return nil
}
