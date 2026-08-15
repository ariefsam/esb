package injector

import (
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// mustParse fails if src is not valid Go — used to assert removals leave a
// still-parseable file.
func mustParse(t *testing.T, src string) {
	t.Helper()
	if _, err := parser.ParseFile(token.NewFileSet(), "", src, parser.AllErrors); err != nil {
		t.Fatalf("result does not parse: %v\n%s", err, src)
	}
}

const removalFixture = `package domain

import "encoding/json"

// OrderPlaced event.
type OrderPlaced struct {
	Amount int64 ` + "`json:\"amount\"`" + `
}

// OrderPlacedRefunded event.
type OrderPlacedRefunded struct {
	Amount int64 ` + "`json:\"amount\"`" + `
}

func NewOrderPlacedEvent(amount int64) OrderPlaced {
	return OrderPlaced{Amount: amount}
}

func NewOrderPlacedRefundedEvent(amount int64) OrderPlacedRefunded {
	return OrderPlacedRefunded{Amount: amount}
}

func (o *Order) Apply(eventName string, data json.RawMessage) error {
	switch eventName {
	case "OrderPlaced":
		var evt OrderPlaced
		if err := json.Unmarshal(data, &evt); err != nil {
			return err
		}
		// multi-line body with a comment
		o.Version++
		return nil
	case "OrderPlacedRefunded":
		o.Version++
		return nil
	default:
		return nil
	}
}
`

func TestRemoveTypeDecl(t *testing.T) {
	out, err := removeTypeDecl(removalFixture, "OrderPlaced")
	if err != nil {
		t.Fatal(err)
	}
	mustParse(t, out)
	if strings.Contains(out, "type OrderPlaced struct") {
		t.Error("OrderPlaced type still present")
	}
	// The prefix-sharing sibling must survive untouched.
	if !strings.Contains(out, "type OrderPlacedRefunded struct") {
		t.Error("OrderPlacedRefunded type was wrongly removed")
	}
	// Its doc comment must go with it.
	if strings.Contains(out, "// OrderPlaced event.") {
		t.Error("OrderPlaced doc comment left behind")
	}
}

func TestRemoveFuncDecl(t *testing.T) {
	out, err := removeFuncDecl(removalFixture, "NewOrderPlacedEvent")
	if err != nil {
		t.Fatal(err)
	}
	mustParse(t, out)
	if strings.Contains(out, "func NewOrderPlacedEvent(") {
		t.Error("NewOrderPlacedEvent still present")
	}
	if !strings.Contains(out, "func NewOrderPlacedRefundedEvent(") {
		t.Error("NewOrderPlacedRefundedEvent wrongly removed")
	}
}

func TestRemoveSwitchCase(t *testing.T) {
	out, err := removeSwitchCase(removalFixture, "Apply", "OrderPlaced")
	if err != nil {
		t.Fatal(err)
	}
	mustParse(t, out)
	if strings.Contains(out, `case "OrderPlaced":`) {
		t.Error(`case "OrderPlaced" still present`)
	}
	// The prefix-sharing case and the multi-line body's sibling survive.
	if !strings.Contains(out, `case "OrderPlacedRefunded":`) {
		t.Error(`case "OrderPlacedRefunded" wrongly removed`)
	}
	if !strings.Contains(out, "default:") {
		t.Error("default clause wrongly removed")
	}
}

func TestRemove_NotFoundErrors(t *testing.T) {
	if _, err := removeTypeDecl(removalFixture, "Nope"); err == nil {
		t.Error("removeTypeDecl(Nope) = nil, want error")
	}
	if _, err := removeFuncDecl(removalFixture, "Nope"); err == nil {
		t.Error("removeFuncDecl(Nope) = nil, want error")
	}
	if _, err := removeSwitchCase(removalFixture, "Apply", "Nope"); err == nil {
		t.Error("removeSwitchCase(Nope) = nil, want error")
	}
	if _, err := removeSwitchCase(removalFixture, "NoSuchFunc", "OrderPlaced"); err == nil {
		t.Error("removeSwitchCase in missing func = nil, want error")
	}
}

func TestRemove_InvalidGoErrors(t *testing.T) {
	if _, err := removeTypeDecl("package x\nthis is not go", "X"); err == nil {
		t.Error("removeTypeDecl on invalid Go = nil, want error")
	}
}

func TestTx_RemovePrimitivesStageAndCommit(t *testing.T) {
	dir := t.TempDir()
	p := writeTemp(t, "order.go", removalFixture)
	_ = dir

	tx := NewTx()
	if err := tx.RemoveTypeDecl(p, "OrderPlaced"); err != nil {
		t.Fatal(err)
	}
	if err := tx.RemoveFuncDecl(p, "NewOrderPlacedEvent"); err != nil {
		t.Fatal(err)
	}
	if err := tx.RemoveSwitchCase(p, "Apply", "OrderPlaced"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	got := readFile(t, p)
	mustParse(t, got)
	for _, banned := range []string{"type OrderPlaced struct", "func NewOrderPlacedEvent(", `case "OrderPlaced":`} {
		if strings.Contains(got, banned) {
			t.Errorf("after commit, still contains %q", banned)
		}
	}
	// Siblings intact.
	if !strings.Contains(got, "OrderPlacedRefunded") {
		t.Error("OrderPlacedRefunded lost")
	}
}
