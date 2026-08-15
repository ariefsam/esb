package generator

import (
	"fmt"
	"strings"

	"github.com/ariefsam/esb/injector"
	"github.com/ariefsam/esb/naming"
)

// AddOutbox scaffolds a transactional outbox for an aggregate's events: an
// ingest worker that appends each event to an outbox table (idempotent by
// source event id) and a publisher worker that relays unpublished rows through
// a Publisher port (with a log stub), marking them published. Everything is
// staged in one injector.Tx.
func AddOutbox(name string) error {
	if err := validateSnakeName("outbox name", name); err != nil {
		return err
	}
	moduleName, err := ReadModuleName()
	if err != nil {
		return err
	}

	pascal := naming.ToPascalCase(name)
	data := LedgerData{ // fixed-shape recipe: reuses the common field set
		ModuleName:  moduleName,
		PackageName: naming.PackageName(moduleName),
		Name:        name,
		NamePascal:  pascal,
		NameKebab:   naming.ToKebabCase(name),
		Receiver:    strings.ToLower(pascal[:1]),
		TableName:   naming.ToPlural(name),
	}

	tx := injector.NewTx()
	var actions []string

	files := []struct {
		tmpl string
		dest string
	}{
		{"recipe/outbox_row.go.tmpl", "projection/" + name + "_outbox_row.go"},
		{"recipe/outbox_worker.go.tmpl", "projection/" + name + "_outbox_worker.go"},
		{"recipe/outbox_publisher.go.tmpl", "projection/" + name + "_outbox_publisher.go"},
		{"recipe/outbox_query.go.tmpl", "projection/" + name + "_outbox_query.go"},
		{"recipe/outbox_test.go.tmpl", "projection/" + name + "_outbox_test.go"},
	}
	for _, f := range files {
		content, err := renderTemplate(f.tmpl, data)
		if err != nil {
			return fmt.Errorf("generate %s: %w", f.dest, err)
		}
		tx.Create(f.dest, content)
		actions = append(actions, "  create  "+f.dest)
	}

	if err := injectAutoMigrateModel(tx, pascal+"OutboxRow", &actions); err != nil {
		return err
	}

	// Wire both background workers into App + main.go.
	if err := wireBackgroundWorker(tx, pascal+"OutboxWorker", "projection.New"+pascal+"OutboxWorker(esClient, db)", &actions); err != nil {
		return err
	}
	if err := wireBackgroundWorker(tx, pascal+"OutboxPublisher", "projection.New"+pascal+"OutboxPublisher(db, projection."+pascal+"LogPublisher{})", &actions); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	for _, a := range actions {
		fmt.Println(a)
	}
	fmt.Printf("\nOutbox %q ready: ingest worker + publisher (wired to a log stub — replace %sPublisher), idempotent + at-least-once.\n", name, pascal)
	return nil
}

// wireBackgroundWorker injects a no-dependency background worker (a Run(ctx)
// type constructed from wire locals) as an App field + init + return field and
// starts it in main.go. Used by recipes that add long-running workers with no
// HTTP surface (e.g. the outbox).
func wireBackgroundWorker(tx *injector.Tx, workerType, ctorExpr string, actions *[]string) error {
	if ok, err := tx.Contains("wire/wire.go", workerType); err != nil {
		return err
	} else if ok {
		return nil
	}
	workerVar := lcFirst(workerType)
	if err := tx.InjectAfterMarker("wire/wire.go", "// esb:inject:app-fields", "\t"+workerType+" *projection."+workerType); err != nil {
		return err
	}
	if err := tx.InjectAfterMarker("wire/wire.go", "// esb:inject:app-init", "\t"+workerVar+" := "+ctorExpr); err != nil {
		return err
	}
	if err := tx.InjectAfterMarker("wire/wire.go", "// esb:inject:app-return-fields", "\t\t"+workerType+": "+workerVar+","); err != nil {
		return err
	}
	if err := tx.InjectAfterMarker("main.go", "// esb:inject:projection-workers", "\t\tapp."+workerType+","); err != nil {
		return err
	}
	*actions = append(*actions, "  update  wire/wire.go")
	*actions = append(*actions, "  update  main.go")
	return nil
}
