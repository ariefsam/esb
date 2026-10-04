package inspector

import (
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// Annotations let a project state what the scanner cannot infer. They are
// line comments starting with "esb:" — in a function's doc comment, or
// anywhere in a domain file for esb:no-projection:
//
//	// esb:emits OrderPlaced, OrderPaid      service method: events it stores
//	                                          (aggregate/Event for another aggregate)
//	// esb:reads budget-cycle               service method: aggregates it loads
//	// esb:writes envelope                  service method: aggregates it stores to
//	// esb:no-projection                    domain file: no event of this aggregate
//	                                          needs a projection (on an event's
//	                                          type: only that event)
//	// esb:ignore                           service, handler or projection
//	                                          function: leave it out of the flow
//
// An annotation wins over inference: esb:emits on a method that stores a
// computed event name clears "event name computed at runtime".
// The generator's own "esb:inject:" markers are not annotations.
type annotationSet map[string][]string

func (a annotationSet) has(key string) bool {
	_, ok := a[key]
	return ok
}

// annotationsOf parses the esb: lines of a comment group.
func annotationsOf(groups ...*ast.CommentGroup) annotationSet {
	out := annotationSet{}
	for _, g := range groups {
		if g == nil {
			continue
		}
		for _, c := range g.List {
			text := strings.TrimSpace(strings.TrimPrefix(c.Text, "//"))
			if !strings.HasPrefix(text, "esb:") || strings.HasPrefix(text, "esb:inject") {
				continue
			}
			text = strings.TrimPrefix(text, "esb:")
			key, rest, _ := strings.Cut(text, " ")
			var args []string
			for _, f := range strings.FieldsFunc(rest, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
				args = append(args, f)
			}
			out[key] = append(out[key], args...)
		}
	}
	return out
}

// applyDomainAnnotations reads esb:no-projection from each aggregate's domain
// file: anywhere outside a type's doc it covers the whole aggregate, on an
// event type's doc only that event.
func applyDomainAnnotations(dir string, m *ProjectModel) error {
	for i := range m.Aggregate {
		a := &m.Aggregate[i]
		path := filepath.Join(dir, a.FileName+".go")
		file, _, err := parseGoFile(path)
		if err != nil {
			if os.IsNotExist(err) || strings.Contains(err.Error(), "no such file") {
				continue
			}
			return err
		}
		if file == nil {
			continue
		}
		typeDocs := map[*ast.CommentGroup]string{}
		for _, d := range file.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, s := range gd.Specs {
				ts, ok := s.(*ast.TypeSpec)
				if !ok {
					continue
				}
				doc := ts.Doc
				if doc == nil && len(gd.Specs) == 1 {
					doc = gd.Doc
				}
				if doc != nil {
					typeDocs[doc] = ts.Name.Name
				}
			}
		}
		for _, g := range file.Comments {
			if !annotationsOf(g).has("no-projection") {
				continue
			}
			if event, ok := typeDocs[g]; ok {
				a.NoProjection = append(a.NoProjection, event)
			} else {
				a.NoProjection = append(a.NoProjection, "*")
			}
		}
	}
	return nil
}

// skipsProjection reports whether event is marked esb:no-projection.
func (a Aggregate) skipsProjection(event string) bool {
	for _, e := range a.NoProjection {
		if e == "*" || e == event {
			return true
		}
	}
	return false
}
