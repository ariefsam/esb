// Package injector modifies existing generated files by locating marker
// comments and inserting code snippets at the right position.
package injector

import (
	"fmt"
	"go/format"
	"os"
	"strings"
)

// InjectAfterMarker finds the first occurrence of marker in file and inserts
// code on the line immediately after it. The marker line itself is preserved.
// Returns an error if the marker is not found.
func InjectAfterMarker(path, marker, code string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	content := string(src)
	idx := strings.Index(content, marker)
	if idx == -1 {
		return fmt.Errorf("marker %q not found in %s", marker, path)
	}

	// Find end of the marker line.
	end := strings.Index(content[idx:], "\n")
	if end == -1 {
		end = len(content) - idx
	}
	insertAt := idx + end + 1 // position right after the newline

	result := content[:insertAt] + code + "\n" + content[insertAt:]
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
	content := string(src)

	// Already imported?
	quoted := `"` + importPath + `"`
	if strings.Contains(content, quoted) {
		return nil
	}

	// Find the import block end (last closing paren of import block) or the
	// single-line import statement.
	if idx := strings.Index(content, "import ("); idx != -1 {
		closeIdx := strings.Index(content[idx:], ")")
		if closeIdx == -1 {
			return fmt.Errorf("malformed import block in %s", path)
		}
		insertAt := idx + closeIdx // before the closing paren
		result := content[:insertAt] + "\t" + quoted + "\n" + content[insertAt:]
		return writeFormatted(path, result)
	}

	return fmt.Errorf("no import block found in %s", path)
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
