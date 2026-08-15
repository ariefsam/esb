package generator

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestAddCRUD_GeneratedProjectBuildsAndTests generates a CRUD recipe into a
// fresh project and asserts the result both compiles and passes its own
// generated Given-When-Then scenario tests. This is the end-to-end safety net
// for the recipe: a template that produces invalid Go, a broken wiring
// injection, or a scenario that does not hold would all fail here.
func TestAddCRUD_GeneratedProjectBuildsAndTests(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	if err := InitProject("example.com/shop", dir); err != nil {
		t.Fatalf("InitProject() error = %v", err)
	}

	fields, err := ParseFields([]string{"name:string", "price:int64", "in_stock:bool"})
	if err != nil {
		t.Fatalf("ParseFields() error = %v", err)
	}
	if err := AddCRUD("product", fields); err != nil {
		t.Fatalf("AddCRUD() error = %v", err)
	}

	// A second CRUD on the same project must also wire cleanly (shared
	// response.go generated once, distinct workers/handlers/services).
	custFields, _ := ParseFields([]string{"email:string"})
	if err := AddCRUD("customer", custFields); err != nil {
		t.Fatalf("AddCRUD(customer) error = %v", err)
	}

	build := exec.Command("go", "build", "./...")
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("generated CRUD project does not compile: %v\n%s", err, out)
	}

	// The generated scenario tests must pass — they exercise the real
	// Create/Update/Archive command methods against the in-memory store.
	test := exec.Command("go", "test", "./service/...")
	test.Dir = dir
	if out, err := test.CombinedOutput(); err != nil {
		t.Fatalf("generated CRUD scenario tests failed: %v\n%s", err, out)
	}

	// response.go must be generated exactly once (shared helper).
	if _, err := os.Stat(filepath.Join(dir, "server", "handler", "response.go")); err != nil {
		t.Fatalf("shared response.go not generated: %v", err)
	}

	// Soft delete, not hard delete: the aggregate carries an Archived flag.
	domainSrc, err := os.ReadFile(filepath.Join(dir, "domain", "product.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(domainSrc), "Archived") {
		t.Error("generated CRUD aggregate has no Archived flag (soft delete)")
	}
}
