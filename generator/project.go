package generator

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/ariefsam/esb/naming"
)

// InitProject scaffolds a new event sourcing project in destDir.
func InitProject(moduleName, destDir string) error {
	data := ProjectData{
		ModuleName:  moduleName,
		PackageName: naming.PackageName(moduleName),
	}

	files := []struct {
		tmpl string
		dest string
	}{
		{"main.go.tmpl", "main.go"},
		{"go.mod.tmpl", "go.mod"},
		{"makefile.tmpl", "Makefile"},
		{"env.example.tmpl", ".env.example"},
		{"gitignore.tmpl", ".gitignore"},
		{"agents_md.tmpl", "AGENTS.md"},
		{"domain_event.go.tmpl", "domain/event.go"},
		{"domain_errors.go.tmpl", "domain/errors.go"},
		{"domain_upcast.go.tmpl", "domain/upcast.go"},
		{"eventstore_client.go.tmpl", "eventstore/client.go"},
		{"eventstore_local_store.go.tmpl", "eventstore/local_store.go"},
		{"eventstore_fake_store.go.tmpl", "eventstore/fake_store.go"},
		{"testkit.go.tmpl", "testkit/testkit.go"},
		{"repository_adapter.go.tmpl", "repository/eventstore_adapter.go"},
		{"repository_local_adapter.go.tmpl", "repository/local_adapter.go"},
		{"projection_base.go.tmpl", "projection/worker.go"},
		{"projection_db.go.tmpl", "projection/db.go"},
		{"projection_query.go.tmpl", "projection/query.go"},
		{"projection_repository.go.tmpl", "projection/repository.go"},
		{"server_routes.go.tmpl", "server/routes.go"},
		{"wire_wire.go.tmpl", "wire/wire.go"},
	}

	for _, f := range files {
		dest := filepath.Join(destDir, f.dest)
		if err := renderFile(f.tmpl, dest, data); err != nil {
			return fmt.Errorf("generate %s: %w", f.dest, err)
		}
		fmt.Printf("  create  %s\n", f.dest)
	}

	// Run go mod tidy to resolve dependencies.
	fmt.Println("  run     go mod tidy")
	cmd := exec.Command("go", "mod", "tidy")
	cmd.Dir = destDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("  warn    go mod tidy failed: %v (run manually)\n", err)
	}

	return nil
}
