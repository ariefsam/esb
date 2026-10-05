package generator

import (
	"os/exec"
	"strings"
	"testing"
)

// TestAdd_SuffixNameIsStillWired guards the double-injection checks against
// substring false positives: once a component whose name ends in another's
// exists (PurchaseOrder before Order, PlaceOrder before Order, Reorder before
// Order), adding the shorter one must still wire it everywhere. The checks
// used to be a bare strings.Contains, so "OrderProjectionWorker" was found
// inside "PurchaseOrderProjectionWorker" and Order's worker, row model and
// handler were silently left out of wire.go, main.go, db.go and routes.go
// while the project still compiled.
func TestAdd_SuffixNameIsStillWired(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	if err := InitProject("example.com/shop", dir); err != nil {
		t.Fatalf("InitProject() error = %v", err)
	}
	for _, agg := range []string{"purchase_order", "reorder", "order", "product"} {
		if err := AddAggregate(agg); err != nil {
			t.Fatalf("AddAggregate(%s) error = %v", agg, err)
		}
	}
	// reorderSvc must not be taken for orderSvc.
	if err := AddHandler("place_reorder", "reorder"); err != nil {
		t.Fatalf("AddHandler(place_reorder) error = %v", err)
	}
	if err := AddHandler("place_order", "order"); err != nil {
		t.Fatalf("AddHandler(place_order) error = %v", err)
	}
	// PlaceOrderHandler must not be taken for OrderHandler.
	if err := AddHandler("order", "order"); err != nil {
		t.Fatalf("AddHandler(order) error = %v", err)
	}
	for _, p := range []string{"purchase_sales", "sales"} {
		if err := AddProjection(p, []string{"order", "product"}); err != nil {
			t.Fatalf("AddProjection(%s) error = %v", p, err)
		}
	}

	wire := readGenerated(t, dir, "wire", "wire.go")
	main := readGenerated(t, dir, "main.go")
	db := readGenerated(t, dir, "projection", "db.go")
	routes := readGenerated(t, dir, "server", "routes.go")

	for _, want := range []struct{ file, content, snippet string }{
		{"wire/wire.go", wire, "OrderProjectionWorker *projection.OrderProjectionWorker"},
		{"wire/wire.go", wire, "SalesProjectionWorker *projection.SalesProjectionWorker"},
		{"wire/wire.go", wire, "orderSvc :="},
		{"wire/wire.go", wire, "OrderHandler *handler.OrderHandler"},
		{"main.go", main, "app.OrderProjectionWorker,"},
		{"main.go", main, "app.SalesProjectionWorker,"},
		{"projection/db.go", db, "&OrderRow{},"},
		{"projection/db.go", db, "&SalesRow{},"},
	} {
		if !containsLine(want.content, want.snippet) {
			t.Errorf("%s is missing %q:\n%s", want.file, want.snippet, want.content)
		}
	}
	// The route hint is a commented TODO mid-line; the "app." prefix keeps
	// app.PlaceOrderHandler from satisfying it.
	if !strings.Contains(routes, "app.OrderHandler.Handle") {
		t.Errorf("server/routes.go is missing the OrderHandler route hint:\n%s", routes)
	}
	if n := countLines(wire, "orderSvc :="); n != 1 {
		t.Errorf("wire/wire.go declares orderSvc %d times, want 1", n)
	}

	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated project does not compile: %v\n%s", err, out)
	}
}

// containsLine reports whether some line of content, trimmed and with runs
// of spaces collapsed (gofmt aligns struct fields), starts with snippet.
func containsLine(content, snippet string) bool {
	return countLines(content, snippet) > 0
}

// countLines counts the lines of content that start with snippet, compared
// as containsLine does. Matching at line start (not anywhere in the line)
// keeps "orderSvc :=" from counting "reorderSvc :=".
func countLines(content, snippet string) int {
	n := 0
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(strings.Join(strings.Fields(line), " "), snippet) {
			n++
		}
	}
	return n
}
