package generator

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ariefsam/esb/injector"
	"github.com/ariefsam/esb/naming"
)

// AddIdempotency generates a reusable idempotency guard (AlreadyProcessed /
// Once) in the service package, backed by the event stream's IdempotencyKey.
// Use it to make command handling safe to retry: check at the top of a command
// and store the event with the same key. It is a shared helper, so it is
// generated once per project.
func AddIdempotency() error {
	moduleName, err := ReadModuleName()
	if err != nil {
		return err
	}

	dest := "service/idempotency.go"
	if _, statErr := os.Stat(filepath.FromSlash(dest)); statErr == nil {
		return fmt.Errorf("%s already exists", dest)
	}

	data := struct {
		ModuleName  string
		PackageName string
	}{ModuleName: moduleName, PackageName: naming.PackageName(moduleName)}

	tx := injector.NewTx()
	files := []struct{ tmpl, dst string }{
		{"service_idempotency.go.tmpl", dest},
		{"service_idempotency_test.go.tmpl", "service/idempotency_test.go"},
	}
	for _, f := range files {
		content, err := renderTemplate(f.tmpl, data)
		if err != nil {
			return fmt.Errorf("generate %s: %w", f.dst, err)
		}
		tx.Create(f.dst, content)
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	for _, f := range files {
		fmt.Printf("  create  %s\n", f.dst)
	}
	fmt.Println("\nIdempotency guard ready: wrap a command in service.Once(ctx, s.eventRepo, <AggregateName>, id, commandID, func() error { ... }) and store the event with IdempotencyKey = commandID.")
	return nil
}
