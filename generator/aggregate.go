package generator

import (
	"fmt"
	"strings"

	"github.com/ariefsam/esb/injector"
	"github.com/ariefsam/esb/naming"
)

// AddAggregate generates domain/service/projection files for a new aggregate
// and updates projection/db.go, wire/wire.go, and main.go. All file writes
// are staged in a single transaction and committed together, so a missing
// marker or bad render leaves the project untouched rather than half-wired.
func AddAggregate(aggregateName string) error {
	if err := validateSnakeName("aggregate name", aggregateName); err != nil {
		return err
	}
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

	tx := injector.NewTx()
	var actions []string

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
		content, err := renderTemplate(f.tmpl, data)
		if err != nil {
			return fmt.Errorf("generate %s: %w", f.dest, err)
		}
		tx.Create(f.dest, content)
		actions = append(actions, "  create  "+f.dest)
	}

	// Inject &<Name>Row{} into projection/db.go AutoMigrate.
	rowEntry := "\t\t&" + data.AggregateNamePascal + "Row{},"
	if ok, err := tx.Contains("projection/db.go", data.AggregateNamePascal+"Row{}"); err != nil {
		return err
	} else if !ok {
		if err := tx.InjectAfterMarker("projection/db.go", "// esb:inject:automigrate-models", rowEntry); err != nil {
			return err
		}
		actions = append(actions, "  update  projection/db.go")
	}

	// Inject App field + worker construction + return field into wire/wire.go.
	workerType := data.AggregateNamePascal + "ProjectionWorker"
	if ok, err := tx.Contains("wire/wire.go", workerType); err != nil {
		return err
	} else if !ok {
		workerVar := lcFirst(data.AggregateNamePascal) + "Worker"
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
	return nil
}
