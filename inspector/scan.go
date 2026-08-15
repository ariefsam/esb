// Package inspector reads the structure of an ESB-generated project
// without writing anything. It walks the file tree produced by `esb init`
// and surfaces an in-memory ProjectModel that the printer turns into a
// single-screen summary.
//
// Declaration-based facts (aggregates, events, handlers, queries, projection
// aggregate lists) are recovered with go/ast, so the scanner is indifferent to
// gofmt spacing and comment wording and never treats an unrelated struct as an
// event. The few marker-anchored blocks the generator injects into hand-shaped
// slices (wire App fields/init, AutoMigrate models, main.go workers) are still
// read by locating their `// esb:inject:*` markers, because there the contract
// is precisely "what was injected after this marker", not a declaration.
package inspector

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ariefsam/esb/naming"
)

// ProjectModel is the in-memory picture of one ESB project.
type ProjectModel struct {
	ModuleName  string
	PackageName string
	Aggregate   []Aggregate // every aggregate discovered in domain/, sorted by aggregate-store name
	Projection  []Projection
	Handler     []Handler
	Query       []Query
	Wire        WireGraph
	Migrate     []string    // GORM models in projection/db.go AutoMigrate
	RunWorker   []string    // workers in main.go
	Storage     StorageInfo // event store mode + per-aggregate event counts
}

// Aggregate is one file in domain/ (excluding event.go / errors.go).
type Aggregate struct {
	Name         string // aggregate-store name from the generated constant (for example "bank-account")
	FileName     string // snake_case file name (without .go), used to identify the root struct
	Events       []string
	EventDetails []EventDetail
}

// EventDetail describes one event's fields, extracted from its
// generated struct declaration in domain/<aggregate>.go. Fields is
// empty when the event name was found only via an Apply() case
// branch with no matching generated struct (e.g. a hand-written
// event that skipped `esb add event`).
type EventDetail struct {
	Name   string
	Fields []EventField
}

// EventField is one field of a generated event struct.
type EventField struct {
	Name    string // Go field name, PascalCase
	Type    string // Go type as written (string, int64, ...)
	JSONTag string
}

// Projection is one projection worker. Multi is true when the worker
// applies events from several aggregates (the worker file contains a
// `switch e.AggregateName` branch, or declares a "<name>AggregateNames"
// slice).
type Projection struct {
	Name       string // worker file name without _worker.go suffix
	Multi      bool
	Aggregates []string // aggregate names listened to
}

// Handler is one file in server/handler/.
type Handler struct {
	Name      string // snake_case file name (without .go)
	Aggregate string // resolved via the service field ("" if not detected)
}

// Query is one query function in projection/query.go.
type Query struct {
	Name      string
	Aggregate string // best-effort: derived from the row type the function returns
}

// WireGraph is the deconstructed wire/wire.go App.
type WireGraph struct {
	Fields []WireNode // declared fields on App (besides Env/Handler)
	Nodes  []WireNode // constructor expressions inside NewApp
}

// WireNode is one provider edge in the wire graph.
type WireNode struct {
	VarName  string // local var in NewApp, e.g. "orderWorker"
	Field    string // matching App field, e.g. "OrderProjectionWorker"
	Type     string // concrete type, e.g. "*projection.OrderProjectionWorker"
	Provider string // constructor call, e.g. "projection.NewOrderProjectionWorker(...)"
}

// NotFound is returned when Scan is run outside an ESB project (no go.mod).
type NotFound struct {
	Dir string
}

func (e *NotFound) Error() string {
	dir := e.Dir
	if dir == "" || dir == "." {
		if cwd, err := os.Getwd(); err == nil {
			dir = cwd
		}
	}
	return fmt.Sprintf("bukan proyek ESB — jalankan 'esb init' di direktori %s dulu", dir)
}

