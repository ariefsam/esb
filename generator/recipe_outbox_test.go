package generator

import (
	"os/exec"
	"testing"
)

// TestAddOutbox_GeneratedProjectBuildsAndTests adds an aggregate + an outbox
// for it and asserts the project builds (both workers wired) and the generated
// outbox tests pass (idempotent ingest, publish, retry-on-failure).
func TestAddOutbox_GeneratedProjectBuildsAndTests(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	if err := InitProject("example.com/shop", dir); err != nil {
		t.Fatalf("InitProject() error = %v", err)
	}
	if err := AddAggregate("order"); err != nil {
		t.Fatal(err)
	}
	if err := AddOutbox("order"); err != nil {
		t.Fatalf("AddOutbox() error = %v", err)
	}

	build := exec.Command("go", "build", "./...")
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("generated outbox project does not compile: %v\n%s", err, out)
	}

	test := exec.Command("go", "test", "./projection/...")
	test.Dir = dir
	if out, err := test.CombinedOutput(); err != nil {
		t.Fatalf("generated outbox tests failed: %v\n%s", err, out)
	}
}
