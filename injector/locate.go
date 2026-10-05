package injector

import (
	"fmt"
	"go/ast"
	"go/token"
	"strings"
)

// A Target says where generated code goes in an existing file: the marker
// comment the templates emit, and a Locator for the Go construct that marker
// lives in (the App struct, NewApp's return literal, the AutoMigrate call…).
//
// Inject prefers the marker while it is a real comment inside that construct,
// so output is exactly what marker injection always produced. When the marker
// was deleted, or moved out of the construct, the code goes to the
// construct's natural end instead of failing. A construct that cannot be
// found falls back to the marker wherever it is, and only when neither exists
// does Inject return an error. A marker is matched as a whole comment, never
// as text inside a string or a longer comment.
type Target struct {
	Marker string
	Find   Locator
}

// A Locator finds one construct in a parsed file. Build one with
// StructFields, CompositeLit, VarLit, CallArgs, SwitchCases, BlockEnd,
// BeforeReturn or BeforeFirstCall.
type Locator struct {
	desc string
	find func(f *ast.File) (spot, bool)
}

// spot is where a Locator puts new code.
type spot struct {
	scope ast.Node  // the marker only counts inside this node
	at    token.Pos // new code goes on its own line(s) right before this
	list  bool      // comma-separated elements: composite literal, call arguments
	last  ast.Node  // list only: the current last element, nil when empty
}

// StructFields appends a field to the struct type typeName.
func StructFields(typeName string) Locator {
	return Locator{
		desc: "struct type " + typeName,
		find: func(f *ast.File) (spot, bool) {
			for _, d := range f.Decls {
				gd, ok := d.(*ast.GenDecl)
				if !ok || gd.Tok != token.TYPE {
					continue
				}
				for _, s := range gd.Specs {
					ts, ok := s.(*ast.TypeSpec)
					if !ok || ts.Name.Name != typeName {
						continue
					}
					if st, ok := ts.Type.(*ast.StructType); ok {
						return spot{scope: st, at: st.Fields.Closing}, true
					}
				}
			}
			return spot{}, false
		},
	}
}

// CompositeLit appends an element to the first typeName{…} (or pkg.typeName,
// or &typeName{…}) literal inside function funcName.
func CompositeLit(funcName, typeName string) Locator {
	return Locator{
		desc: typeName + "{…} literal in " + funcName,
		find: func(f *ast.File) (spot, bool) {
			var lit *ast.CompositeLit
			inFunc(f, funcName, func(n ast.Node) bool {
				if cl, ok := n.(*ast.CompositeLit); ok && typeName == exprName(cl.Type) {
					lit = cl
				}
				return lit == nil
			})
			if lit == nil {
				return spot{}, false
			}
			return litSpot(lit), true
		},
	}
}

// VarLit appends an element to the composite literal assigned to varName
// inside function funcName (`workers := []Worker{…}` or `var workers = …`).
func VarLit(funcName, varName string) Locator {
	return Locator{
		desc: varName + " literal in " + funcName,
		find: func(f *ast.File) (spot, bool) {
			var lit *ast.CompositeLit
			match := func(lhs, rhs []ast.Expr) {
				for i := range lhs {
					id, ok := lhs[i].(*ast.Ident)
					if !ok || id.Name != varName || i >= len(rhs) {
						continue
					}
					if cl, ok := rhs[i].(*ast.CompositeLit); ok {
						lit = cl
					}
				}
			}
			inFunc(f, funcName, func(n ast.Node) bool {
				switch s := n.(type) {
				case *ast.AssignStmt:
					match(s.Lhs, s.Rhs)
				case *ast.ValueSpec:
					names := make([]ast.Expr, len(s.Names))
					for i, id := range s.Names {
						names[i] = id
					}
					match(names, s.Values)
				}
				return lit == nil
			})
			if lit == nil {
				return spot{}, false
			}
			return litSpot(lit), true
		},
	}
}

