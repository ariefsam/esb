package generator

import (
	"os/exec"
	"strings"
	"testing"
)

// TestAddFlow_GeneratedProjectCompiles is the end-to-end safety net that the
// assessment (C2) identified as missing. It walks the full README "hello
// world" path — init, then every `add` subcommand — inside a temp dir and
// asserts the result still `go build`s. This is the regression guard for C1
// (`add handler` used to emit references to an un-imported handler package and
// an un-constructed service, breaking the build on a new user's first run).
func TestAddFlow_GeneratedProjectCompiles(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir) // all Add* helpers operate on the current working directory

	if err := InitProject("example.com/shop", dir); err != nil {
		t.Fatalf("InitProject() error = %v", err)
	}

	if err := AddAggregate("order"); err != nil {
		t.Fatalf("AddAggregate(order) error = %v", err)
	}
	if err := AddAggregate("product"); err != nil {
		t.Fatalf("AddAggregate(product) error = %v", err)
	}

	orderFields, err := ParseFields([]string{"amount:int64", "currency:string"})
	if err != nil {
		t.Fatalf("ParseFields() error = %v", err)
	}
	if err := AddEvent("order", "OrderPlaced", orderFields); err != nil {
		t.Fatalf("AddEvent(order, OrderPlaced) error = %v", err)
	}

	// A handler is the command that used to break the build (C1).
	if err := AddHandler("place_order", "order"); err != nil {
		t.Fatalf("AddHandler(place_order, order) error = %v", err)
	}
	// A second handler on the same aggregate must reuse the single service
	// instance rather than re-declaring it (guards the shared-service path).
	if err := AddHandler("cancel_order", "order"); err != nil {
		t.Fatalf("AddHandler(cancel_order, order) error = %v", err)
	}

	if err := AddQuery("orders_by_buyer", "order"); err != nil {
		t.Fatalf("AddQuery() error = %v", err)
	}
	if err := AddProjection("sales_report", []string{"order", "product"}); err != nil {
		t.Fatalf("AddProjection() error = %v", err)
	}

	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated project does not compile after full add flow: %v\n%s", err, output)
	}

	// Also make sure `go vet` is happy — a stray unused import from an
	// injection would slip past `go build` in some cases but not vet.
	vet := exec.Command("go", "vet", "./...")
	vet.Dir = dir
	if output, err := vet.CombinedOutput(); err != nil {
		// go vet can be noisy; only fail on real diagnostics, not tool errors.
		if strings.Contains(string(output), "declared and not used") ||
			strings.Contains(string(output), "undefined:") {
			t.Fatalf("go vet reported issues after full add flow: %v\n%s", err, output)
		}
	}
}
