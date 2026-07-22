// Package inspector reads the structure of an ESB-generated project
// without writing anything. It walks the file tree produced by `esb init`
// and surfaces an in-memory ProjectModel that the printer turns into a
// single-screen summary.
//
// The package intentionally scans the stable names, declarations, and marker
// blocks emitted by the generator, so no generated manifest or extra parser
// dependency is required.
package inspector

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/ariefsam/esb/naming"
)

// ProjectModel is the in-memory picture of one ESB project.
type ProjectModel struct {
	ModuleName  string
	PackageName string
	Aggregate   []Aggregate // every aggregate discovered in domain/, sorted by file name
	Projection  []Projection
	Handler     []Handler
	Query       []Query
	Wire        WireGraph
	Migrate     []string // GORM models in projection/db.go AutoMigrate
	RunWorker   []string // workers in main.go
}

// Aggregate is one file in domain/ (excluding event.go / errors.go).
type Aggregate struct {
	Name   string // snake_case file name (without .go)
	Events []string
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
	if err := scanHandlers(filepath.Join(rootDir, "server", "handler"), &m); err != nil {
		return m, err
	}
	if err := scanProjections(filepath.Join(rootDir, "projection"), &m); err != nil {
		return m, err
	}
	if err := scanQueries(filepath.Join(rootDir, "projection", "query.go"), &m); err != nil {
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
	return "", fmt.Errorf("module directive not found in %s", path)
}

// scanAggregates lists every aggregate file in domain/ and extracts event
// names. Event names are the union of:
//   - struct types declared at the top level (not nested),
//   - `case "Name":` branches inside the Apply() switch.
//
// The two sources are cross-validated — a struct with no Apply case, or
// vice-versa, still appears so the summary reports what the user wrote.
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
		aggName := strings.TrimSuffix(name, ".go")
		path := filepath.Join(dir, name)
		if !declaresAggregate(path, aggName) {
			continue
		}
		agg := Aggregate{
			Name:   aggName,
			Events: extractEvents(path),
		}
		m.Aggregate = append(m.Aggregate, agg)
	}

	sort.Slice(m.Aggregate, func(i, j int) bool {
		return m.Aggregate[i].Name < m.Aggregate[j].Name
	})
	return nil
}

// aggregateDeclRegex matches generated aggregate constants such as
// `const OrderAggregateName = "order"`.
var aggregateDeclRegex = regexp.MustCompile(`(?m)^\s*const\s+([A-Z][A-Za-z0-9]*)AggregateName\s*=`)

func declaresAggregate(path, name string) bool {
	src, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	match := aggregateDeclRegex.FindSubmatch(src)
	return match != nil && string(match[1]) == naming.ToPascalCase(name)
}

// structEventRegex matches top-level type declarations of the form
// "type Foo struct" — these are the event structs the generator appends.
var structEventRegex = regexp.MustCompile(`^type\s+([A-Z][A-Za-z0-9]+)\s+struct\b`)

// applyCaseRegex matches `case "Foo":` inside Apply().
var applyCaseRegex = regexp.MustCompile(`case\s+"([A-Z][A-Za-z0-9]+)"`)

// extractEvents returns the union of event struct names and Apply case
// names found in the file. The order is preserved (structs first, then
// any extras from case branches).
//
// The aggregate's own root struct (e.g. `type Order struct`) is excluded
// since it is not an event — only the generated event structs count.
func extractEvents(path string) []string {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	// File name -> aggregate struct name (the root type to skip).
	aggPascal := naming.ToPascalCase(strings.TrimSuffix(filepath.Base(path), ".go"))

	seen := map[string]bool{}
	var out []string

	inFn := false
	for _, line := range strings.Split(string(src), "\n") {
		trimmed := strings.TrimSpace(line)

		// Top-level type declarations.
		if !inFn {
			if m := structEventRegex.FindStringSubmatch(trimmed); m != nil {
				if m[1] == aggPascal {
					// The aggregate root, not an event.
					continue
				}
				if !seen[m[1]] {
					seen[m[1]] = true
					out = append(out, m[1])
				}
				continue
			}
		}

		// Track entry into Apply() so we ignore switch on other fields.
		if strings.HasPrefix(trimmed, "func ") && strings.Contains(trimmed, "Apply(") {
			inFn = true
			continue
		}
		// Leave Apply() at the next top-level func or closing brace at col 0.
		if inFn && (strings.HasPrefix(trimmed, "func ") || strings.HasPrefix(line, "}")) {
			inFn = false
		}
		if m := applyCaseRegex.FindStringSubmatch(trimmed); m != nil {
			if !seen[m[1]] {
				seen[m[1]] = true
				out = append(out, m[1])
			}
		}
	}
	return out
}

// handlerServiceRegex matches `svc *service.PascalService` on a struct field.
var handlerServiceRegex = regexp.MustCompile(`svc\s+\*service\.([A-Z][A-Za-z0-9]+)Service`)