// CallArgs appends an argument to the first call of method (x.method(…) or
// method(…)) inside function funcName.
func CallArgs(funcName, method string) Locator {
	return Locator{
		desc: method + "(…) call in " + funcName,
		find: func(f *ast.File) (spot, bool) {
			var call *ast.CallExpr
			inFunc(f, funcName, func(n ast.Node) bool {
				if c, ok := n.(*ast.CallExpr); ok && exprName(c.Fun) == method {
					call = c
				}
				return call == nil
			})
			if call == nil {
				return spot{}, false
			}
			return spot{scope: call, at: call.Rparen, list: true, last: lastExpr(call.Args)}, true
		},
	}
}

// SwitchCases adds a case clause to the first expression switch inside
// function funcName, ahead of its default clause when it has one.
func SwitchCases(funcName string) Locator {
	return Locator{
		desc: "switch in " + funcName,
		find: func(f *ast.File) (spot, bool) {
			var sw *ast.SwitchStmt
			inFunc(f, funcName, func(n ast.Node) bool {
				if s, ok := n.(*ast.SwitchStmt); ok {
					sw = s
				}
				return sw == nil
			})
			if sw == nil {
				return spot{}, false
			}
			at := sw.Body.Rbrace
			for _, s := range sw.Body.List {
				if cc, ok := s.(*ast.CaseClause); ok && cc.List == nil {
					at = cc.Pos()
				}
			}
			return spot{scope: sw.Body, at: at}, true
		},
	}
}

// BlockEnd appends a statement at the end of function funcName's body.
func BlockEnd(funcName string) Locator {
	return Locator{
		desc: "func " + funcName,
		find: func(f *ast.File) (spot, bool) {
			fd := funcDecl(f, funcName)
			if fd == nil {
				return spot{}, false
			}
			return spot{scope: fd.Body, at: fd.Body.Rbrace}, true
		},
	}
}

// BeforeReturn adds a statement to function funcName's body, ahead of its
// last top-level return (or at the end when it has none).
func BeforeReturn(funcName string) Locator {
	return Locator{
		desc: "func " + funcName,
		find: func(f *ast.File) (spot, bool) {
			fd := funcDecl(f, funcName)
			if fd == nil {
				return spot{}, false
			}
			return spot{scope: fd.Body, at: returnPos(fd.Body)}, true
		},
	}
}

// BeforeFirstCall adds a statement to function funcName's body, ahead of the
// first top-level statement that calls into package pkg, else ahead of its
// last return. It keeps declarations above their first user: services above
// the handler constructors that take them.
func BeforeFirstCall(funcName, pkg string) Locator {
	return Locator{
		desc: "func " + funcName,
		find: func(f *ast.File) (spot, bool) {
			fd := funcDecl(f, funcName)
			if fd == nil {
				return spot{}, false
			}
			for _, s := range fd.Body.List {
				if callsInto(s, pkg) {
					return spot{scope: fd.Body, at: s.Pos()}, true
				}
			}
			return spot{scope: fd.Body, at: returnPos(fd.Body)}, true
		},
	}
}

// inject is the pure core of Tx.Inject.
func inject(src string, t Target, code string) (string, error) {
	fset, f, err := parseSrc(src)
	if err != nil {
		return "", err
	}
	s, found := t.Find.find(f)
	if found {
		if c := markerComment(f, t.Marker, s.scope); c != nil {
			return insertAfterLine(src, fset.Position(c.End()).Offset, code), nil
		}
		return insertAt(src, fset, s, code), nil
	}
	if c := markerComment(f, t.Marker, nil); c != nil {
		return insertAfterLine(src, fset.Position(c.End()).Offset, code), nil
	}
	return "", fmt.Errorf("neither %s nor marker %q found", t.Find.desc, t.Marker)
}

// Inject stages code into path at t (see Target).
func (t *Tx) Inject(path string, target Target, code string) error {
	return t.mutate(path, func(src string) (string, error) { return inject(src, target, code) })
}