// Scan walks rootDir and returns a populated ProjectModel. A missing
// go.mod is reported as *NotFound so the CLI can print a friendly message.
func Scan(rootDir string) (ProjectModel, error) {
	var m ProjectModel

	goModPath := filepath.Join(rootDir, "go.mod")
	if _, err := os.Stat(goModPath); err != nil {
		return m, &NotFound{Dir: rootDir}
	}

	moduleName, err := readModuleName(goModPath)
	if err != nil {
		return m, err
	}
	m.ModuleName = moduleName
	m.PackageName = naming.PackageName(moduleName)

	if err := scanAggregates(filepath.Join(rootDir, "domain"), &m); err != nil {
		return m, err
	}
	aggregateNames := aggregateNamesByFile(m.Aggregate)
	if err := scanHandlers(filepath.Join(rootDir, "server", "handler"), aggregateNames, &m); err != nil {
		return m, err
	}
	if err := scanProjections(filepath.Join(rootDir, "projection"), aggregateNames, &m); err != nil {
		return m, err
	}
	if err := scanQueries(filepath.Join(rootDir, "projection"), aggregateNames, &m); err != nil {
		return m, err
	}
	if err := scanWire(filepath.Join(rootDir, "wire", "wire.go"), &m); err != nil {
		return m, err
	}
	if err := scanDB(filepath.Join(rootDir, "projection", "db.go"), &m); err != nil {
		return m, err
	}
	if err := scanMain(filepath.Join(rootDir, "main.go"), &m); err != nil {
		return m, err
	}
	m.Storage = ScanStorage(rootDir)

	return m, nil
}

// readModuleName extracts the first "module ..." line from go.mod.
func readModuleName(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module ")), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return "", fmt.Errorf("module directive not found in %s", path)
}

// parseGoFile reads and parses a Go source file into an AST. A read error
// (including a missing file) is returned to the caller, which decides whether
// absence is tolerable — directory-walked files exist by construction, so a
// read failure there is real and propagates; single-file scanners tolerate
// os.IsNotExist. A syntactically invalid file is NOT an error: it returns
// (nil, nil, nil) so callers skip it best-effort.
//
// This is the contract change from the old regex scanner: inspection is now
// AST-based, so syntactically invalid Go is skipped rather than pattern-matched
// line by line. Unresolved identifiers/imports are type errors, not syntax
// errors, so a file that references un-imported packages still parses fine.
func parseGoFile(path string) (*ast.File, *token.FileSet, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", path, err)
	}
	fset := token.NewFileSet()
	f, perr := parser.ParseFile(fset, path, src, parser.ParseComments)
	if perr != nil {
		return nil, nil, nil
	}
	return f, fset, nil
}

// exprString renders an AST type/expression back to source text (e.g. the
// StarExpr `*service.OrderService` → "*service.OrderService").
func exprString(fset *token.FileSet, e ast.Expr) string {
	var b strings.Builder
	if err := printer.Fprint(&b, fset, e); err != nil {
		return ""
	}
	return b.String()
}

// scanAggregates lists every aggregate file in domain/ and extracts event
// names from generated event declaration comments and Apply() case branches.
// Unrelated domain structs are not treated as events, and generated events
// whose Apply case is missing are still retained.
func scanAggregates(dir string, m *ProjectModel) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		if name == "event.go" || name == "errors.go" {
			continue
		}
		aggFileName := strings.TrimSuffix(name, ".go")
		file, fset, err := parseGoFile(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		if file == nil {
			continue
		}
		aggName, isAgg := declaredAggregateName(file, aggFileName)
		if !isAgg {
			continue
		}
		details := extractEvents(file, fset)
		m.Aggregate = append(m.Aggregate, Aggregate{
			Name:         aggName,
			FileName:     aggFileName,
			Events:       eventNames(details),
			EventDetails: details,
		})
	}

	sort.Slice(m.Aggregate, func(i, j int) bool {
		return m.Aggregate[i].Name < m.Aggregate[j].Name
	})
	return nil
}

