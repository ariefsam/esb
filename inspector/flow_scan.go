package inspector

// Flow scanning recovers the *per-event* edges of a project. The scanners in
// scan.go stop at the aggregate level — they know "worker balance listens to
// bank-account" but not which event travels that edge. The three passes here
// close that gap by reading the three declaration shapes the generator emits:
//
//	service/<agg>.go            s.store(ctx, agg, "OrderPlaced", domain.OrderPlaced{…})
//	server/handler/<h>.go       h.svc.Create(r.Context(), …)
//	projection/<p>_worker.go    switch e.EventName { case "OrderPlaced": … }
//
// Together they give the write-side chain handler method → service command →
// event → projection worker, which is what BuildFlow turns into a graph.
//
// Every pass is best-effort and declaration-based, never line-based: an
// unrecognised shape yields no edge rather than a wrong one. A store() call
// whose event-name argument is not a string literal (the state-machine recipe
// passes a variable) sets Dynamic instead of inventing a name, so downstream
// gap reporting can say "emitted dynamically" rather than "no producer".

import (
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ServiceCommand is one exported command method on a generated
// *<X>Service — an entry point on the write side.
type ServiceCommand struct {
	Name    string   // method name, e.g. "Create"
	Emits   []string // events passed as a literal 3rd arg to store(), sorted
	Dynamic bool     // a store() call passed a non-literal event name
}

// Service is one file in service/ that declares a <X>Service struct.
type Service struct {
	Name      string // snake_case file name (without .go)
	Aggregate string // resolved aggregate store name ("" if not detected)
	Commands  []ServiceCommand
}

// HandlerMethod is one exported method on a generated *<X>Handler and the
// service commands it delegates to through the generated svc field.
type HandlerMethod struct {
	Name  string   // e.g. "Create"
	Calls []string // service method names invoked as h.svc.<M>(…), sorted
}

// scanServices fills m.Service from service/*.go. A file that declares no
// <X>Service struct (shared helpers, generated tests) is skipped.
func scanServices(dir string, aggregateNames map[string]string, m *ProjectModel) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, _, err := parseGoFile(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		if file == nil {
			continue
		}
		structName, ok := declaredStructWithSuffix(file, "Service")
		if !ok {
			continue
		}

		serviceName := strings.TrimSuffix(name, ".go")
		m.Service = append(m.Service, Service{
			Name:      serviceName,
			Aggregate: aggregateStoreName(aggregateNames, serviceName),
			Commands:  serviceCommands(file, structName),
		})
	}

	sort.Slice(m.Service, func(i, j int) bool { return m.Service[i].Name < m.Service[j].Name })
	return nil
}

// serviceCommands returns the exported methods on *structName that reach
// store(), with the event names each one emits. load/store are plumbing rather
// than commands, and an exported method that never calls store() (a read
// helper) is left out — the flow should show only edges that produce events.
func serviceCommands(file *ast.File, structName string) []ServiceCommand {
	var out []ServiceCommand
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || !fn.Name.IsExported() {
			continue
		}
		recvName, recvType, ok := receiverOf(fn)
		if !ok || recvType != structName {
			continue
		}
		emits, dynamic, found := storedEventNames(fn.Body, recvName)
		if !found {
			continue
		}
		sort.Strings(emits)
		out = append(out, ServiceCommand{Name: fn.Name.Name, Emits: emits, Dynamic: dynamic})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// storedEventNames walks a command body for `<recvName>.store(ctx, agg, X, …)`
// and reports the literal event names in X. found is false when the body never
// calls store() at all, which is how serviceCommands tells a command apart from
// a plain helper.
func storedEventNames(body *ast.BlockStmt, recvName string) (emits []string, dynamic, found bool) {
	seen := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "store" {
			return true
		}
		if ident, ok := sel.X.(*ast.Ident); !ok || ident.Name != recvName {
			return true
		}
		found = true
		// store(ctx, agg, eventName, data): the event name is the 3rd arg.
		if len(call.Args) < 3 {
			dynamic = true
			return true
		}
		name, ok := stringLit(call.Args[2])
		if !ok {
			dynamic = true
			return true
		}
		if !seen[name] {
			seen[name] = true
			emits = append(emits, name)
		}
		return true
	})
	return emits, dynamic, found
}

// handlerMethods returns the exported methods on *structName and the service
// commands each delegates to through the generated svc field.
func handlerMethods(file *ast.File, structName string) []HandlerMethod {
	var out []HandlerMethod
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || !fn.Name.IsExported() {
			continue
		}
		recvName, recvType, ok := receiverOf(fn)
		if !ok || recvType != structName {
			continue
		}
		calls := serviceCallsIn(fn.Body, recvName)
		if len(calls) == 0 {
			continue
		}
		out = append(out, HandlerMethod{Name: fn.Name.Name, Calls: calls})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// serviceCallsIn collects the <M> of every `<recvName>.svc.<M>(…)` call.
func serviceCallsIn(body *ast.BlockStmt, recvName string) []string {
	var calls []string
	seen := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		method, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		field, ok := method.X.(*ast.SelectorExpr)
		if !ok || field.Sel.Name != "svc" {
			return true
		}
		if ident, ok := field.X.(*ast.Ident); !ok || ident.Name != recvName {
			return true
		}
		if !seen[method.Sel.Name] {
			seen[method.Sel.Name] = true
			calls = append(calls, method.Sel.Name)
		}
		return true
	})
	sort.Strings(calls)
	return calls
}

// workerEventNames returns the event names a projection worker handles, read
// from the case labels of its `switch e.EventName` statement. The tag is
// matched on the selector's field name rather than on the variable, because the
// generated loop variable name is not part of the contract.
func workerEventNames(file *ast.File) []string {
	var names []string
	seen := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok || sw.Tag == nil || sw.Body == nil {
			return true
		}
		sel, ok := sw.Tag.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "EventName" {
			return true
		}
		for _, stmt := range sw.Body.List {
			clause, ok := stmt.(*ast.CaseClause)
			if !ok {
				continue
			}
			for _, expr := range clause.List {
				name, ok := stringLit(expr)
				if !ok || seen[name] {
					continue
				}
				seen[name] = true
				names = append(names, name)
			}
		}
		return true
	})
	sort.Strings(names)
	return names
}

// declaredStructWithSuffix returns the name of the first struct type declared
// in file whose name ends in suffix ("ProductService" for suffix "Service").
// It is how a generated service/handler file is told apart from a shared helper
// living in the same package.
func declaredStructWithSuffix(file *ast.File, suffix string) (string, bool) {
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			if _, ok := ts.Type.(*ast.StructType); !ok {
				continue
			}
			if strings.HasSuffix(ts.Name.Name, suffix) && len(ts.Name.Name) > len(suffix) {
				return ts.Name.Name, true
			}
		}
	}
	return "", false
}

// receiverOf returns the receiver variable name and base type name of fn.
// ok is false for functions, multi-receiver declarations, unnamed receivers,
// and receivers whose base type is not a plain identifier — none of which can
// carry the selector shapes the flow passes look for.
func receiverOf(fn *ast.FuncDecl) (varName, typeName string, ok bool) {
	if fn.Recv == nil || len(fn.Recv.List) != 1 {
		return "", "", false
	}
	field := fn.Recv.List[0]
	expr := field.Type
	if star, isStar := expr.(*ast.StarExpr); isStar {
		expr = star.X
	}
	ident, isIdent := expr.(*ast.Ident)
	if !isIdent || len(field.Names) != 1 {
		return "", "", false
	}
	return field.Names[0].Name, ident.Name, true
}
