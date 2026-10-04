package inspector

// Flow scanning recovers the *per-event* edges of a project. The scanners in
// scan.go stop at the aggregate level — they know "worker balance listens to
// bank-account" but not which event travels that edge. The three passes here
// close that gap by reading the three declaration shapes the generator emits:
//
//	service/<agg>.go            s.store(ctx, agg, "OrderPlaced", …) or s.storeWithKey(…),
//	                            directly or through unexported helpers, also of
//	                            another service reached via a field (s.carts.checkout)
//	server/handler/<h>.go       h.svc.Create(r.Context(), …), or any *service.<T> field
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

	"github.com/ariefsam/esb/naming"
)

// ServiceCommand is one exported method in service/ that reaches a store call
// — an entry point on the write side.
type ServiceCommand struct {
	Name  string   // method name, e.g. "Create"
	Emits []string // events of the owning Service's aggregate, sorted
	// Other are events stored on a different aggregate, reached through a
	// helper of another service (s.cycles.attachEnvelope(…)). Sorted.
	Other   []EventRef
	Dynamic bool // a store() call passed a non-literal event name
}

// EventRef names an event together with the aggregate it is stored on.
type EventRef struct {
	Aggregate string
	Event     string
}

// Service is one struct in service/ with commands: every <X>Service, plus any
// other struct (a resolver, a saga) whose exported methods reach a store call.
type Service struct {
	Name      string // snake_case: the file name for the file's <X>Service, else the struct name
	Struct    string // Go type name, e.g. "OrderService"
	File      string // project-relative path, e.g. "service/order.go"
	Aggregate string // resolved aggregate store name ("" if not detected)
	Commands  []ServiceCommand
}

// HandlerMethod is one exported method on a generated *<X>Handler and the
// service methods it calls through its *service.<T> fields.
type HandlerMethod struct {
	Name  string   // e.g. "Create"
	Calls []string // "<Type>.<Method>" for every h.<field>.<Method>(…), sorted
	// Queries are the projection functions called directly (projection.F(…)),
	// sorted: the handler reading the read model without a service.
	Queries []string
	Line    int // declaration line in the handler file
}

// svcMethod is one method declared in package service, as seen by the
// cross-struct walk in scanServices.
type svcMethod struct {
	structName string
	exported   bool
	emits      []string
	dynamic    bool
	found      bool       // the body calls store()/storeWithKey() itself
	calls      []string   // "<Type>.<Method>" keys of methods it calls
	esRead     bool       // calls a read method on an EventRepository field
	esWrite    bool       // calls a Store* method on an EventRepository field
	queries    []string   // projection functions it calls directly
	otherEmits []EventRef // esb:emits entries naming another aggregate
	annReads   []string   // esb:reads aggregates
	annWrites  []string   // esb:writes aggregates
}

// svcStruct is one struct declared in package service.
type svcStruct struct {
	name    string
	file    string            // file name without .go
	primary bool              // the file's first <X>Service struct (the generated one)
	service bool              // name ends in "Service"
	fields  map[string]string // field name → struct type in package service
	repos   map[string]bool   // fields typed *EventRepository (the event store)
}