// declaredAggregateName returns the string value of the generated
// `const <Pascal>AggregateName = "..."` when the file declares one whose name
// matches the file's PascalCase form; the boolean reports whether it exists.
func declaredAggregateName(file *ast.File, fileName string) (string, bool) {
	want := naming.ToPascalCase(fileName) + "AggregateName"
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, nm := range vs.Names {
				if nm.Name != want || i >= len(vs.Values) {
					continue
				}
				if v, ok := stringLit(vs.Values[i]); ok {
					return v, true
				}
			}
		}
	}
	return "", false
}

func aggregateNamesByFile(aggregates []Aggregate) map[string]string {
	names := make(map[string]string, len(aggregates))
	for _, aggregate := range aggregates {
		names[aggregate.FileName] = aggregate.Name
	}
	return names
}

func aggregateStoreName(names map[string]string, fileName string) string {
	if name := names[fileName]; name != "" {
		return name
	}
	return fileName
}

// eventNames projects EventDetail.Name for callers that only need the
// flat list (aggregate counts, esb show, the overview chips).
func eventNames(details []EventDetail) []string {
	if len(details) == 0 {
		return nil
	}
	out := make([]string, len(details))
	for i, d := range details {
		out[i] = d.Name
	}
	return out
}

// eventDocRE matches a generated event doc line, e.g. "OrderPlaced event."
// (the leading "// " is already stripped by ast.CommentGroup.Text).
var eventDocRE = regexp.MustCompile(`^([A-Z][A-Za-z0-9]+) event\.$`)

// eventNameRE bounds an Apply() case value to a PascalCase event identifier.
var eventNameRE = regexp.MustCompile(`^[A-Z][A-Za-z0-9]+$`)

// extractEvents returns events anchored to either generated event struct
// declarations (carrying a "<Name> event." doc comment, with their fields) or
// bare case branches inside Apply() (fields left empty). Declaration order in
// the file is preserved.
func extractEvents(file *ast.File, fset *token.FileSet) []EventDetail {
	byName := map[string]*EventDetail{}
	var order []string
	add := func(name string, fields []EventField) {
		if name == "" {
			return
		}
		if existing, ok := byName[name]; ok {
			if len(existing.Fields) == 0 && len(fields) > 0 {
				existing.Fields = fields
			}
			return
		}
		order = append(order, name)
		byName[name] = &EventDetail{Name: name, Fields: fields}
	}

	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			if d.Tok != token.TYPE {
				continue
			}
			for _, spec := range d.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					continue
				}
				// Only generated events carry a "<TypeName> event." doc line;
				// this is what keeps unrelated structs out of the event list.
				if !hasEventDoc(ts.Name.Name, d.Doc, ts.Doc) {
					continue
				}
				add(ts.Name.Name, eventFields(fset, st))
			}
		case *ast.FuncDecl:
			if d.Name.Name != "Apply" || d.Body == nil {
				continue
			}
			for _, name := range applyCaseNames(d.Body) {
				add(name, nil)
			}
		}
	}

	out := make([]EventDetail, 0, len(order))
	for _, name := range order {
		out = append(out, *byName[name])
	}
	return out
}

// hasEventDoc reports whether any of the comment groups contains a line
// exactly equal to "<typeName> event.".
func hasEventDoc(typeName string, groups ...*ast.CommentGroup) bool {
	for _, g := range groups {
		if g == nil {
			continue
		}
		for _, line := range strings.Split(g.Text(), "\n") {
			if m := eventDocRE.FindStringSubmatch(strings.TrimSpace(line)); m != nil && m[1] == typeName {
				return true
			}
		}
	}
	return false
}