// scanHandlers walks server/handler/ and resolves the aggregate each
// handler belongs to via the service field of its struct.
func scanHandlers(dir string, m *ProjectModel) error {
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
		path := filepath.Join(dir, name)
		src, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		agg := ""
		if mm := handlerServiceRegex.FindStringSubmatch(string(src)); mm != nil {
			agg = naming.ToSnakeCase(mm[1])
		}
		m.Handler = append(m.Handler, Handler{
			Name:      handlerName,
			Aggregate: agg,
		})
	}

	sort.Slice(m.Handler, func(i, j int) bool {
		return m.Handler[i].Name < m.Handler[j].Name
	})
	return nil
}

// aggregateNamesVarRegex matches `var <name>AggregateNames = []string{`.
var aggregateNamesVarRegex = regexp.MustCompile(`var\s+(\w+)AggregateNames\s*=\s*\[\]string\{`)

// switchAggNameRegex matches `switch e.AggregateName {` inside applyEvent.
var switchAggNameRegex = regexp.MustCompile(`switch\s+e\.AggregateName\b`)

// switchEventNameRegex matches `switch e.EventName {` inside applyEvent.
var switchEventNameRegex = regexp.MustCompile(`switch\s+e\.EventName\b`)

// aggregateLiteralRegex matches a string literal element in the
// "<name>AggregateNames" slice, e.g.   "order",
var aggregateLiteralRegex = regexp.MustCompile(`"([a-z][a-z0-9_-]*)"`)

// scanProjections inspects each *_worker.go file in projection/ to decide
// whether it is multi-aggregate or single, and which aggregates it lists.
func scanProjections(dir string, m *ProjectModel) error {
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
		if !strings.HasSuffix(name, "_worker.go") {
			continue
		}
		workerName := strings.TrimSuffix(name, "_worker.go")
		path := filepath.Join(dir, name)
		src, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		text := string(src)

		p := Projection{Name: workerName}

		// Multi-aggregate is detected by either an explicit aggregate list
		// (var <name>AggregateNames) or a switch on e.AggregateName.
		isMulti := false
		if mm := aggregateNamesVarRegex.FindStringSubmatch(text); mm != nil {
			isMulti = true
			// Extract the literal aggregate names from the slice, but
			// stop at the closing brace so we don't pick up string
			// literals from log messages, gorm tags, etc.
			sliceStart := strings.Index(text, "[]string{")
			if sliceStart != -1 {
				rest := text[sliceStart:]
				if end := strings.Index(rest, "}"); end != -1 {
					rest = rest[:end]
				}
				for _, lit := range aggregateLiteralRegex.FindAllString(rest, -1) {
					v := strings.Trim(lit, `"`)
					if v != "" {
						p.Aggregates = append(p.Aggregates, v)
					}
				}
			}
		}
		if switchAggNameRegex.MatchString(text) {
			isMulti = true
		}
		p.Multi = isMulti

		// Single-aggregate projection listens to the worker name itself.
		if !isMulti {
			p.Aggregates = []string{workerName}
		}
		sort.Strings(p.Aggregates)

		m.Projection = append(m.Projection, p)
	}

	sort.Slice(m.Projection, func(i, j int) bool {
		return m.Projection[i].Name < m.Projection[j].Name
	})
	return nil
}

// queryFuncRegex matches the query stubs the generator injects.
var queryFuncRegex = regexp.MustCompile(`^func\s+([A-Z][A-Za-z0-9]+)\s*\(\s*ctx\s+context\.Context\s*,\s*db\s+\*gorm\.DB`)

// rowReturnRegex matches `[]XxxRow` or `*XxxRow` in a query return type.
var rowReturnRegex = regexp.MustCompile(`\((?:\*|\[\])(\w+)Row\b`)

// scanQueries lists query functions in projection/query.go and derives
// the aggregate they serve from the row type they return.
func scanQueries(path string, m *ProjectModel) error {
	src, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, line := range strings.Split(string(src), "\n") {
		if mm := queryFuncRegex.FindStringSubmatch(strings.TrimSpace(line)); mm != nil {
			agg := ""
			if rr := rowReturnRegex.FindStringSubmatch(line); rr != nil {
				agg = naming.ToSnakeCase(rr[1])
			}
			m.Query = append(m.Query, Query{Name: mm[1], Aggregate: agg})
		}
	}
	return nil
}

// fieldDeclRegex matches a line like `OrderProjectionWorker *projection.OrderProjectionWorker`
var fieldDeclRegex = regexp.MustCompile(`^\s*([A-Z][A-Za-z0-9]+)\s+\*?([\w\.]+)\s*$`)

// initLineRegex matches `varName := constructor(...)` lines inside NewApp.
var initLineRegex = regexp.MustCompile(`^\s*([a-z][A-Za-z0-9]*)\s*:=\s*([\w\.]+)New([A-Z][A-Za-z0-9]+)\(`)

// scanWire parses wire/wire.go into three lists it stitches back together
// via PascalCase names: declared App fields, init() locals, and the
// Node mapping each local to the constructor that built it.
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

// scanDB lists the GORM models registered in projection/db.go AutoMigrate.
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
