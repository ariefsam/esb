package generator

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ariefsam/esb/injector"
)

// waitPastCode is the WaitPast every generated projection worker has,
// filled in with the worker type, its cursor row name and the aggregate
// names it fetches — read from the worker itself, so the waiter watches
// exactly the cursor and aggregates the worker uses.
const waitPastCode = `package projection

import "context"

// WaitPast implements domain.ProjectionWaiter: it returns once this
// worker has applied the event with this ID, so a command can hold its
// answer until its own write is readable (see
// domain.StoreAndWaitProjectionWorker).
func (w *%s) WaitPast(ctx context.Context, aggregateName string, id uint) error {
	return NewCursorWaiter(w.db, %s, %s).WaitPast(ctx, aggregateName, id)
}
`

// waitMethodChanges brings storeAndWait (with storeEvent) to the services
// and WaitPast to the projection workers of a project generated before
// they existed. Only those methods are added — never a declaration the
// project removed — and a service whose store was changed by hand is
// reported, not touched: storeEvent repeats the generated store's body
// and would bypass the change. Files with nothing to report are left out.
func waitMethodChanges(moduleName string) ([]ClientFileChange, map[string][]byte, error) {
	var changes []ClientFileChange
	outputs := map[string][]byte{}
	for _, dir := range []string{"service", "projection"} {
		paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			return nil, nil, err
		}
		for _, path := range paths {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return nil, nil, fmt.Errorf("read %s: %w", path, err)
			}
			var change ClientFileChange
			var out []byte
			if dir == "service" {
				change, out, err = serviceWaitChange(path, src, moduleName)
			} else {
				change, out, err = workerWaitChange(path, src)
			}
			if err != nil {
				return nil, nil, err
			}
			if len(change.Added)+len(change.Differs)+len(change.Skipped) == 0 {
				continue
			}
			changes = append(changes, change)
			if change.Changed() {
				outputs[path] = out
			}
		}
	}
	return changes, outputs, nil
}

func serviceWaitChange(path string, src []byte, moduleName string) (ClientFileChange, []byte, error) {
	change := ClientFileChange{Path: path}
	f, _, err := parseGo(path, src)
	if err != nil {
		return change, nil, err
	}
	methods := methodsByType(f)
	out := src
	for _, typ := range sortedKeys(methods) {
		m := methods[typ]
		agg, ok := strings.CutSuffix(typ, "Service")
		// A service with storeEvent already has the current store; if it
		// lacks storeAndWait, that was removed on purpose.
		if !ok || m["store"] == nil || m["storeAndWait"] != nil || m["storeEvent"] != nil {
			continue
		}
		data := AggregateData{ModuleName: moduleName, AggregateNamePascal: agg}
		legacy, err := renderTemplate("legacy_service_store.go.tmpl", data)
		if err != nil {
			return change, nil, err
		}
		same, err := injector.SameDecl(out, []byte(legacy), "method "+typ+".store")
		if err != nil {
			return change, nil, fmt.Errorf("%s: %w", path, err)
		}
		if !same {
			change.Skipped = append(change.Skipped, fmt.Sprintf(
				"method %s.storeAndWait not added: store differs from the one esb generated, and storeEvent would bypass that change; call domain.StoreAndWaitProjectionWorker from the command instead", typ))
			continue
		}
		rendered, err := renderTemplate("service.go.tmpl", data)
		if err != nil {
			return change, nil, err
		}
		res, err := injector.MergeSelectedDecls(out, []byte(rendered), "method "+typ+".storeEvent", "method "+typ+".storeAndWait")
		if err != nil {
			return change, nil, fmt.Errorf("%s: %w", path, err)
		}
		change.Added = append(change.Added, res.Added...)
		change.Imports = append(change.Imports, res.Imports...)
		out = res.Source
	}
	return change, out, nil
}

