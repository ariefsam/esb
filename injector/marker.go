// Package injector modifies existing generated files by locating marker
// comments and inserting code snippets at the right position.
//
// The file-path functions (InjectAfterMarker, EnsureImport, AlreadyContains)
// operate on one file at a time and write immediately. For multi-file edits
// that must be all-or-nothing, use a Tx (see tx.go) which stages every change
// in memory and only touches disk on a successful Commit.
package injector

import (
	"fmt"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// injectAfterMarker inserts code on the line immediately after the first
// occurrence of marker in content. Returns an error if the marker is absent.
// This is the pure-string core shared by the file API and Tx.
func injectAfterMarker(content, marker, code string) (string, error) {
	idx := strings.Index(content, marker)
	if idx == -1 {
		return "", fmt.Errorf("marker %q not found", marker)
	}

	// Find end of the marker line.
	end := strings.Index(content[idx:], "\n")
	if end == -1 {
		end = len(content) - idx
	}
	insertAt := idx + end + 1 // position right after the newline

	return content[:insertAt] + code + "\n" + content[insertAt:], nil
}

// ensureImport adds importPath to content's imports if no import of that path
// exists yet. Presence and placement both come from the parsed import
// declarations, so a path that only appears in a comment or a string does not
// count, a ")" inside the import block cannot misplace the insertion, and a
// file with a single-line import or none at all still gets one. This is the
// pure core shared by the file API and Tx.
func ensureImport(content, importPath string) (string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", content, parser.ImportsOnly)
	if err != nil {
		return "", fmt.Errorf("parse imports: %w", err)
	}
	for _, imp := range f.Imports {
		if p, err := strconv.Unquote(imp.Path.Value); err == nil && p == importPath {
			return content, nil
		}
	}
	return addImport(content, importSpec{path: importPath})
}

// containsIdent reports whether needle occurs in content as a whole token
// run: an occurrence only counts when it is not glued to more identifier
// characters on either side where the needle itself starts or ends with one.
// So "OrderProjectionWorker" does not match inside
// "PurchaseOrderProjectionWorker", and "orderSvc :=" does not match inside
// "reorderSvc :=". Comments are searched too, on purpose: generated route
// hints are commented-out TODO lines, and a guard that skipped comments would
// inject them again on every run.
func containsIdent(content, needle string) bool {
	if needle == "" {
		return true
	}
	first, _ := utf8.DecodeRuneInString(needle)
	last, _ := utf8.DecodeLastRuneInString(needle)
	for from := 0; ; {
		i := strings.Index(content[from:], needle)
		if i == -1 {
			return false
		}
		start := from + i
		end := start + len(needle)
		before, _ := utf8.DecodeLastRuneInString(content[:start])
		after, _ := utf8.DecodeRuneInString(content[end:])
		if (!isIdentRune(first) || start == 0 || !isIdentRune(before)) &&
			(!isIdentRune(last) || end == len(content) || !isIdentRune(after)) {
			return true
		}
		from = start + 1
	}
}

func isIdentRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// InjectAfterMarker finds the first occurrence of marker in file and inserts
// code on the line immediately after it. The marker line itself is preserved.
// Returns an error if the marker is not found.
func InjectAfterMarker(path, marker, code string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	result, err := injectAfterMarker(string(src), marker, code)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return writeFormatted(path, result)
}

// EnsureImport adds importPath to the import block of a Go source file if it
// is not already present. Presence is detected by an exact match of the
// quoted full import path (e.g. `"mymod/service"`), not the last segment.
func EnsureImport(path, importPath string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	result, err := ensureImport(string(src), importPath)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return writeFormatted(path, result)
}

// AlreadyContains reports whether path contains needle as a whole token run
// (see containsIdent). Used to guard against double-injection.
func AlreadyContains(path, needle string) (bool, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	return containsIdent(string(src), needle), nil
}

// writeFormatted writes content to path, running gofmt if it is a .go file.
func writeFormatted(path, content string) error {
	out := []byte(content)
	if strings.HasSuffix(path, ".go") {
		formatted, err := format.Source(out)
		if err == nil {
			out = formatted
		}
		// On format error, write as-is so the user can inspect.
	}
	return os.WriteFile(path, out, 0644)
}
