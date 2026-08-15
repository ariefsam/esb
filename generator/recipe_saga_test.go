package generator

import (
	"os/exec"
	"testing"
)

// TestAddSaga_GeneratedProjectBuildsAndTests generates a saga recipe and
// asserts it compiles (including the wired log-stub port) and passes its
// generated scenario tests (happy path, debit-fails, credit-fails-compensates).
func TestAddSaga_GeneratedProjectBuildsAndTests(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	if err := InitProject("example.com/bank", dir); err != nil {
		t.Fatalf("InitProject() error = %v", err)
	}
	if err := AddSaga("money_transfer"); err != nil {
		t.Fatalf("AddSaga() error = %v", err)
	}

	build := exec.Command("go", "build", "./...")
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("generated saga project does not compile: %v\n%s", err, out)
	}

	test := exec.Command("go", "test", "./service/...")
	test.Dir = dir
	if out, err := test.CombinedOutput(); err != nil {
		t.Fatalf("generated saga scenario tests failed: %v\n%s", err, out)
	}
}
