package inspector

import (
	"go/parser"
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Diagnostic is something the scanner could not understand, as opposed to a
// Gap, which is something the project does not have yet. Every diagnostic
// names where it is and how to make the scanner understand it, usually an
// annotation (see annotations.go).
type Diagnostic struct {
	Level   string // "warn" or "info"
	Code    string // stable identifier, e.g. "dynamic-event"
	File    string // project-relative, slash-separated
	Line    int
	Message string
	Hint    string
}

func (m *ProjectModel) diag(level, code, file string, line int, msg, hint string) {
	m.Diagnostics = append(m.Diagnostics, Diagnostic{
		Level: level, Code: code, File: file, Line: line, Message: msg, Hint: hint,
	})
}

// diagnoseSyntax reports Go files in the scanned folders that do not parse.
// The scanners skip such files silently (see parseGoFile), so without this a
// typo makes slices vanish from the flow with no explanation.
func diagnoseSyntax(root string, m *ProjectModel) {
	var files []string
	for _, dir := range []string{"domain", "service", filepath.Join("server", "handler"), "projection", "wire"} {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
				files = append(files, filepath.Join(dir, e.Name()))
			}
		}
	}
	files = append(files, "main.go")
	for _, rel := range files {
		src, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			continue
		}
		_, err = parser.ParseFile(token.NewFileSet(), rel, src, parser.SkipObjectResolution)
		if err == nil {
			continue
		}
		line, msg := 0, err.Error()
		if list, ok := err.(scanner.ErrorList); ok && len(list) > 0 {
			line, msg = list[0].Pos.Line, list[0].Msg
		}
		m.diag("warn", "parse-error", filepath.ToSlash(rel), line,
			"file tidak bisa di-parse, jadi dilewati scanner: "+msg,
			"perbaiki syntax-nya; isi file ini tidak ikut di graph sampai bisa di-parse")
	}
}

// diagnoseFlow reports what only shows once the graph is built: handlers
// whose calls lead nowhere the scanner knows, and read-model functions it
// could not tie to a table.
func diagnoseFlow(m *ProjectModel) {
	b := newFlowBuilder(*m)
	b.addCommands()
	known := b.methods
	for _, h := range m.Handler {
		for _, hm := range h.Methods {
			if len(hm.Queries) > 0 {
				continue
			}
			linked := false
			for _, c := range hm.Calls {
				if known[c] != "" {
					linked = true
				}
			}
			if linked || len(hm.Calls) == 0 {
				continue
			}
			m.diag("info", "handler-unknown-call", "server/handler/"+h.Name+".go", hm.Line,
				h.Name+"."+hm.Name+" memanggil "+strings.Join(hm.Calls, ", ")+
					", yang tidak menyentuh event store maupun read model",
				"kalau memang begitu (logout, health), tandai method handler dengan // esb:ignore; "+
					"kalau menyentuh lewat jalur lain, tandai method service-nya dengan // esb:reads / // esb:writes")
		}
	}
	for _, q := range m.Query {
		if len(q.Tables) == 0 && !q.Writes {
			m.diag("info", "query-no-table", q.File, q.Line,
				"query "+q.Name+" tidak dikenali tabelnya; hubungan ke projection hanya ditebak dari nama",
				"pakai tipe <X>Row yang punya TableName() (atau terdaftar di AutoMigrate), atau sebut nama tabelnya di SQL")
		}
	}
	sort.SliceStable(m.Diagnostics, func(i, j int) bool {
		a, b := m.Diagnostics[i], m.Diagnostics[j]
		if a.Level != b.Level {
			return a.Level == "warn"
		}
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	})
}