func workerWaitChange(path string, src []byte) (ClientFileChange, []byte, error) {
	change := ClientFileChange{Path: path}
	f, fset, err := parseGo(path, src)
	if err != nil {
		return change, nil, err
	}
	methods := methodsByType(f)
	var codes []string
	for _, typ := range structTypes(f) {
		if !strings.HasSuffix(typ.Name.Name, "ProjectionWorker") || methods[typ.Name.Name]["WaitPast"] != nil {
			continue
		}
		name := typ.Name.Name
		cursor, aggs := workerCursor(fset, src, methods[name])
		switch {
		case !hasField(typ, "db"):
			change.Skipped = append(change.Skipped, "method "+name+".WaitPast not added: the worker has no db field")
		case cursor == "" || aggs == "":
			change.Skipped = append(change.Skipped, "method "+name+".WaitPast not added: its projection_cursors name or FetchAll aggregates could not be read")
		default:
			codes = append(codes, fmt.Sprintf(waitPastCode, name, cursor, aggs))
		}
	}
	out := src
	for _, code := range codes {
		res, err := injector.MergeDecls(out, []byte(code))
		if err != nil {
			return change, nil, fmt.Errorf("%s: %w", path, err)
		}
		change.Added = append(change.Added, res.Added...)
		change.Imports = append(change.Imports, res.Imports...)
		out = res.Source
	}
	return change, out, nil
}

// workerCursor reads, from a worker's methods, the quoted name of its
// projection_cursors row (the literal in `.Where("name = ?", "…")`) and
// the aggregate names it passes to FetchAll, as NewCursorWaiter arguments:
// the elements of a []string{…} literal as they are, anything else spread
// with "...". Either way it is the code the current templates generate.
func workerCursor(fset *token.FileSet, src []byte, methods map[string]*ast.FuncDecl) (cursor, aggs string) {
	for _, name := range sortedKeys(methods) {
		ast.Inspect(methods[name].Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch {
			case sel.Sel.Name == "Where" && len(call.Args) == 2 && cursor == "":
				if q, ok := stringLit(call.Args[0]); ok && q == "name = ?" {
					if _, ok := stringLit(call.Args[1]); ok {
						cursor = call.Args[1].(*ast.BasicLit).Value
					}
				}
			case sel.Sel.Name == "FetchAll" && len(call.Args) >= 2 && aggs == "":
				text := func(n ast.Node) string {
					return string(src[fset.Position(n.Pos()).Offset:fset.Position(n.End()).Offset])
				}
				if lit, ok := call.Args[1].(*ast.CompositeLit); ok && len(lit.Elts) > 0 {
					elts := make([]string, len(lit.Elts))
					for i, e := range lit.Elts {
						elts[i] = text(e)
					}
					aggs = strings.Join(elts, ", ")
				} else {
					aggs = text(call.Args[1]) + "..."
				}
			}
			return true
		})
	}
	return cursor, aggs
}

func parseGo(path string, src []byte) (*ast.File, *token.FileSet, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return f, fset, nil
}

// methodsByType maps each receiver type name to its methods by name.
func methodsByType(f *ast.File) map[string]map[string]*ast.FuncDecl {
	out := map[string]map[string]*ast.FuncDecl{}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv == nil || len(fd.Recv.List) == 0 || fd.Body == nil {
			continue
		}
		t := fd.Recv.List[0].Type
		if star, ok := t.(*ast.StarExpr); ok {
			t = star.X
		}
		id, ok := t.(*ast.Ident)
		if !ok {
			continue
		}
		if out[id.Name] == nil {
			out[id.Name] = map[string]*ast.FuncDecl{}
		}
		out[id.Name][fd.Name.Name] = fd
	}
	return out
}

func structTypes(f *ast.File) []*ast.TypeSpec {
	var out []*ast.TypeSpec
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, s := range gd.Specs {
			if ts, ok := s.(*ast.TypeSpec); ok {
				if _, ok := ts.Type.(*ast.StructType); ok {
					out = append(out, ts)
				}
			}
		}
	}
	return out
}

func hasField(ts *ast.TypeSpec, name string) bool {
	for _, fld := range ts.Type.(*ast.StructType).Fields.List {
		for _, n := range fld.Names {
			if n.Name == name {
				return true
			}
		}
	}
	return false
}

func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	v, err := strconv.Unquote(lit.Value)
	return v, err == nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
