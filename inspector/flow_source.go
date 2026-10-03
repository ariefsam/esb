package inspector

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"strings"
)

// SourceRef points a flow node at the declaration it was read from.
// File is slash-separated and relative to the project root.
type SourceRef struct {
	File string
	Line int
}

// AttachSources fills FlowNode.Source for every node in g. The scanners do
// not record positions, so each node is located by its conventional file
// (service/<name>.go, projection/<name>_worker.go, ...) and then by
// declaration name inside it. A node whose file does not exist keeps a zero
// Source and the UI shows it as not clickable. When the declaration is not
// found inside an existing file the line falls back to 1.
func AttachSources(m ProjectModel, root string, g *FlowGraph) {
	for ci := range g.Columns {
		for ni := range g.Columns[ci].Nodes {
			n := &g.Columns[ci].Nodes[ni]
			n.Source = resolveSource(m, root, *n)
		}
	}
}

// SourceFiles returns every file some flow node points at. The UI serves
// only these, so the source endpoint cannot be used to read arbitrary files.
func SourceFiles(m ProjectModel, root string) map[string]bool {
	g := BuildFlow(m, "")
	AttachSources(m, root, &g)
	files := map[string]bool{}
	for _, c := range g.Columns {
		for _, n := range c.Nodes {
			if n.Source.File != "" {
				files[n.Source.File] = true
			}
		}
	}
	return files
}

func resolveSource(m ProjectModel, root string, n FlowNode) SourceRef {
	var rel, decl string
	switch n.Kind {
	case FlowHandler:
		// ID is "handler:<file>.<Method>"; Method is empty for a handler
		// that calls nothing yet.
		id := strings.TrimPrefix(n.ID, "handler:")
		i := strings.LastIndex(id, ".")
		if i < 0 {
			return SourceRef{}
		}
		rel, decl = "server/handler/"+id[:i]+".go", id[i+1:]
	case FlowCommand:
		for _, s := range m.Service {
			if s.Name == n.Sub {
				rel = s.File
			}
		}
		decl = n.Label
	case FlowEvent:
		for _, a := range m.Aggregate {
			if a.Name == n.Aggregate {
				rel = "domain/" + a.FileName + ".go"
			}
		}
		decl = n.Label
	case FlowProjection:
		rel = "projection/" + n.Label + "_worker.go"
	case FlowQuery:
		rel, decl = "projection/query.go", n.Label
	}
	if rel == "" {
		return SourceRef{}
	}
	file, fset, err := parseGoFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil || file == nil {
		return SourceRef{}
	}
	line := 1
	if decl != "" {
		if pos := findDecl(file, decl); pos.IsValid() {
			line = fset.Position(pos).Line
		}
	}
	return SourceRef{File: rel, Line: line}
}

// findDecl returns the position of the function, method or type named name.
func findDecl(f *ast.File, name string) token.Pos {
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Name.Name == name {
				return d.Pos()
			}
		case *ast.GenDecl:
			for _, s := range d.Specs {
				if ts, ok := s.(*ast.TypeSpec); ok && ts.Name.Name == name {
					return ts.Pos()
				}
			}
		}
	}
	return token.NoPos
}
