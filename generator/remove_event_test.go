package generator

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRemoveEvent_AddThenRemoveStillBuilds walks add event → remove event and
// asserts the project compiles at each step and the event's code is gone.
func TestRemoveEvent_AddThenRemoveStillBuilds(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	if err := InitProject("example.com/shop", dir); err != nil {
		t.Fatal(err)
	}
	if err := AddAggregate("order"); err != nil {
		t.Fatal(err)
	}
	f1, _ := ParseFields([]string{"amount:int64"})
	if err := AddEvent("order", "OrderPlaced", f1); err != nil {
		t.Fatal(err)
	}
	// A prefix-sharing sibling to prove removal is exact.
	if err := AddEvent("order", "OrderPlacedRefunded", f1); err != nil {
		t.Fatal(err)
	}

	if err := RemoveEvent("order", "OrderPlaced"); err != nil {
		t.Fatalf("RemoveEvent() error = %v", err)
	}

	domainSrc, _ := os.ReadFile(filepath.Join(dir, "domain", "order.go"))
	if strings.Contains(string(domainSrc), "type OrderPlaced struct") {
		t.Error("OrderPlaced struct still present after remove")
	}
	if strings.Contains(string(domainSrc), `case "OrderPlaced":`) {
		t.Error("OrderPlaced Apply case still present after remove")
	}
	if !strings.Contains(string(domainSrc), "OrderPlacedRefunded") {
		t.Error("prefix-sharing sibling OrderPlacedRefunded was wrongly removed")
	}

	build := exec.Command("go", "build", "./...")
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("project does not compile after remove: %v\n%s", err, out)
	}
}

func TestRemoveEvent_Guards(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := InitProject("example.com/shop", dir); err != nil {
		t.Fatal(err)
	}
	if err := AddAggregate("order"); err != nil {
		t.Fatal(err)
	}
	f, _ := ParseFields([]string{"amount:int64"})
	if err := AddEvent("order", "OrderPlaced", f); err != nil {
		t.Fatal(err)
	}

	// Unknown event → error.
	if err := RemoveEvent("order", "Nope"); err == nil {
		t.Error("RemoveEvent(Nope) = nil, want not-found error")
	}

	// An upcaster targeting the event blocks removal.
	if err := AddUpcaster("order", "OrderPlaced"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveEvent("order", "OrderPlaced"); err == nil {
		t.Error("RemoveEvent with an upcaster present = nil, want block error")
	} else if !strings.Contains(err.Error(), "upcaster") {
		t.Errorf("error = %v, want upcaster block", err)
	}
}
