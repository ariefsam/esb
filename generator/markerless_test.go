package generator

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestAdd_WorksWithoutMarkers deletes every `// esb:inject:` marker before
// each step of a full add flow, as a hand edit might, and requires the flow
// to still wire everything and the project to build, vet and pass its
// generated tests. Placement then comes from the constructs the markers sat
// in (see injector/targets.go), not from the markers.
func TestAdd_WorksWithoutMarkers(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	if err := InitProject("example.com/shop", dir); err != nil {
		t.Fatalf("InitProject() error = %v", err)
	}
	fields, err := ParseFields([]string{"amount:int64"})
	if err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		name string
		run  func() error
	}{
		{"aggregate order", func() error { return AddAggregate("order") }},
		{"aggregate product", func() error { return AddAggregate("product") }},
		{"event OrderPlaced", func() error { return AddEvent("order", "OrderPlaced", fields) }},
		{"handler place_order", func() error { return AddHandler("place_order", "order") }},
		{"handler cancel_order", func() error { return AddHandler("cancel_order", "order") }},
		{"projection sales_report", func() error { return AddProjection("sales_report", []string{"order", "product"}) }},
		{"crud invoice", func() error { return AddCRUD("invoice", fields) }},
		{"ledger account", func() error { return AddLedger("account") }},
		{"outbox order", func() error { return AddOutbox("order") }},
	}
	for _, s := range steps {
		stripMarkers(t, dir)
		if err := s.run(); err != nil {
			t.Fatalf("%s without markers: %v", s.name, err)
		}
	}

	wire := readGenerated(t, dir, "wire", "wire.go")
	for _, want := range []string{
		"OrderProjectionWorker *projection.OrderProjectionWorker",
		"SalesReportProjectionWorker *projection.SalesReportProjectionWorker",
		"PlaceOrderHandler *handler.PlaceOrderHandler",
		"orderSvc := service.NewOrderService(eventRepo)",
		"OrderProjectionWorker: orderWorker,",
	} {
		if !containsLine(wire, want) {
			t.Errorf("wire/wire.go is missing %q:\n%s", want, wire)
		}
	}
	if !strings.Contains(readGenerated(t, dir, "projection", "db.go"), "&OrderRow{}") {
		t.Error("projection/db.go does not migrate OrderRow")
	}
	if !strings.Contains(readGenerated(t, dir, "main.go"), "app.SalesReportProjectionWorker") {
		t.Error("main.go does not start SalesReportProjectionWorker")
	}
	if !strings.Contains(readGenerated(t, dir, "server", "routes.go"), "app.PlaceOrderHandler.Handle") {
		t.Error("server/routes.go has no PlaceOrderHandler route hint")
	}

	for _, args := range [][]string{{"build", "./..."}, {"vet", "./..."}, {"test", "./..."}} {
		cmd := exec.Command("go", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %s on the markerless project: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
}

// stripMarkers removes every `// esb:inject:` comment line from the Go files
// under dir.
func stripMarkers(t *testing.T, dir string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var kept []string
		for _, line := range strings.Split(string(src), "\n") {
			if !strings.HasPrefix(strings.TrimSpace(line), "// esb:inject:") {
				kept = append(kept, line)
			}
		}
		return os.WriteFile(path, []byte(strings.Join(kept, "\n")), 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}
