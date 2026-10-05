package injector

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	return p
}

func TestInjectAfterMarker(t *testing.T) {
	// Non-.go file so content is preserved verbatim (no gofmt rewrite).
	p := writeTemp(t, "routes.txt", "line1\n// marker\nline3\n")
	if err := InjectAfterMarker(p, "// marker", "INSERTED"); err != nil {
		t.Fatalf("InjectAfterMarker() error = %v", err)
	}
	got, _ := os.ReadFile(p)
	want := "line1\n// marker\nINSERTED\nline3\n"
	if string(got) != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestInjectAfterMarker_MissingMarkerReturnsError(t *testing.T) {
	p := writeTemp(t, "routes.txt", "line1\nline2\n")
	err := InjectAfterMarker(p, "// nope", "X")
	if err == nil {
		t.Fatal("expected error for missing marker, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %v, want 'not found'", err)
	}
	// File must be untouched when the marker is absent.
	got, _ := os.ReadFile(p)
	if string(got) != "line1\nline2\n" {
		t.Fatalf("file was modified despite missing marker: %q", got)
	}
}

func TestInjectAfterMarker_MultipleInjectionsReverseOrder(t *testing.T) {
	// Two injections at the same marker: the last one lands closest to the
	// marker. This is the ordering the wire.go service/handler split relies on.
	p := writeTemp(t, "wire.txt", "// marker\n")
	if err := InjectAfterMarker(p, "// marker", "first"); err != nil {
		t.Fatal(err)
	}
	if err := InjectAfterMarker(p, "// marker", "second"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	want := "// marker\nsecond\nfirst\n"
	if string(got) != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestEnsureImport(t *testing.T) {
	src := `package foo

import (
	"fmt"
)

func Bar() {}
`
	p := writeTemp(t, "foo.go", src)
	if err := EnsureImport(p, "example.com/mod/service"); err != nil {
		t.Fatalf("EnsureImport() error = %v", err)
	}
	got, _ := os.ReadFile(p)
	if !strings.Contains(string(got), `"example.com/mod/service"`) {
		t.Fatalf("import not added:\n%s", got)
	}
	// gofmt must keep the file compilable (import inside the block).
	if !strings.Contains(string(got), "import (") {
		t.Fatalf("import block malformed:\n%s", got)
	}
}

func TestEnsureImport_Idempotent(t *testing.T) {
	src := `package foo

import (
	"example.com/mod/service"
)
`
	p := writeTemp(t, "foo.go", src)
	if err := EnsureImport(p, "example.com/mod/service"); err != nil {
		t.Fatalf("EnsureImport() error = %v", err)
	}
	got, _ := os.ReadFile(p)
	if n := strings.Count(string(got), `"example.com/mod/service"`); n != 1 {
		t.Fatalf("import appears %d times, want 1:\n%s", n, got)
	}
}

func TestEnsureImport_EdgeCases(t *testing.T) {
	const svc = `"example.com/mod/service"`
	cases := []struct {
		name string
		src  string
		want int // occurrences of svc after EnsureImport
	}{
		{"no import at all", "package foo\n", 1},
		{"single-line import", "package foo\n\nimport \"fmt\"\n\nvar _ = fmt.Sprint\n", 1},
		{"paren in block comment", "package foo\n\nimport (\n\t\"fmt\" // see (docs)\n\t\"os\"\n)\n\nvar _, _ = fmt.Sprint, os.Exit\n", 1},
		{"path only in a comment", "package foo\n\n// uses \"example.com/mod/service\" later\nimport \"fmt\"\n\nvar _ = fmt.Sprint\n", 2},
		{"already imported with alias", "package foo\n\nimport svc \"example.com/mod/service\"\n\nvar _ = svc.X\n", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := writeTemp(t, "foo.go", tc.src)
			if err := EnsureImport(p, "example.com/mod/service"); err != nil {
				t.Fatalf("EnsureImport() error = %v", err)
			}
			got, _ := os.ReadFile(p)
			if n := strings.Count(string(got), svc); n != tc.want {
				t.Fatalf("import path appears %d times, want %d:\n%s", n, tc.want, got)
			}
			if _, err := parser.ParseFile(token.NewFileSet(), "", got, parser.ImportsOnly); err != nil {
				t.Fatalf("result does not parse: %v\n%s", err, got)
			}
		})
	}
}

func TestContainsIdent(t *testing.T) {
	cases := []struct {
		content, needle string
		want            bool
	}{
		{"x PurchaseOrderProjectionWorker y", "OrderProjectionWorker", false},
		{"x PurchaseOrderProjectionWorker OrderProjectionWorker", "OrderProjectionWorker", true},
		{"app.OrderProjectionWorker,", "OrderProjectionWorker", true},
		{"OrderProjectionWorkerV2", "OrderProjectionWorker", false},
		{"&PurchaseOrderRow{},", "OrderRow{}", false},
		{"&OrderRow{},", "OrderRow{}", true},
		{"reorderSvc := 1", "orderSvc :=", false},
		{"orderSvc := 1", "orderSvc :=", true},
		{"// TODO: app.OrderHandler.Create)", "OrderHandler.Create", true},
		{"app.OrderHandler.CreateAll", "OrderHandler.Create", false},
		{"type OrderPlacedRefunded struct", "type OrderPlaced struct", false},
		{"ÄOrder", "Order", false},
	}
	for _, tc := range cases {
		if got := containsIdent(tc.content, tc.needle); got != tc.want {
			t.Errorf("containsIdent(%q, %q) = %v, want %v", tc.content, tc.needle, got, tc.want)
		}
	}
}

func TestAlreadyContains(t *testing.T) {
	p := writeTemp(t, "x.txt", "hello PlaceOrderHandler world\n")
	ok, err := AlreadyContains(p, "PlaceOrderHandler")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("AlreadyContains() = false, want true")
	}
	ok, _ = AlreadyContains(p, "Missing")
	if ok {
		t.Fatal("AlreadyContains() = true, want false")
	}
}