// eventFields extracts the json-tagged exported fields of a generated event
// struct, mirroring the old regex that keyed off the presence of a json tag.
func eventFields(fset *token.FileSet, st *ast.StructType) []EventField {
	if st.Fields == nil {
		return nil
	}
	var out []EventField
	for _, f := range st.Fields.List {
		if len(f.Names) != 1 || f.Tag == nil {
			continue
		}
		fieldName := f.Names[0].Name
		if !ast.IsExported(fieldName) {
			continue
		}
		tag := reflect.StructTag(strings.Trim(f.Tag.Value, "`"))
		jsonName := strings.Split(tag.Get("json"), ",")[0]
		if jsonName == "" || jsonName == "-" {
			continue
		}
		out = append(out, EventField{
			Name:    fieldName,
			Type:    exprString(fset, f.Type),
			JSONTag: jsonName,
		})
	}
	return out
}

// applyCaseNames returns the PascalCase case values of every switch inside an
// Apply() body, in source order.
func applyCaseNames(body *ast.BlockStmt) []string {
	var out []string
	ast.Inspect(body, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok {
			return true
		}
		for _, stmt := range sw.Body.List {
			cc, ok := stmt.(*ast.CaseClause)
			if !ok {
				continue
			}
			for _, expr := range cc.List {
				if s, ok := stringLit(expr); ok && eventNameRE.MatchString(s) {
					out = append(out, s)
				}
			}
		}
		return true
	})
	return out
}

// scanHandlers walks server/handler/ and resolves the aggregate each handler
// belongs to via the `svc *service.<X>Service` field of its struct.
func scanHandlers(dir string, aggregateNames map[string]string, m *ProjectModel) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		handlerName := strings.TrimSuffix(name, ".go")
		file, _, err := parseGoFile(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		// Only files that actually declare a `<Something>Handler` struct are
		// handlers; shared helpers (e.g. response.go) are skipped.
		if file == nil || !declaresHandlerStruct(file) {
			continue
		}
		agg := ""
		if pascal, found := handlerServiceAggregate(file); found {
			agg = aggregateStoreName(aggregateNames, naming.ToSnakeCase(pascal))
		}
		m.Handler = append(m.Handler, Handler{Name: handlerName, Aggregate: agg})
	}

	sort.Slice(m.Handler, func(i, j int) bool {
		return m.Handler[i].Name < m.Handler[j].Name
	})
	return nil
}

// declaresHandlerStruct reports whether the file declares a struct type whose
// name ends in "Handler".
func declaresHandlerStruct(file *ast.File) bool {
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
			if _, ok := ts.Type.(*ast.StructType); ok && strings.HasSuffix(ts.Name.Name, "Handler") {
				return true
			}
		}
	}
	return false
}

// handlerServiceAggregate finds a struct field `svc *service.<X>Service` and
// returns X (the aggregate's PascalCase name).
func handlerServiceAggregate(file *ast.File) (string, bool) {
	var result string
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		if found {
			return false
		}
		st, ok := n.(*ast.StructType)
		if !ok || st.Fields == nil {
			return true
		}
		for _, f := range st.Fields.List {
			named := false
			for _, nm := range f.Names {
				if nm.Name == "svc" {
					named = true
				}
			}
			if !named {
				continue
			}
			star, ok := f.Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			sel, ok := star.X.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "service" {
				continue
			}
			if strings.HasSuffix(sel.Sel.Name, "Service") && len(sel.Sel.Name) > len("Service") {
				result = strings.TrimSuffix(sel.Sel.Name, "Service")
				found = true
				return false
			}
		}
		return true
	})
	return result, found
}