// scanServices fills m.Service from service/*.go.
//
// The whole package is read at once because a command often stores through a
// helper of another service: EnvelopeService.OpenEnvelope calls
// s.cycles.attachEnvelope, which stores CycleEnvelopeAttached on the
// budget-cycle aggregate. Calls are resolved through struct field types
// (cycles *BudgetCycleService), so each store call is attributed to the
// aggregate of the service it is made on, not of the caller.
//
// Only unexported callees are followed. An exported method that stores is a
// command of its own and gets its own node, so following it would credit one
// event to two commands.
func scanServices(dir string, aggregateNames map[string]string, m *ProjectModel) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	structs := map[string]*svcStruct{}
	methods := map[string]*svcMethod{} // "<Type>.<Method>"
	var order []string                 // method keys in file/declaration order
	var files []*ast.File
	fsets := map[*ast.File]*token.FileSet{}
	fileNames := map[*ast.File]string{}

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, fset, err := parseGoFile(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		if file == nil {
			continue
		}
		base := strings.TrimSuffix(name, ".go")
		fsets[file] = fset
		fileNames[file] = "service/" + name
		primary, _ := declaredStructWithSuffix(file, "Service")
		for _, st := range structTypes(file) {
			structs[st.name] = &svcStruct{
				name:    st.name,
				file:    base,
				primary: st.name == primary,
				service: strings.HasSuffix(st.name, "Service") && len(st.name) > len("Service"),
				fields:  st.fields,
				repos:   st.repos,
			}
		}
		files = append(files, file)
	}

	for _, file := range files {
		projPkg := projectionImportName(file)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			recvName, recvType, ok := receiverOf(fn)
			if !ok || structs[recvType] == nil {
				continue
			}
			ann := annotationsOf(fn.Doc)
			if ann.has("ignore") {
				continue
			}
			emits, dynamic, found := storedEventNames(fn.Body, recvName)
			var otherEmits []EventRef
			if ann.has("emits") {
				// Declared events replace the guess: whatever the body passes
				// to store(), these are what it stores.
				for _, e := range ann["emits"] {
					if agg, ev, ok := strings.Cut(e, "/"); ok {
						otherEmits = append(otherEmits, EventRef{Aggregate: agg, Event: ev})
					} else if !contains(emits, e) {
						emits = append(emits, e)
					}
				}
				dynamic, found = false, true
			} else if dynamic && !isStoreMethod(recvType+"."+fn.Name.Name) {
				m.diag("warn", "dynamic-event", fileNames[file], fsets[file].Position(fn.Pos()).Line,
					recvType+"."+fn.Name.Name+" menyimpan event dengan nama yang dihitung saat runtime, jadi event-nya tidak diketahui",
					"tambahkan // esb:emits NamaEvent[, NamaEvent2] di atas method ini")
			}
			esRead, esWrite := eventStoreOps(fn.Body, recvName, structs[recvType].repos)
			key := recvType + "." + fn.Name.Name
			methods[key] = &svcMethod{
				structName: recvType,
				exported:   fn.Name.IsExported(),
				emits:      emits,
				dynamic:    dynamic,
				found:      found,
				calls:      methodCalls(fn.Body, recvName, recvType, structs[recvType].fields),
				esRead:     esRead,
				esWrite:    esWrite,
				otherEmits: otherEmits,
				annReads:   ann["reads"],
				annWrites:  ann["writes"],
				queries:    projectionCalls(fn.Body, projPkg),
			}
			order = append(order, key)
		}
	}

	aggregateOf := func(st *svcStruct) string {
		if st.service {
			if st.primary {
				return aggregateStoreName(aggregateNames, st.file)
			}
			return aggregateStoreName(aggregateNames, naming.ToSnakeCase(strings.TrimSuffix(st.name, "Service")))
		}
		return aggregateNames[naming.ToSnakeCase(st.name)]
	}

	type built struct{ cmds []ServiceCommand }
	byStruct := map[string]*built{}
	for _, key := range order {
		root := methods[key]
		if !root.exported {
			continue
		}
		emitted := map[EventRef]bool{}
		var refs []EventRef
		dynamic, found := false, false
		seen := map[string]bool{key: true}
		var walk func(mm *svcMethod)
		walk = func(mm *svcMethod) {
			found = found || mm.found
			dynamic = dynamic || mm.dynamic
			agg := aggregateOf(structs[mm.structName])
			for _, e := range mm.emits {
				ref := EventRef{Aggregate: agg, Event: e}
				if !emitted[ref] {
					emitted[ref] = true
					refs = append(refs, ref)
				}
			}
			for _, ref := range mm.otherEmits {
				if !emitted[ref] {
					emitted[ref] = true
					refs = append(refs, ref)
				}
			}
			for _, c := range mm.calls {
				callee, ok := methods[c]
				if !ok || callee.exported || seen[c] || isStoreMethod(c) {
					continue
				}
				seen[c] = true
				walk(callee)
			}
		}
		walk(root)
		if !found {
			continue
		}
		bs := byStruct[root.structName]
		if bs == nil {
			bs = &built{}
			byStruct[root.structName] = bs
		}
		// Every ref sits in Other until the owner's aggregate is known; the
		// loop below moves the owner's own events into Emits.
		bs.cmds = append(bs.cmds, ServiceCommand{
			Name:    strings.TrimPrefix(key, root.structName+"."),
			Other:   refs,
			Dynamic: dynamic,
		})
	}

	names := make([]string, 0, len(structs))
	for n := range structs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		st := structs[n]
		bs := byStruct[n]
		// A generated <X>Service is listed even before it has commands, as
		// before; any other struct only when it actually stores events.
		if bs == nil && !st.primary {
			continue
		}
		svc := Service{Struct: n, File: "service/" + st.file + ".go", Aggregate: aggregateOf(st)}
		svc.Name = naming.ToSnakeCase(n)
		if st.primary {
			svc.Name = st.file
		}
		if bs != nil {
			if svc.Aggregate == "" {
				svc.Aggregate = soleAggregate(bs.cmds)
			}
			for _, c := range bs.cmds {
				refs := c.Other
				c.Other = nil
				for _, r := range refs {
					if r.Aggregate == svc.Aggregate {
						c.Emits = append(c.Emits, r.Event)
					} else {
						c.Other = append(c.Other, r)
					}
				}
				sort.Strings(c.Emits)
				sort.Slice(c.Other, func(i, j int) bool {
					if c.Other[i].Aggregate != c.Other[j].Aggregate {
						return c.Other[i].Aggregate < c.Other[j].Aggregate
					}
					return c.Other[i].Event < c.Other[j].Event
				})
				svc.Commands = append(svc.Commands, c)
			}
			sort.Slice(svc.Commands, func(i, j int) bool { return svc.Commands[i].Name < svc.Commands[j].Name })
		}
		m.Service = append(m.Service, svc)
	}
	sort.Slice(m.Service, func(i, j int) bool { return m.Service[i].Name < m.Service[j].Name })

	// Event store access: every exported method, with what it reads and
	// writes in the event store, following every callee (exported too), since
	// here the question is "does this entry point hit the store", not "which
	// command owns this event".
	for _, key := range order {
		root := methods[key]
		if !root.exported {
			continue
		}
		reads, writes, queries := map[string]bool{}, map[string]bool{}, map[string]bool{}
		seen := map[string]bool{key: true}
		var walk func(mm *svcMethod)
		walk = func(mm *svcMethod) {
			for _, q := range mm.queries {
				queries[q] = true
			}
			agg := aggregateOf(structs[mm.structName])
			if agg != "" {
				if mm.esRead {
					reads[agg] = true
				}
				if mm.esWrite {
					writes[agg] = true
				}
			}
			for _, a := range mm.annReads {
				reads[a] = true
			}
			for _, a := range mm.annWrites {
				writes[a] = true
			}
			for _, c := range mm.calls {
				if callee, ok := methods[c]; ok && !seen[c] {
					seen[c] = true
					walk(callee)
				}
			}
		}
		walk(root)
		if len(reads) == 0 && len(writes) == 0 && len(queries) == 0 {
			continue
		}
		st := structs[root.structName]
		name := naming.ToSnakeCase(st.name)
		if st.primary {
			name = st.file
		}
		m.StoreAccess = append(m.StoreAccess, StoreAccess{
			Service: name,
			Struct:  st.name,
			Method:  strings.TrimPrefix(key, st.name+"."),
			Reads:   sortedKeys(reads),
			Writes:  sortedKeys(writes),
			Queries: sortedKeys(queries),
		})
	}
	sort.Slice(m.StoreAccess, func(i, j int) bool {
		a, b := m.StoreAccess[i], m.StoreAccess[j]
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		return a.Method < b.Method
	})
	return nil
}

