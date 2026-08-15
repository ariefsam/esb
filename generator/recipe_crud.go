package generator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ariefsam/esb/injector"
	"github.com/ariefsam/esb/naming"
)

// AddCRUD scaffolds a complete CRUD vertical slice for an entity aggregate:
// domain aggregate + Created/Updated/Archived events, a service with
// Create/Update/Archive commands (invariants enforced on the aggregate), a
// read-model row + projection worker, List/Get queries, write-side HTTP
// handlers, and Given-When-Then scenario tests. Everything is staged in one
// injector.Tx and committed together, so a failure leaves the project
// untouched.
func AddCRUD(name string, fields []FieldDef) error {
	if err := validateSnakeName("crud name", name); err != nil {
		return err
	}
	moduleName, err := ReadModuleName()
	if err != nil {
		return err
	}

	pascal := naming.ToPascalCase(name)
	data := CRUDData{
		ModuleName:  moduleName,
		PackageName: naming.PackageName(moduleName),
		Name:        name,
		NamePascal:  pascal,
		NameKebab:   naming.ToKebabCase(name),
		Receiver:    strings.ToLower(pascal[:1]),
		TableName:   naming.ToPlural(name),
		Fields:      toCRUDFields(fields),
	}

	tx := injector.NewTx()
	var actions []string

	files := []struct {
		tmpl string
		dest string
	}{
		{"recipe/crud_domain.go.tmpl", "domain/" + name + ".go"},
		{"recipe/crud_service.go.tmpl", "service/" + name + ".go"},
		{"recipe/crud_scenario_test.go.tmpl", "service/" + name + "_scenario_test.go"},
		{"recipe/crud_row.go.tmpl", "projection/" + name + "_row.go"},
		{"recipe/crud_worker.go.tmpl", "projection/" + name + "_worker.go"},
		{"recipe/crud_query.go.tmpl", "projection/" + name + "_query.go"},
		{"recipe/crud_handler.go.tmpl", "server/handler/" + name + ".go"},
	}
	for _, f := range files {
		content, err := renderTemplate(f.tmpl, data)
		if err != nil {
			return fmt.Errorf("generate %s: %w", f.dest, err)
		}
		tx.Create(f.dest, content)
		actions = append(actions, "  create  "+f.dest)
	}

	// Shared HTTP response helpers — generate once so multiple CRUD recipes
	// don't redefine writeJSON/writeError.
	if _, statErr := os.Stat(filepath.FromSlash("server/handler/response.go")); os.IsNotExist(statErr) {
		content, err := renderTemplate("recipe/crud_response.go.tmpl", data)
		if err != nil {
			return fmt.Errorf("generate server/handler/response.go: %w", err)
		}
		tx.Create("server/handler/response.go", content)
		actions = append(actions, "  create  server/handler/response.go")
	}

	if err := wireCRUD(tx, moduleName, data, &actions); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	for _, a := range actions {
		fmt.Println(a)
	}
	fmt.Printf("\nCRUD %q ready: Create/Update/Archive commands, List%ss/Get%s queries, and scenario tests.\n",
		name, data.NamePascal, data.NamePascal)
	return nil
}

// wireCRUD injects the projection worker, service, and handler into the
// existing wire/db/main files — the union of what `add aggregate` and
// `add handler` wire, staged into the recipe's transaction.
func wireCRUD(tx *injector.Tx, moduleName string, data CRUDData, actions *[]string) error {
	pascal := data.NamePascal
	lower := lcFirst(pascal)
	workerType := pascal + "ProjectionWorker"
	handlerType := pascal + "Handler"
	svcVar := lower + "Svc"

	// projection/db.go — AutoMigrate the read-model row.
	if ok, err := tx.Contains("projection/db.go", pascal+"Row{}"); err != nil {
		return err
	} else if !ok {
		if err := tx.InjectAfterMarker("projection/db.go", "// esb:inject:automigrate-models", "\t\t&"+pascal+"Row{},"); err != nil {
			return err
		}
		*actions = append(*actions, "  update  projection/db.go")
	}

	// wire/wire.go — imports for the handler and service packages.
	if err := tx.EnsureImport("wire/wire.go", moduleName+"/server/handler"); err != nil {
		return err
	}
	if err := tx.EnsureImport("wire/wire.go", moduleName+"/service"); err != nil {
		return err
	}

	// Service instance (at the app-services marker, above app-init).
	if ok, err := tx.Contains("wire/wire.go", svcVar+" :="); err != nil {
		return err
	} else if !ok {
		if err := tx.InjectAfterMarker("wire/wire.go", "// esb:inject:app-services", "\t"+svcVar+" := service.New"+pascal+"Service(eventRepo)"); err != nil {
			return err
		}
	}

	// Projection worker (field + init + return).
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

	// Write-side handler (field + init + return).
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

	// main.go — start the projection worker.
	if ok, err := tx.Contains("main.go", workerType); err != nil {
		return err
	} else if !ok {
		if err := tx.InjectAfterMarker("main.go", "// esb:inject:projection-workers", "\t\tapp."+workerType+","); err != nil {
			return err
		}
		*actions = append(*actions, "  update  main.go")
	}

	// server/routes.go — leave route hints for the write commands.
	kebab := data.NameKebab
	for _, cmd := range []string{"create", "update", "archive"} {
		method := naming.ToPascalCase(cmd)
		route := "\t// TODO: router.HandleFunc(\"/" + kebab + "/" + cmd + "\", app." + handlerType + "." + method + ").Methods(http.MethodPost)"
		if ok, err := tx.Contains("server/routes.go", handlerType+"."+method); err != nil {
			return err
		} else if !ok {
			if err := tx.InjectAfterMarker("server/routes.go", "// esb:inject:routes", route); err != nil {
				return err
			}
		}
	}

	return nil
}

// toCRUDFields converts parsed fields into template fields with sample literals
// for the generated scenario tests.
func toCRUDFields(fields []FieldDef) []CRUDField {
	out := make([]CRUDField, 0, len(fields))
	for _, f := range fields {
		out = append(out, CRUDField{
			NamePascal: f.NamePascal,
			JSONTag:    f.JSONTag,
			Type:       f.Type,
			Sample:     sampleLiteral(f.Type),
		})
	}
	return out
}

// sampleLiteral returns a Go literal usable as a sample value for typ.
func sampleLiteral(typ string) string {
	switch typ {
	case "string":
		return `"sample"`
	case "bool":
		return "true"
	default:
		// int, int64, int32, float64, float32
		return "1"
	}
}
