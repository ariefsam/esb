package generator

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ariefsam/esb/injector"
	"github.com/ariefsam/esb/naming"
)

// AddCRUD scaffolds a complete CRUD vertical slice for an entity aggregate:
// domain aggregate + Created/Updated/Archived/Restored events, a service with
// Create/Update/Archive/Restore commands (invariants enforced on the aggregate), a
// read-model row + projection worker, an activity log (who changed what,
// when), List/Get/Activities queries, write- and read-side HTTP handlers, and
// Given-When-Then scenario tests. Fields named in redact are logged as
// changed without their values. Everything is staged in one injector.Tx and
// committed together, so a failure leaves the project untouched.
func AddCRUD(name string, fields []FieldDef, redact ...string) error {
	if err := validateSnakeName("crud name", name); err != nil {
		return err
	}
	for _, f := range fields {
		// Events embed domain.Actor, whose field and JSON names these would shadow.
		if slices.Contains([]string{"actor", "actor_id", "actor_name"}, f.JSONTag) {
			return fmt.Errorf("field %q is reserved: CRUD events already carry the actor", f.JSONTag)
		}
	}
	for _, r := range redact {
		if !slices.ContainsFunc(fields, func(f FieldDef) bool { return f.JSONTag == r }) {
			return fmt.Errorf("--activity-redact: %q is not a field of %s", r, name)
		}
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
		Fields:      toCRUDFields(fields, redact),
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

	if err := ensureResponseHelper(tx, &actions); err != nil {
		return err
	}
	for _, f := range []struct{ tmpl, dest string }{
		{"recipe/crud_actor.go.tmpl", "domain/actor.go"},
		{"recipe/crud_activity.go.tmpl", "projection/activity.go"},
	} {
		if err := ensureSharedFile(tx, f.tmpl, f.dest, &actions); err != nil {
			return err
		}
	}

	if err := wireCRUD(tx, moduleName, data, &actions); err != nil {
		return err
	}

	if err := stageWaitSupport(tx, moduleName, &actions); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, a := range actions {
		fmt.Println(a)
	}
	fmt.Printf("\nCRUD %q ready: Create/Update/Archive/Restore commands, List%ss/Get%s/List%sActivities queries, and scenario tests.\n",
		name, data.NamePascal, data.NamePascal, data.NamePascal)
	return nil
}

// ensureSharedFile stages a file every CRUD slice uses (domain/actor.go,
// projection/activity.go) only when the project does not have it yet, so a
// second CRUD does not redefine its declarations.
func ensureSharedFile(tx *injector.Tx, tmpl, dest string, actions *[]string) error {
	if _, err := os.Stat(filepath.FromSlash(dest)); !os.IsNotExist(err) {
		return err
	}
	content, err := renderTemplate(tmpl, nil)
	if err != nil {
		return fmt.Errorf("generate %s: %w", dest, err)
	}
	tx.Create(dest, content)
	*actions = append(*actions, "  create  "+dest)
	return nil
}

// wireCRUD injects the read-model row AutoMigrate plus the projection worker,
// service, handler, and route hints for a CRUD slice into the existing
// wire/db/main files, staged into the recipe's transaction.
func wireCRUD(tx *injector.Tx, moduleName string, data CRUDData, actions *[]string) error {
	pascal := data.NamePascal
	handlerType := pascal + "Handler"

	// projection/db.go — AutoMigrate the read-model and activity rows.
	for _, row := range []string{pascal + "Row", pascal + "ActivityRow"} {
		if err := injectAutoMigrateModel(tx, row, actions); err != nil {
			return err
		}
	}

	// Shared wiring: service instance + projection worker + write-side handler.
	if err := wireAggregateSlice(tx, moduleName, pascal, actions); err != nil {
		return err
	}

	// The read-side handler queries the projection DB directly.
	queryType := pascal + "QueryHandler"
	if ok, err := tx.Contains("wire/wire.go", queryType); err != nil {
		return err
	} else if !ok {
		queryVar := lcFirst(pascal) + "QueryHandler"
		if err := tx.Inject("wire/wire.go", injector.AppFields, "\t"+queryType+" *handler."+queryType); err != nil {
			return err
		}
		if err := tx.Inject("wire/wire.go", injector.AppInit, "\t"+queryVar+" := handler.New"+queryType+"(db)"); err != nil {
			return err
		}
		if err := tx.Inject("wire/wire.go", injector.AppReturnFields, "\t\t"+queryType+": "+queryVar+","); err != nil {
			return err
		}
	}

	// server/routes.go — leave route hints for the write commands.
	for _, cmd := range []string{"create", "update", "archive", "restore"} {
		method := naming.ToPascalCase(cmd)
		route := "\t// TODO: router.HandleFunc(\"/" + data.NameKebab + "/" + cmd + "\", app." + handlerType + "." + method + ").Methods(http.MethodPost)"
		if ok, err := tx.Contains("server/routes.go", handlerType+"."+method); err != nil {
			return err
		} else if !ok {
			if err := tx.Inject("server/routes.go", injector.Routes, route); err != nil {
				return err
			}
		}
	}
	for _, q := range []string{"list", "get", "activities"} {
		method := naming.ToPascalCase(q)
		route := "\t// TODO: router.HandleFunc(\"/" + data.NameKebab + "/" + q + "\", app." + queryType + "." + method + ").Methods(http.MethodGet)"
		if ok, err := tx.Contains("server/routes.go", queryType+"."+method); err != nil {
			return err
		} else if !ok {
			if err := tx.Inject("server/routes.go", injector.Routes, route); err != nil {
				return err
			}
		}
	}

	return nil
}

// toCRUDFields converts parsed fields into template fields with sample literals
// for the generated scenario tests, marking the ones named in redact.
func toCRUDFields(fields []FieldDef, redact []string) []CRUDField {
	out := make([]CRUDField, 0, len(fields))
	for _, f := range fields {
		out = append(out, CRUDField{
			NamePascal: f.NamePascal,
			JSONTag:    f.JSONTag,
			Type:       f.Type,
			Sample:     sampleLiteral(f.Type),
			Redact:     slices.Contains(redact, f.JSONTag),
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
