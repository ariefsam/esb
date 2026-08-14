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

// renderTemplate renders a template from the embedded FS to a string. No
// gofmt or disk write happens here — callers that stage into an injector.Tx
// let Commit format and write, so the output stays raw.
func renderTemplate(tmplName string, data any) (string, error) {
	src, err := templateFS.ReadFile("templates/" + tmplName)
	if err != nil {
		return "", fmt.Errorf("read template %s: %w", tmplName, err)
	}

	tmpl, err := template.New(tmplName).Parse(string(src))
	if err != nil {
		return "", fmt.Errorf("parse template %s: %w", tmplName, err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute template %s: %w", tmplName, err)
	}
	return buf.String(), nil
}

// renderFile renders a template from the embedded FS to destPath.
// data is passed to the template. The file is gofmt'd if it ends in .go.
func renderFile(tmplName, destPath string, data any) error {
	rendered, err := renderTemplate(tmplName, data)
	if err != nil {
		return err
	}

	out := []byte(rendered)
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