// Region returns the source of the construct t locates and whether t's
// marker is a comment inside it, for readers that must see the same place
// Inject writes to. ok is false when the construct is absent or src does not
// parse.
func (t Target) Region(src string) (region string, marked, ok bool) {
	fset, f, err := parseSrc(src)
	if err != nil {
		return "", false, false
	}
	s, found := t.Find.find(f)
	if !found {
		return "", false, false
	}
	start := fset.Position(s.scope.Pos()).Offset
	end := fset.Position(s.scope.End()).Offset
	return src[start:end], markerComment(f, t.Marker, s.scope) != nil, true
}

// markerComment returns the comment whose whole text is marker, searching
// only inside scope when scope is not nil.
func markerComment(f *ast.File, marker string, scope ast.Node) *ast.Comment {
	if marker == "" {
		return nil
	}
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			if strings.TrimSpace(c.Text) != marker {
				continue
			}
			if scope == nil || (c.Pos() >= scope.Pos() && c.End() <= scope.End()) {
				return c
			}
		}
	}
	return nil
}

// insertAfterLine puts code on the line after the one holding offset off,
// the placement marker injection has always used.
func insertAfterLine(src string, off int, code string) string {
	end := strings.IndexByte(src[off:], '\n')
	if end == -1 {
		return src + "\n" + code + "\n"
	}
	at := off + end + 1
	return src[:at] + code + "\n" + src[at:]
}

// insertAt puts code right before s.at. When s.at starts its own line (the
// closing brace of a multi-line struct or literal, a return, a default
// clause) the code becomes the line(s) above it; otherwise it is spliced in
// on the same line, with a separating comma for list elements.
func insertAt(src string, fset *token.FileSet, s spot, code string) string {
	off := fset.Position(s.at).Offset
	lineStart := strings.LastIndexByte(src[:off], '\n') + 1
	if strings.TrimSpace(src[lineStart:off]) == "" {
		if s.list && !strings.HasSuffix(strings.TrimSpace(code), ",") {
			code += ","
		}
		return src[:lineStart] + code + "\n" + src[lineStart:]
	}
	if s.list {
		elem := strings.TrimSuffix(strings.TrimSpace(code), ",")
		if s.last != nil && strings.TrimSpace(src[fset.Position(s.last.End()).Offset:off]) != "," {
			elem = ", " + elem
		}
		return src[:off] + elem + src[off:]
	}
	return src[:off] + "\n" + code + "\n" + src[off:]
}

func funcDecl(f *ast.File, name string) *ast.FuncDecl {
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == name && fd.Body != nil {
			return fd
		}
	}
	return nil
}

// inFunc walks the body of function name (closures included) with visit.
func inFunc(f *ast.File, name string, visit func(ast.Node) bool) {
	if fd := funcDecl(f, name); fd != nil {
		ast.Inspect(fd.Body, visit)
	}
}

func litSpot(lit *ast.CompositeLit) spot {
	return spot{scope: lit, at: lit.Rbrace, list: true, last: lastExpr(lit.Elts)}
}

func lastExpr(xs []ast.Expr) ast.Node {
	if len(xs) == 0 {
		return nil
	}
	return xs[len(xs)-1]
}

// exprName is the final name of an identifier, selector, pointer or address
// expression: App, pkg.App, *App and &App all give "App".
func exprName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return x.Sel.Name
	case *ast.StarExpr:
		return exprName(x.X)
	case *ast.UnaryExpr:
		return exprName(x.X)
	}
	return ""
}

func returnPos(body *ast.BlockStmt) token.Pos {
	for i := len(body.List) - 1; i >= 0; i-- {
		if r, ok := body.List[i].(*ast.ReturnStmt); ok {
			return r.Pos()
		}
	}
	return body.Rbrace
}

// callsInto reports whether n contains a call to a function of package pkg.
func callsInto(n ast.Node, pkg string) bool {
	found := false
	ast.Inspect(n, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			if sel, ok := c.Fun.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == pkg {
					found = true
				}
			}
		}
		return !found
	})
	return found
}
