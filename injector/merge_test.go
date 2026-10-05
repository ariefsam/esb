package injector

import (
	"go/parser"
	"go/token"
	"slices"
	"strings"
	"testing"
)

const mergeExisting = `package store

import (
	"context"
	"errors"
)

// Client talks to the store. The project added a field.
type Client struct {
	url   string
	retry int
}

// Store was changed by hand in the project.
func (c *Client) Store(ctx context.Context) error {
	if c.retry > 0 {
		return errors.New("custom")
	}
	return nil
}

// Custom is the project's own and must survive.
func Custom() {}

const (
	ModeA = "a"
)
`

const mergeGenerated = `package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm/clause"
)

// Client talks to the store.
type Client struct {
	url string
}

// Store appends an event.
func (c *Client) Store(ctx context.Context) error {
	return nil
}

// StoreAndWait is new in the template.
func (c *Client) StoreAndWait(ctx context.Context, d time.Duration) error {
	if err := c.Store(ctx); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	return nil
}

// Timeout is a new package-level value.
var Timeout = 2 * time.Second

var _ = errors.New

const (
	ModeA = "a"
	// ModeB is new.
	ModeB = "b"
)

func useClause() clause.Column { return clause.Column{} }
`

func TestMergeDecls_AddsOnlyWhatIsMissing(t *testing.T) {
	res, err := MergeDecls([]byte(mergeExisting), []byte(mergeGenerated))
	if err != nil {
		t.Fatalf("MergeDecls: %v", err)
	}
	got := string(res.Source)

	for _, want := range []string{
		"retry int",                     // the project's struct field
		`errors.New("custom")`,          // the project's Store body
		"func Custom() {}",              // the project's own function
		"func (c *Client) StoreAndWait", // new method
		"// StoreAndWait is new in the template.",
		"var Timeout = 2 * time.Second",
		"// ModeB is new.\nconst ModeB = \"b\"",
		`"fmt"`, `"time"`, `"gorm.io/gorm/clause"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("merged file lacks %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "func (c *Client) Store(") != 1 {
		t.Errorf("Store declared %d times", strings.Count(got, "func (c *Client) Store("))
	}
	if strings.Count(got, "ModeA") != 1 {
		t.Errorf("ModeA declared %d times", strings.Count(got, "ModeA"))
	}

	wantAdded := []string{"method Client.StoreAndWait", "var Timeout", "var _ = errors.New", "const ModeB", "func useClause"}
	if !slices.Equal(res.Added, wantAdded) {
		t.Errorf("Added = %q, want %q", res.Added, wantAdded)
	}
	wantDiffers := []string{"type Client", "method Client.Store"}
	if !slices.Equal(res.Differs, wantDiffers) {
		t.Errorf("Differs = %q, want %q", res.Differs, wantDiffers)
	}
	if !slices.Equal(res.Imports, []string{"fmt", "gorm.io/gorm/clause", "time"}) {
		t.Errorf("Imports = %q", res.Imports)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "", res.Source, 0); err != nil {
		t.Fatalf("merged file does not parse: %v", err)
	}
}

// Running it again changes nothing: everything is already there.
func TestMergeDecls_IsIdempotent(t *testing.T) {
	first, err := MergeDecls([]byte(mergeExisting), []byte(mergeGenerated))
	if err != nil {
		t.Fatalf("first merge: %v", err)
	}
	second, err := MergeDecls(first.Source, []byte(mergeGenerated))
	if err != nil {
		t.Fatalf("second merge: %v", err)
	}
	if len(second.Added) != 0 || len(second.Imports) != 0 {
		t.Fatalf("second merge added %q / %q", second.Added, second.Imports)
	}
	if string(second.Source) != string(first.Source) {
		t.Fatal("second merge changed the file")
	}
}

// Formatting and comments are not a difference.
func TestMergeDecls_FormattingIsNotADifference(t *testing.T) {
	existing := "package p\n\n// Old comment.\nfunc F( a int ) int { return a+1 }\n"
	generated := "package p\n\n// New comment.\nfunc F(a int) int {\n\treturn a + 1\n}\n"
	res, err := MergeDecls([]byte(existing), []byte(generated))
	if err != nil {
		t.Fatalf("MergeDecls: %v", err)
	}
	if len(res.Differs) != 0 || len(res.Added) != 0 {
		t.Fatalf("Differs = %q, Added = %q; want neither", res.Differs, res.Added)
	}
}

func TestMergeDecls_IotaContinuationIsSkipped(t *testing.T) {
	existing := "package p\n\nconst (\n\tA = iota\n)\n"
	generated := "package p\n\nconst (\n\tA = iota\n\tB\n)\n"
	res, err := MergeDecls([]byte(existing), []byte(generated))
	if err != nil {
		t.Fatalf("MergeDecls: %v", err)
	}
	if len(res.Added) != 0 || len(res.Skipped) != 1 || !strings.HasPrefix(res.Skipped[0], "const B") {
		t.Fatalf("Added = %q, Skipped = %q", res.Added, res.Skipped)
	}
}

func TestMergeDecls_FileWithoutImports(t *testing.T) {
	existing := "package p\n\nfunc A() {}\n"
	generated := "package p\n\nimport \"strings\"\n\nfunc A() {}\n\nfunc B() string { return strings.ToUpper(\"b\") }\n"
	res, err := MergeDecls([]byte(existing), []byte(generated))
	if err != nil {
		t.Fatalf("MergeDecls: %v", err)
	}
	if !strings.Contains(string(res.Source), "import \"strings\"") || !strings.Contains(string(res.Source), "func B()") {
		t.Fatalf("merged:\n%s", res.Source)
	}
}

func TestMergeDecls_RefusesAnotherPackageOrAlias(t *testing.T) {
	if _, err := MergeDecls([]byte("package a\n"), []byte("package b\n")); err == nil {
		t.Error("merged two packages")
	}
	existing := "package p\n\nimport str \"strings\"\n\nvar _ = str.ToUpper\n"
	generated := "package p\n\nimport \"strings\"\n\nfunc B() string { return strings.ToUpper(\"b\") }\n"
	if _, err := MergeDecls([]byte(existing), []byte(generated)); err == nil {
		t.Error("merged an import the project aliases differently")
	}
}
