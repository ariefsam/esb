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
	"os"
	"strings"
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

// ensureImport adds a quoted importPath to the import block of content if not
// already present. Presence is an exact match of the quoted full path. This is
// the pure-string core shared by the file API and Tx.
func ensureImport(content, importPath string) (string, error) {
	quoted := `"` + importPath + `"`
	if strings.Contains(content, quoted) {
		return content, nil
	}

	if idx := strings.Index(content, "import ("); idx != -1 {
		closeIdx := strings.Index(content[idx:], ")")
		if closeIdx == -1 {
			return "", fmt.Errorf("malformed import block")
		}
		insertAt := idx + closeIdx // before the closing paren
		return content[:insertAt] + "\t" + quoted + "\n" + content[insertAt:], nil
	}

	return "", fmt.Errorf("no import block found")
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

// AlreadyContains reports whether path contains the given string. Used to
// guard against double-injection.
func AlreadyContains(path, needle string) (bool, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	return strings.Contains(string(src), needle), nil
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
