package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAddEvent_TransactionalOnMissingMarker verifies #6: if an injection
// fails partway (here, because a required marker was removed), AddEvent must
// leave every file exactly as it was — no half-applied struct append, no
// stray import.
func TestAddEvent_TransactionalOnMissingMarker(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := InitProject("example.com/shop", dir); err != nil {
		t.Fatal(err)
	}
	if err := AddAggregate("order"); err != nil {
		t.Fatal(err)
	}

	domainFile := filepath.Join(dir, "domain", "order.go")
	before, err := os.ReadFile(domainFile)
	if err != nil {
		t.Fatal(err)
	}
	// Remove the worker-case marker so the LAST injection in AddEvent fails
	// after the domain-file mutations have already been staged.
	workerFile := filepath.Join(dir, "projection", "order_worker.go")
	wb, err := os.ReadFile(workerFile)
	if err != nil {
		t.Fatal(err)
	}
	corrupted := strings.Replace(string(wb), "// esb:inject:applyevent-cases", "// removed", 1)
	if corrupted == string(wb) {
		t.Fatal("test setup: worker marker not found")
	}
	if err := os.WriteFile(workerFile, []byte(corrupted), 0644); err != nil {
		t.Fatal(err)
	}

	fields, _ := ParseFields([]string{"amount:int64"})
	if err := AddEvent("order", "OrderPlaced", fields); err == nil {
		t.Fatal("AddEvent() = nil, want error (marker was removed)")
	}

	// The domain file must be byte-identical to before: no struct appended,
	// no "time" import added, no Apply case injected.
	after, err := os.ReadFile(domainFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Errorf("domain file was mutated despite the failed AddEvent:\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
}

// TestAddQuery_WordBoundaryGuard verifies #7: adding a query whose PascalCase
// name is a prefix of an existing query's name must not be falsely rejected as
// a duplicate. "Order" must be addable even though "OrderItems" exists.
func TestAddQuery_WordBoundaryGuard(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := InitProject("example.com/shop", dir); err != nil {
		t.Fatal(err)
	}
	if err := AddAggregate("order"); err != nil {
		t.Fatal(err)
	}

	if err := AddQuery("order_items", "order"); err != nil {
		t.Fatalf("AddQuery(order_items) error = %v", err)
	}
	// "Order" is a substring of "OrderItems" — the old bare-substring guard
	// would wrongly report it as already existing.
	if err := AddQuery("order", "order"); err != nil {
		t.Fatalf("AddQuery(order) falsely rejected as duplicate: %v", err)
	}
	// A genuine duplicate must still be rejected.
	if err := AddQuery("order", "order"); err == nil {
		t.Fatal("AddQuery(order) twice = nil, want duplicate error")
	}
}
