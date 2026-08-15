package generator

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestAddUpcaster_GeneratedProjectCompiles adds an aggregate + event + upcaster
// and asserts the project still builds, the registry exists, and the upcaster
// registers itself.
func TestAddUpcaster_GeneratedProjectCompiles(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	if err := InitProject("example.com/shop", dir); err != nil {
		t.Fatalf("InitProject() error = %v", err)
	}
	if err := AddAggregate("order"); err != nil {
		t.Fatal(err)
	}
	fields, _ := ParseFields([]string{"amount:int64"})
	if err := AddEvent("order", "OrderPlaced", fields); err != nil {
		t.Fatal(err)
	}
	if err := AddUpcaster("order", "OrderPlaced"); err != nil {
		t.Fatalf("AddUpcaster() error = %v", err)
	}

	// The registry ships from init; the upcaster file registers itself.
	if _, err := os.Stat(filepath.Join(dir, "domain", "upcast.go")); err != nil {
		t.Fatalf("registry domain/upcast.go missing: %v", err)
	}
	fn := filepath.Join(dir, "domain", "upcast_order_order_placed.go")
	b, err := os.ReadFile(fn)
	if err != nil {
		t.Fatalf("upcaster file missing: %v", err)
	}
	if !strings.Contains(string(b), `RegisterUpcaster("OrderPlaced"`) {
		t.Errorf("upcaster does not register itself:\n%s", b)
	}

	// A second upcaster for the same event must be rejected.
	if err := AddUpcaster("order", "OrderPlaced"); err == nil {
		t.Error("AddUpcaster twice = nil, want already-exists error")
	}

	build := exec.Command("go", "build", "./...")
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("generated project does not compile after add upcaster: %v\n%s", err, out)
	}
}