// scanProjections inspects each *_worker.go file in projection/ to decide
// whether it is multi-aggregate or single, and which aggregates it lists.
func scanProjections(dir string, aggregateNames map[string]string, m *ProjectModel) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, "_worker.go") {
			continue
		}
		workerName := strings.TrimSuffix(name, "_worker.go")
		file, _, err := parseGoFile(filepath.Join(dir, name))
		if err != nil {
			return err
		}

		p := Projection{Name: workerName}
		isMulti := false
		if file != nil {
			// The generated declaration derives from the projection name, whose
			// casing/underscores vary ("balanceAggregateNames",
			// "sales_reportAggregateNames"). Normalize both sides to lowercase
			// with underscores stripped so a multi-word projection still binds
			// to its own list. An explicit list binds only when its normalized
			// prefix matches this worker; otherwise fall back to a
			// `switch e.AggregateName` in the file.
			expectedPrefix := normalizePrefix(workerName)
			if names, matched := aggregateNamesVar(file, expectedPrefix); matched {
				isMulti = true
				p.Aggregates = names
			} else if names, matched := switchAggregateNames(file); matched {
				isMulti = true
				p.Aggregates = names
			}
		}
		if !isMulti {
			p.Aggregates = []string{aggregateStoreName(aggregateNames, workerName)}
		}
		p.Multi = isMulti
		sort.Strings(p.Aggregates)

		m.Projection = append(m.Projection, p)
	}

	sort.Slice(m.Projection, func(i, j int) bool {
		return m.Projection[i].Name < m.Projection[j].Name
	})
	return nil
}

// aggregateNamesVar returns the string elements of a top-level
// `var <prefix>AggregateNames = []string{...}` whose prefix (lower-cased)
// matches expectedPrefix. The bool reports whether the matching declaration
// was found.
// normalizePrefix lowercases s and strips underscores so projection worker
// names and their generated *AggregateNames variables compare equal regardless
// of snake_case vs CamelCase spelling ("sales_report" and "SalesReport" both
// normalize to "salesreport").
func normalizePrefix(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, "_", ""))
}

func aggregateNamesVar(file *ast.File, expectedPrefix string) ([]string, bool) {
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, nm := range vs.Names {
				if !strings.HasSuffix(nm.Name, "AggregateNames") || i >= len(vs.Values) {
					continue
				}
				prefix := normalizePrefix(strings.TrimSuffix(nm.Name, "AggregateNames"))
				if prefix != expectedPrefix {
					continue
				}
				if lits, ok := stringSliceLit(vs.Values[i]); ok {
					return lits, true
				}
			}
		}
	}
	return nil, false
}

// switchAggregateNames returns the case values of a `switch e.AggregateName`
// block anywhere in the file, in source order. The bool reports whether such a
// switch exists (a worker can be multi-aggregate with an empty case list).
func switchAggregateNames(file *ast.File) ([]string, bool) {
	var out []string
	seen := map[string]bool{}
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		if found {
			return false
		}
		sw, ok := n.(*ast.SwitchStmt)
		if !ok || sw.Tag == nil {
			return true
		}
		sel, ok := sw.Tag.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "AggregateName" {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); !ok || id.Name != "e" {
			return true
		}
		found = true
		for _, stmt := range sw.Body.List {
			cc, ok := stmt.(*ast.CaseClause)
			if !ok {
				continue
			}
			for _, expr := range cc.List {
				if s, ok := stringLit(expr); ok && s != "" && !seen[s] {
					seen[s] = true
					out = append(out, s)
				}
			}
		}
		return false
	})
	return out, found
}

// scanQueries lists exported query functions across every .go file in the
// projection directory (queries may be split across files, e.g. one per
// aggregate or recipe) and derives the aggregate each serves from the
// `[]XxxRow`/`*XxxRow` return type.
func scanQueries(dir string, aggregateNames map[string]string, m *ProjectModel) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		file, _, err := parseGoFile(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		if file == nil {
			continue
		}
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Recv != nil || !ast.IsExported(fd.Name.Name) {
				continue
			}
			if !isQuerySignature(fd) {
				continue
			}
			agg := ""
			if pascal, found := queryRowAggregate(fd); found {
				agg = aggregateStoreName(aggregateNames, naming.ToSnakeCase(pascal))
			}
			m.Query = append(m.Query, Query{Name: fd.Name.Name, Aggregate: agg})
		}
	}

	sort.Slice(m.Query, func(i, j int) bool {
		return m.Query[i].Name < m.Query[j].Name
	})
	return nil
}

