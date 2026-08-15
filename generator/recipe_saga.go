package generator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ariefsam/esb/injector"
	"github.com/ariefsam/esb/naming"
)

// AddSaga scaffolds an orchestration saga (process manager): a two-step
// transfer with compensation. It coordinates a Debit then a Credit through a
// Port interface, recording its own event stream (Requested -> Debited ->
// Credited -> Completed, or -> Failed / -> Compensated) so the outcome is
// auditable. Everything is staged in one injector.Tx.
func AddSaga(name string) error {
	if err := validateSnakeName("saga name", name); err != nil {
		return err
	}
	moduleName, err := ReadModuleName()
	if err != nil {
		return err
	}

	pascal := naming.ToPascalCase(name)
	data := LedgerData{ // reuses the same field set (fixed-shape recipe, no user fields)
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
		{"recipe/saga_domain.go.tmpl", "domain/" + name + ".go"},
		{"recipe/saga_service.go.tmpl", "service/" + name + ".go"},
		{"recipe/saga_port.go.tmpl", "service/" + name + "_port.go"},
		{"recipe/saga_scenario_test.go.tmpl", "service/" + name + "_scenario_test.go"},
		{"recipe/saga_row.go.tmpl", "projection/" + name + "_row.go"},
		{"recipe/saga_worker.go.tmpl", "projection/" + name + "_worker.go"},
		{"recipe/saga_query.go.tmpl", "projection/" + name + "_query.go"},
		{"recipe/saga_handler.go.tmpl", "server/handler/" + name + ".go"},
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

	if ok, err := tx.Contains("projection/db.go", pascal+"Row{}"); err != nil {
		return err
	} else if !ok {
		if err := tx.InjectAfterMarker("projection/db.go", "// esb:inject:automigrate-models", "\t\t&"+pascal+"Row{},"); err != nil {
			return err
		}
		actions = append(actions, "  update  projection/db.go")
	}

	// The saga service needs a Port; wire the generated log-only stub so the
	// project compiles. Replace it with a real adapter in wire/wire.go.
	serviceCtor := "service.New" + pascal + "Service(eventRepo, service." + pascal + "LogPort{})"
	if err := wireAggregateSliceWithService(tx, moduleName, pascal, serviceCtor, &actions); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	for _, a := range actions {
		fmt.Println(a)
	}
	fmt.Printf("\nSaga %q ready: Transfer command with compensation, %sPort interface (wired to a log stub — replace it), read model, and scenario tests.\n",
		name, pascal)
	return nil
}
