package generator

import (
	"os/exec"
	"testing"
)

// TestAddLedger_GeneratedProjectBuildsAndTests generates two ledger recipes
// into one project and asserts it compiles and passes its generated scenario
// tests (including the concurrent no-double-spend test). Two ledgers in one
// project also guards against cross-recipe symbol collisions in the shared
// service/handler packages.
func TestAddLedger_GeneratedProjectBuildsAndTests(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	if err := InitProject("example.com/bank", dir); err != nil {
		t.Fatalf("InitProject() error = %v", err)
	}
	if err := AddLedger("account"); err != nil {
		t.Fatalf("AddLedger(account) error = %v", err)
	}
	if err := AddLedger("wallet"); err != nil {
		t.Fatalf("AddLedger(wallet) error = %v", err)
	}

	build := exec.Command("go", "build", "./...")
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("generated ledger project does not compile: %v\n%s", err, out)
	}

	test := exec.Command("go", "test", "./service/...")
	test.Dir = dir
	if out, err := test.CombinedOutput(); err != nil {
		t.Fatalf("generated ledger scenario tests failed: %v\n%s", err, out)
	}
}
