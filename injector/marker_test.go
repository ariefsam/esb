package injector

import (
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

func TestEnsureImport_NoImportBlock(t *testing.T) {
	p := writeTemp(t, "foo.go", "package foo\n")
	if err := EnsureImport(p, "example.com/mod/service"); err == nil {
		t.Fatal("expected error for missing import block, got nil")
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
