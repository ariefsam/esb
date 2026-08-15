package generator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ariefsam/esb/injector"
	"github.com/ariefsam/esb/naming"
)

// AddLedger scaffolds a double-entry-style ledger account aggregate: an
// append-only balance with Open/Deposit/Withdraw/Freeze/Close commands, a
// non-negative-balance invariant, a balance read model + statement journal,
// balance/statement queries, write-side HTTP handlers, and Given-When-Then
// scenario tests (including a concurrent no-double-spend test). Everything is
// staged in one injector.Tx and committed together.
func AddLedger(name string) error {
	if err := validateSnakeName("ledger name", name); err != nil {
		return err
	}
	moduleName, err := ReadModuleName()
	if err != nil {
		return err
	}

	pascal := naming.ToPascalCase(name)
	data := LedgerData{
		ModuleName:     moduleName,
		PackageName:    naming.PackageName(moduleName),
		Name:           name,
		NamePascal:     pascal,
		NameKebab:      naming.ToKebabCase(name),
		Receiver:       strings.ToLower(pascal[:1]),
		TableName:      naming.ToPlural(name),
		EntryTableName: name + "_entries",
	}

	tx := injector.NewTx()
	var actions []string

	files := []struct {
		tmpl string
		dest string
	}{
		{"recipe/ledger_domain.go.tmpl", "domain/" + name + ".go"},
		{"recipe/ledger_service.go.tmpl", "service/" + name + ".go"},
		{"recipe/ledger_scenario_test.go.tmpl", "service/" + name + "_scenario_test.go"},
		{"recipe/ledger_row.go.tmpl", "projection/" + name + "_row.go"},
		{"recipe/ledger_worker.go.tmpl", "projection/" + name + "_worker.go"},
		{"recipe/ledger_query.go.tmpl", "projection/" + name + "_query.go"},
		{"recipe/ledger_handler.go.tmpl", "server/handler/" + name + ".go"},
	}
	for _, f := range files {
		content, err := renderTemplate(f.tmpl, data)
		if err != nil {
			return fmt.Errorf("generate %s: %w", f.dest, err)
		}
		tx.Create(f.dest, content)
		actions = append(actions, "  create  "+f.dest)
	}

	if _, statErr := os.Stat(filepath.FromSlash("server/handler/response.go")); os.IsNotExist(statErr) {
		content, err := renderTemplate("recipe/crud_response.go.tmpl", data)
		if err != nil {
			return fmt.Errorf("generate server/handler/response.go: %w", err)
		}
		tx.Create("server/handler/response.go", content)
		actions = append(actions, "  create  server/handler/response.go")
	}

	// AutoMigrate both read-model tables (balance + statement).
	for _, row := range []string{pascal + "BalanceRow", pascal + "EntryRow"} {
		if ok, err := tx.Contains("projection/db.go", row+"{}"); err != nil {
			return err
		} else if !ok {
			if err := tx.InjectAfterMarker("projection/db.go", "// esb:inject:automigrate-models", "\t\t&"+row+"{},"); err != nil {
				return err
			}
			actions = append(actions, "  update  projection/db.go")
		}
	}

	if err := wireAggregateSlice(tx, moduleName, pascal, &actions); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	for _, a := range actions {
		fmt.Println(a)
	}
	fmt.Printf("\nLedger %q ready: Open/Deposit/Withdraw/Freeze/Close, balance + statement queries, and scenario tests (incl. concurrency).\n", name)
	return nil
}

// wireAggregateSlice injects the projection worker + write-side handler +
// service instance for a recipe aggregate into wire/main, constructing the
// service with the default `service.New<Pascal>Service(eventRepo)`.
func wireAggregateSlice(tx *injector.Tx, moduleName, pascal string, actions *[]string) error {
	return wireAggregateSliceWithService(tx, moduleName, pascal, "service.New"+pascal+"Service(eventRepo)", actions)
}

// wireAggregateSliceWithService is wireAggregateSlice with an explicit service
// constructor RHS — used by recipes whose service takes extra dependencies
// (e.g. the saga service, which needs a port).
func wireAggregateSliceWithService(tx *injector.Tx, moduleName, pascal, serviceCtor string, actions *[]string) error {
	lower := lcFirst(pascal)
	workerType := pascal + "ProjectionWorker"
	handlerType := pascal + "Handler"
	svcVar := lower + "Svc"

	if err := tx.EnsureImport("wire/wire.go", moduleName+"/server/handler"); err != nil {
		return err
	}
	if err := tx.EnsureImport("wire/wire.go", moduleName+"/service"); err != nil {
		return err
	}

	if ok, err := tx.Contains("wire/wire.go", svcVar+" :="); err != nil {
		return err
	} else if !ok {
		if err := tx.InjectAfterMarker("wire/wire.go", "// esb:inject:app-services", "\t"+svcVar+" := "+serviceCtor); err != nil {
			return err
		}
	}

	if ok, err := tx.Contains("wire/wire.go", workerType); err != nil {
		return err
	} else if !ok {
		workerVar := lower + "Worker"
		if err := tx.InjectAfterMarker("wire/wire.go", "// esb:inject:app-fields", "\t"+workerType+" *projection."+workerType); err != nil {
			return err
		}
		if err := tx.InjectAfterMarker("wire/wire.go", "// esb:inject:app-init", "\t"+workerVar+" := projection.New"+workerType+"(esClient, db)"); err != nil {
			return err
		}
		if err := tx.InjectAfterMarker("wire/wire.go", "// esb:inject:app-return-fields", "\t\t"+workerType+": "+workerVar+","); err != nil {
			return err
		}
	}

	if ok, err := tx.Contains("wire/wire.go", handlerType); err != nil {
		return err
	} else if !ok {
		handlerVar := lower + "Handler"
		if err := tx.InjectAfterMarker("wire/wire.go", "// esb:inject:app-fields", "\t"+handlerType+" *handler."+handlerType); err != nil {
			return err
		}
		if err := tx.InjectAfterMarker("wire/wire.go", "// esb:inject:app-init", "\t"+handlerVar+" := handler.New"+handlerType+"("+svcVar+")"); err != nil {
			return err
		}
		if err := tx.InjectAfterMarker("wire/wire.go", "// esb:inject:app-return-fields", "\t\t"+handlerType+": "+handlerVar+","); err != nil {
			return err
		}
	}
	*actions = append(*actions, "  update  wire/wire.go")

	if ok, err := tx.Contains("main.go", workerType); err != nil {
		return err
	} else if !ok {
		if err := tx.InjectAfterMarker("main.go", "// esb:inject:projection-workers", "\t\tapp."+workerType+","); err != nil {
			return err
		}
		*actions = append(*actions, "  update  main.go")
	}

	return nil
}