// StoreAccess is one exported method in service/ that reaches the event store
// or the read model, directly or through any callee: the aggregates it reads
// and writes, and the projection functions it calls.
type StoreAccess struct {
	Service string // same naming as Service.Name
	Struct  string // Go type, e.g. "CycleResolver"
	Method  string
	Reads   []string // aggregates loaded (Retrieve, LatestSnapshot, FetchAll), sorted
	Writes  []string // aggregates written (StoreAtomic, StoreSnapshot), sorted
	Queries []string // projection functions it calls, transitively, sorted
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// eventStoreOps reports whether body reads from or writes to the event store
// through a field of the receiver typed as an EventRepository. Store* methods
// (StoreAtomic, StoreSnapshot) write; every other method reads.
func eventStoreOps(body *ast.BlockStmt, recvName string, repos map[string]bool) (read, write bool) {
	if len(repos) == 0 {
		return false, false
	}
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
		if !ok || !repos[field.Sel.Name] {
			return true
		}
		if ident, ok := field.X.(*ast.Ident); !ok || ident.Name != recvName {
			return true
		}
		if strings.HasPrefix(method.Sel.Name, "Store") {
			write = true
		} else {
			read = true
		}
		return true
	})
	return read, write
}

// isStoreMethod reports whether key ("<Type>.<Method>") is the store plumbing
// itself. Its event name is a parameter, already read at the call site, so
// walking into it would only mark the caller dynamic.
func isStoreMethod(key string) bool {
	return strings.HasSuffix(key, ".store") || strings.HasSuffix(key, ".storeWithKey")
}

// soleAggregate returns the aggregate every event of cmds is stored on, or ""
// when they span several. It names the aggregate of a non-service struct such
// as a resolver that only ever writes one aggregate.
func soleAggregate(cmds []ServiceCommand) string {
	agg := ""
	for _, c := range cmds {
		for _, r := range c.Other {
			if agg != "" && r.Aggregate != agg {
				return ""
			}
			agg = r.Aggregate
		}
	}
	return agg
}

