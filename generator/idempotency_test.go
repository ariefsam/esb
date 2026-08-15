package generator

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestAddIdempotency_GeneratedProjectBuildsAndTests generates the idempotency
// guard into a project and asserts it builds and its generated tests pass.
func TestAddIdempotency_GeneratedProjectBuildsAndTests(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	if err := InitProject("example.com/shop", dir); err != nil {
		t.Fatalf("InitProject() error = %v", err)
	}
	// A service package must exist for the helper to live in.
	if err := AddAggregate("order"); err != nil {
		t.Fatal(err)
	}
	if err := AddIdempotency(); err != nil {
		t.Fatalf("AddIdempotency() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "service", "idempotency.go")); err != nil {
		t.Fatalf("service/idempotency.go not generated: %v", err)
	}
	// Generated once — a second run is rejected.
	if err := AddIdempotency(); err == nil {
		t.Error("AddIdempotency twice = nil, want already-exists error")
	}

	build := exec.Command("go", "build", "./...")
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("generated project does not compile after add idempotency: %v\n%s", err, out)
	}
	test := exec.Command("go", "test", "./service/...")
	test.Dir = dir
	if out, err := test.CombinedOutput(); err != nil {
		t.Fatalf("generated idempotency tests failed: %v\n%s", err, out)
	}
}