// isQuerySignature reports whether fd's first two parameters are
// (context.Context, *gorm.DB) — the shape every generated query stub has.
func isQuerySignature(fd *ast.FuncDecl) bool {
	if fd.Type.Params == nil {
		return false
	}
	var types []ast.Expr
	for _, f := range fd.Type.Params.List {
		n := len(f.Names)
		if n == 0 {
			n = 1
		}
		for i := 0; i < n; i++ {
			types = append(types, f.Type)
		}
	}
	if len(types) < 2 {
		return false
	}
	return isSelector(types[0], "context", "Context") && isPointerSelector(types[1], "gorm", "DB")
}

// queryRowAggregate returns the PascalCase aggregate name embedded in the
// function's `[]XxxRow` or `*XxxRow` result type.
func queryRowAggregate(fd *ast.FuncDecl) (string, bool) {
	if fd.Type.Results == nil {
		return "", false
	}
	for _, r := range fd.Type.Results.List {
		if name, ok := rowTypeName(r.Type); ok {
			return name, true
		}
	}
	return "", false
}

// rowTypeName unwraps slice/pointer wrappers and returns the "<X>" of an
// "<X>Row" identifier (qualified or not).
func rowTypeName(e ast.Expr) (string, bool) {
	switch t := e.(type) {
	case *ast.ArrayType:
		return rowTypeName(t.Elt)
	case *ast.StarExpr:
		return rowTypeName(t.X)
	case *ast.Ident:
		if strings.HasSuffix(t.Name, "Row") && len(t.Name) > len("Row") {
			return strings.TrimSuffix(t.Name, "Row"), true
		}
	case *ast.SelectorExpr:
		if strings.HasSuffix(t.Sel.Name, "Row") && len(t.Sel.Name) > len("Row") {
			return strings.TrimSuffix(t.Sel.Name, "Row"), true
		}
	}
	return "", false
}

