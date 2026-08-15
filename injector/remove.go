package injector

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
)

// The removal primitives are AST-based (go/parser), not string/regex, so an
// event name that is a prefix of another (OrderPlaced vs OrderPlacedRefunded)
// can never be false-matched, and multi-line case clauses are handled by node
// boundaries rather than brace counting. Each returns an error when the target
// is absent or the source does not parse; nothing is a silent no-op.
//
// Removal is done by cutting the target node's byte range (including its doc
// comment) out of the source, which preserves the exact formatting of
// everything else. Tx.Commit re-runs gofmt, collapsing any leftover blank line.

func parseSrc(src string) (*token.FileSet, *ast.File, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.ParseComments)
	if err != nil {
		return nil, nil, fmt.Errorf("parse Go source: %w", err)
	}
	return fset, f, nil
}

// docPos returns the doc comment's start position when present, so the comment
// is removed along with its declaration.
func docPos(doc *ast.CommentGroup, fallback token.Pos) token.Pos {
	if doc != nil {
		return doc.Pos()
	}
	return fallback
}

// cutNode removes the byte range [start, end) from src, then swallows trailing
// horizontal whitespace and one newline so no dangling blank fragment is left.
func cutNode(src string, fset *token.FileSet, start, end token.Pos) string {
	b := []byte(src)
	s := fset.Position(start).Offset
	e := fset.Position(end).Offset
	for e < len(b) && (b[e] == ' ' || b[e] == '\t') {
		e++
	}
	if e < len(b) && b[e] == '\n' {
		e++
	}
	return string(b[:s]) + string(b[e:])
}

func stringLitValue(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	v, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return v, true
}

// removeTypeDecl removes a top-level `type <typeName> ...` declaration (the
// whole GenDecl when it is the only spec, or just the spec inside a group).
func removeTypeDecl(src, typeName string) (string, error) {
	fset, f, err := parseSrc(src)
	if err != nil {
		return "", err
	}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != typeName {
				continue
			}
			if len(gd.Specs) == 1 {
				return cutNode(src, fset, docPos(gd.Doc, gd.Pos()), gd.End()), nil
			}
			return cutNode(src, fset, docPos(ts.Doc, ts.Pos()), ts.End()), nil
		}
	}
	return "", fmt.Errorf("type %q not found", typeName)
}

// removeFuncDecl removes a top-level (non-method) function declaration.
func removeFuncDecl(src, funcName string) (string, error) {
	fset, f, err := parseSrc(src)
	if err != nil {
		return "", err
	}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv != nil || fd.Name.Name != funcName {
			continue
		}
		return cutNode(src, fset, docPos(fd.Doc, fd.Pos()), fd.End()), nil
	}
	return "", fmt.Errorf("func %q not found", funcName)
}

// removeSwitchCase removes the `case "<caseValue>":` clause from the first
// switch statement inside the function/method named funcName.
func removeSwitchCase(src, funcName, caseValue string) (string, error) {
	fset, f, err := parseSrc(src)
	if err != nil {
		return "", err
	}
	var target *ast.CaseClause
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Name.Name != funcName || fd.Body == nil {
			continue
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			if target != nil {
				return false
			}
			cc, ok := n.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, e := range cc.List {
				if v, ok := stringLitValue(e); ok && v == caseValue {
					target = cc
					return false
				}
			}
			return true
		})
		if target != nil {
			break
		}
	}
	if target == nil {
		return "", fmt.Errorf("case %q not found in %s", caseValue, funcName)
	}
	return cutNode(src, fset, target.Pos(), target.End()), nil
}

// RemoveTypeDecl stages the removal of a top-level type declaration in path.
func (t *Tx) RemoveTypeDecl(path, typeName string) error {
	return t.mutate(path, func(src string) (string, error) { return removeTypeDecl(src, typeName) })
}

// RemoveFuncDecl stages the removal of a top-level function declaration in path.
func (t *Tx) RemoveFuncDecl(path, funcName string) error {
	return t.mutate(path, func(src string) (string, error) { return removeFuncDecl(src, funcName) })
}

// RemoveSwitchCase stages the removal of a case clause from a switch inside the
// named function in path.
func (t *Tx) RemoveSwitchCase(path, funcName, caseValue string) error {
	return t.mutate(path, func(src string) (string, error) { return removeSwitchCase(src, funcName, caseValue) })
}

// mutate applies fn to path's staged content and stages the result.
func (t *Tx) mutate(path string, fn func(string) (string, error)) error {
	f, err := t.get(path)
	if err != nil {
		return err
	}
	res, err := fn(f.content)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	f.content = res
	f.dirty = true
	return nil
}
