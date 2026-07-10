package generator

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

// renderFile renders a template from the embedded FS to destPath.
// data is passed to the template. The file is gofmt'd if it ends in .go.
func renderFile(tmplName, destPath string, data any) error {
	src, err := templateFS.ReadFile("templates/" + tmplName)
	if err != nil {
		return fmt.Errorf("read template %s: %w", tmplName, err)
	}

	tmpl, err := template.New(tmplName).Parse(string(src))
	if err != nil {
		return fmt.Errorf("parse template %s: %w", tmplName, err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("execute template %s: %w", tmplName, err)
	}

	out := buf.Bytes()
	if strings.HasSuffix(destPath, ".go") {
		if formatted, err := format.Source(out); err == nil {
			out = formatted
		}
	}

	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return err
	}
	return os.WriteFile(destPath, out, 0644)
}

// renderString renders a template string with data and returns the result.
func renderString(tmpl string, data any) (string, error) {
	t, err := template.New("").Parse(tmpl)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// readModuleName reads the module name from go.mod in the current directory.
func ReadModuleName() (string, error) {
	data, err := os.ReadFile("go.mod")
	if err != nil {
		return "", fmt.Errorf("go.mod not found — run 'esb init' first")
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module ")), nil
		}
	}
	return "", fmt.Errorf("module directive not found in go.mod")
}