// stringLit returns the unquoted value of a string literal expression.
func stringLit(e ast.Expr) (string, bool) {
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

// stringSliceLit returns the string elements of a `[]string{...}` composite
// literal.
func stringSliceLit(e ast.Expr) ([]string, bool) {
	cl, ok := e.(*ast.CompositeLit)
	if !ok {
		return nil, false
	}
	if at, ok := cl.Type.(*ast.ArrayType); ok {
		if id, ok := at.Elt.(*ast.Ident); !ok || id.Name != "string" {
			return nil, false
		}
	}
	var out []string
	for _, elt := range cl.Elts {
		if s, ok := stringLit(elt); ok && s != "" {
			out = append(out, s)
		}
	}
	return out, true
}

func isSelector(e ast.Expr, pkg, name string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == pkg && sel.Sel.Name == name
}

func isPointerSelector(e ast.Expr, pkg, name string) bool {
	star, ok := e.(*ast.StarExpr)
	if !ok {
		return false
	}
	return isSelector(star.X, pkg, name)
}

// fieldDeclRegex matches a line like `OrderProjectionWorker *projection.OrderProjectionWorker`
var fieldDeclRegex = regexp.MustCompile(`^\s*([A-Z][A-Za-z0-9]+)\s+(\*?[\w\.]+)\s*$`)

// initLineRegex matches `varName := constructor(...)` lines inside NewApp.
var initLineRegex = regexp.MustCompile(`^\s*([a-z][A-Za-z0-9]*)\s*:=\s*([\w\.]+)New([A-Z][A-Za-z0-9]+)\(`)

// scanWire parses wire/wire.go into three lists it stitches back together
// via PascalCase names: declared App fields, init() locals, and the
// Node mapping each local to the constructor that built it. These live in
// hand-shaped regions the generator injects into after `// esb:inject:*`
// markers, so they are read by marker block rather than by declaration.
func scanWire(path string, m *ProjectModel) error {
	src, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	text := string(src)

	fieldsByPascal := map[string]WireNode{}
	for _, line := range readMarkerBlock(text, "// esb:inject:app-fields") {
		if mm := fieldDeclRegex.FindStringSubmatch(line); mm != nil {
			fieldsByPascal[mm[1]] = WireNode{Field: mm[1], Type: mm[2]}
		}
	}
	for _, f := range fieldsByPascal {
		m.Wire.Fields = append(m.Wire.Fields, f)
	}
	sort.Slice(m.Wire.Fields, func(i, j int) bool {
		return m.Wire.Fields[i].Field < m.Wire.Fields[j].Field
	})

	nodesByField := map[string]WireNode{}
	for _, line := range readMarkerBlock(text, "// esb:inject:app-init") {
		if mm := initLineRegex.FindStringSubmatch(line); mm != nil {
			// The constructor's type name (the word after `New`) is the
			// PascalCase field name on App — e.g. `orderWorker := projection.NewOrderProjectionWorker(...)`
			// maps to App.OrderProjectionWorker via [OrderProjectionWorker].
			nodesByField[mm[3]] = WireNode{
				VarName:  mm[1],
				Provider: mm[2] + "New" + mm[3] + "(...)",
			}
		}
	}
	for _, n := range nodesByField {
		m.Wire.Nodes = append(m.Wire.Nodes, n)
	}
	sort.Slice(m.Wire.Nodes, func(i, j int) bool {
		return m.Wire.Nodes[i].VarName < m.Wire.Nodes[j].VarName
	})
	return nil
}

// autoMigrateRegex matches `&XxxRow{}` entries inside the AutoMigrate block.
var autoMigrateRegex = regexp.MustCompile(`&([A-Z][A-Za-z0-9]+)Row\{\}`)

// scanDB lists the GORM models registered in projection/db.go AutoMigrate. The
// generator injects models after `// esb:inject:automigrate-models`, so only
// the entries below that marker are reported (the base cursor row above it is
// intentionally excluded).
func scanDB(path string, m *ProjectModel) error {
	src, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, line := range readMarkerBlock(string(src), "// esb:inject:automigrate-models") {
		if mm := autoMigrateRegex.FindStringSubmatch(line); mm != nil {
			m.Migrate = append(m.Migrate, mm[1]+"Row")
		}
	}
	return nil
}

// runWorkerRegex matches a single worker entry inside the workers slice.
// We anchor on the line start (only whitespace prefix) so struct-field
// references like `Handler: app.Handler,` in the App literal are ignored.
var runWorkerRegex = regexp.MustCompile(`(?m)^\s*app\.([A-Z][A-Za-z0-9]+),`)

// scanMain lists the workers that are actually started in main.go.
func scanMain(path string, m *ProjectModel) error {
	src, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, line := range readMarkerBlock(string(src), "// esb:inject:projection-workers") {
		if mm := runWorkerRegex.FindStringSubmatch(line); mm != nil {
			m.RunWorker = append(m.RunWorker, mm[1])
		}
	}
	return nil
}

// readMarkerBlock returns the lines that sit directly under the marker
// comment, up to (but not including) the next non-indented statement.
// It is tolerant of missing markers — a missing block returns nil.
func readMarkerBlock(text, marker string) []string {
	idx := strings.Index(text, marker)
	if idx == -1 {
		return nil
	}
	// Move past the marker line itself.
	after := text[idx:]
	nl := strings.Index(after, "\n")
	if nl == -1 {
		return nil
	}
	rest := after[nl+1:]

	var out []string
	for _, line := range strings.Split(rest, "\n") {
		// Block ends at the next line that is not just whitespace and
		// does not start with a tab/space — i.e. we leave the marker body.
		if line == "" {
			out = append(out, line)
			continue
		}
		if line[0] != '\t' && line[0] != ' ' {
			break
		}
		out = append(out, line)
	}
	return out
}