type structType struct {
	name   string
	fields map[string]string
	repos  map[string]bool
}

// structTypes lists the struct types declared in file with, for each named
// field whose type is a local identifier (T or *T), that identifier.
func structTypes(file *ast.File) []structType {
	var out []structType
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
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}
			fields := map[string]string{}
			repos := map[string]bool{}
			if st.Fields != nil {
				for _, f := range st.Fields.List {
					t := f.Type
					if star, ok := t.(*ast.StarExpr); ok {
						t = star.X
					}
					typeName := ""
					switch x := t.(type) {
					case *ast.Ident:
						typeName = x.Name
						for _, nm := range f.Names {
							fields[nm.Name] = x.Name
						}
					case *ast.SelectorExpr:
						typeName = x.Sel.Name
					}
					if strings.HasSuffix(typeName, "EventRepository") {
						for _, nm := range f.Names {
							repos[nm.Name] = true
						}
					}
				}
			}
			out = append(out, structType{name: ts.Name.Name, fields: fields, repos: repos})
		}
	}
	return out
}

// methodCalls lists "<Type>.<Method>" for every <recv>.<M>(…) (Type is the
// receiver's own) and <recv>.<field>.<M>(…) (Type from the field's declared
// type) call in body.
func methodCalls(body *ast.BlockStmt, recvName, recvType string, fields map[string]string) []string {
	var calls []string
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch x := sel.X.(type) {
		case *ast.Ident:
			if x.Name == recvName {
				calls = append(calls, recvType+"."+sel.Sel.Name)
			}
		case *ast.SelectorExpr:
			if ident, ok := x.X.(*ast.Ident); ok && ident.Name == recvName {
				if t := fields[x.Sel.Name]; t != "" {
					calls = append(calls, t+"."+sel.Sel.Name)
				}
			}
		}
		return true
	})
	return calls
}

// storedEventNames walks a method body for `<recvName>.store(ctx, agg, X, …)`
// or the idempotent `<recvName>.storeWithKey(ctx, agg, X, data, key)`
// and reports the literal event names in X. found is false when the body never
// calls a store method itself.
func storedEventNames(body *ast.BlockStmt, recvName string) (emits []string, dynamic, found bool) {
	seen := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "store" && sel.Sel.Name != "storeWithKey") {
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
// methods each calls through a *service.<T> field (the generated svc field or
// any other, such as cycles *service.CycleResolver).
func handlerMethods(file *ast.File, fset *token.FileSet, structName string) []HandlerMethod {
	fields := serviceFields(file, structName)
	projPkg := projectionImportName(file)
	var out []HandlerMethod
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || !fn.Name.IsExported() {
			continue
		}
		recvName, recvType, ok := receiverOf(fn)
		if !ok || recvType != structName || annotationsOf(fn.Doc).has("ignore") {
			continue
		}
		calls := serviceCallsIn(fn.Body, recvName, fields)
		queries := projectionCalls(fn.Body, projPkg)
		if len(calls) == 0 && len(queries) == 0 {
			continue
		}
		out = append(out, HandlerMethod{
			Name: fn.Name.Name, Calls: calls, Queries: queries,
			Line: fset.Position(fn.Pos()).Line,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// serviceFields maps each field of structName typed *service.<T> (or
// service.<T>) to T.
func serviceFields(file *ast.File, structName string) map[string]string {
	fields := map[string]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != structName {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok || st.Fields == nil {
			return false
		}
		for _, f := range st.Fields.List {
			t := f.Type
			if star, ok := t.(*ast.StarExpr); ok {
				t = star.X
			}
			sel, ok := t.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "service" {
				continue
			}
			for _, nm := range f.Names {
				fields[nm.Name] = sel.Sel.Name
			}
		}
		return false
	})
	return fields
}

// serviceCallsIn collects "<T>.<M>" for every `<recvName>.<field>.<M>(…)` call
// whose field is typed *service.<T>.
func serviceCallsIn(body *ast.BlockStmt, recvName string, fields map[string]string) []string {
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
		if !ok {
			return true
		}
		if ident, ok := field.X.(*ast.Ident); !ok || ident.Name != recvName {
			return true
		}
		t := fields[field.Sel.Name]
		if t == "" {
			return true
		}
		key := t + "." + method.Sel.Name
		if !seen[key] {
			seen[key] = true
			calls = append(calls, key)
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
