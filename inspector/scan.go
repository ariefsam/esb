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
	Aggregate   []Aggregate // every aggregate discovered in domain/, sorted by aggregate-store name
	Projection  []Projection
	Handler     []Handler
	Query       []Query
	Wire        WireGraph
	Migrate     []string // GORM models in projection/db.go AutoMigrate
	RunWorker   []string // workers in main.go
}

// Aggregate is one file in domain/ (excluding event.go / errors.go).
type Aggregate struct {
	Name     string // aggregate-store name from the generated constant (for example "bank-account")
	FileName string // snake_case file name (without .go), used to identify the root struct
	Events   []string
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
	if err := scanQueries(filepath.Join(rootDir, "projection", "query.go"), aggregateNames, &m); err != nil {
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
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return "", fmt.Errorf("module directive not found in %s", path)
}

// scanAggregates lists every aggregate file in domain/ and extracts event
// names from generated event declaration comments and Apply() case branches.
// This avoids treating unrelated domain structs as events while retaining
// generated declarations whose Apply case has not been added successfully.
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
		path := filepath.Join(dir, name)
		aggName, ok, err := declaredAggregateName(path, aggFileName)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		events, err := extractEvents(path)
		if err != nil {
			return err
		}
		agg := Aggregate{
			Name:     aggName,
			FileName: aggFileName,
			Events:   events,
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
var aggregateDeclRegex = regexp.MustCompile(`(?m)^\s*const\s+([A-Z][A-Za-z0-9]*)AggregateName\s*=\s*"([a-z][a-z0-9_-]*)"`)

func declaredAggregateName(path, fileName string) (string, bool, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return "", false, fmt.Errorf("read %s: %w", path, err)
	}
	match := aggregateDeclRegex.FindSubmatch(src)
	if match == nil || string(match[1]) != naming.ToPascalCase(fileName) {
		return "", false, nil
	}
	return string(match[2]), true, nil
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

// generatedEventCommentRegex matches comments emitted immediately before
// generated event declarations, for example "// OrderPlaced event.".
var generatedEventCommentRegex = regexp.MustCompile(`^//\s+([A-Z][A-Za-z0-9]+)\s+event\.$`)

// structEventRegex matches top-level type declarations. It is only used to
// confirm a declaration named by a generated event comment; unrelated structs
// are not events merely because they share the aggregate file.
var structEventRegex = regexp.MustCompile(`^type\s+([A-Z][A-Za-z0-9]+)\s+struct\b`)

// applyCaseRegex matches `case "Foo":` inside Apply().
var applyCaseRegex = regexp.MustCompile(`case\s+"([A-Z][A-Za-z0-9]+)"`)

// extractEvents returns event names anchored to either generated event
// declaration comments or case branches inside Apply(). Declaration order is
// preserved, followed by any Apply-only event names.
//
// Apply() detection tracks brace depth rather than relying on gofmt-style
// top-level closing braces so that non-gofmt files (or hand-edited Apply
// bodies) do not leak the inApply flag into unrelated code below.
func extractEvents(path string) ([]string, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}

	lines := strings.Split(string(src), "\n")
	pendingGeneratedEvent := ""
	inApply := false
	applyDepth := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if !inApply {
			if m := generatedEventCommentRegex.FindStringSubmatch(trimmed); m != nil {
				pendingGeneratedEvent = m[1]
				continue
			}
			if pendingGeneratedEvent != "" {
				if trimmed == "" {
					continue
				}
				if m := structEventRegex.FindStringSubmatch(trimmed); m != nil && m[1] == pendingGeneratedEvent {
					add(m[1])
				}
				pendingGeneratedEvent = ""
			}
		}

		if !inApply && strings.HasPrefix(trimmed, "func ") && strings.Contains(trimmed, "Apply(") {
			// Enter Apply on the opening brace of the function body —
			// not the signature line — so signatures that contain
			// braces (rare) do not skew the depth count.
			inApply = true
			applyDepth = 0
			if open := strings.Index(trimmed, "{"); open != -1 {
				applyDepth = 1
				if close := strings.LastIndex(trimmed, "}"); close > open {
					applyDepth = 0
				}
			}
			continue
		}

		if inApply {
			// Count net braces on this line so we leave Apply only when
			// the matching close brace brings depth back to 0.
			for _, r := range line {
				switch r {
				case '{':
					applyDepth++
				case '}':
					applyDepth--
					if applyDepth <= 0 {
						inApply = false
						applyDepth = 0
						break
					}
				}
				if !inApply {
					break
				}
			}
			if !inApply {
				continue
			}
			if m := applyCaseRegex.FindStringSubmatch(trimmed); m != nil {
				add(m[1])
			}
		}
	}
	return out, nil
}

// handlerServiceRegex matches `svc *service.PascalService` on a struct field.
var handlerServiceRegex = regexp.MustCompile(`svc\s+\*service\.([A-Z][A-Za-z0-9]+)Service`)

// scanHandlers walks server/handler/ and resolves the aggregate each
// handler belongs to via the service field of its struct.
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
		path := filepath.Join(dir, name)
		src, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		agg := ""
		if mm := handlerServiceRegex.FindStringSubmatch(string(src)); mm != nil {
			agg = aggregateStoreName(aggregateNames, naming.ToSnakeCase(mm[1]))
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
// The captured prefix (group 1) is the worker-specific identifier that
// must be matched against the current worker file name before we treat the
// declaration as belonging to this worker.
var aggregateNamesVarRegex = regexp.MustCompile(`var\s+(\w+)AggregateNames\s*=\s*\[\]string\s*\{`)

// switchAggNameRegex matches `switch e.AggregateName {` inside applyEvent.
var switchAggNameRegex = regexp.MustCompile(`switch\s+e\.AggregateName\b`)

// switchEventNameRegex matches `switch e.EventName {` inside applyEvent.
var switchEventNameRegex = regexp.MustCompile(`switch\s+e\.EventName\b`)

// aggregateLiteralRegex matches a string literal element in the
// "<name>AggregateNames" slice, e.g.   "order",
var aggregateLiteralRegex = regexp.MustCompile(`"([a-z][a-z0-9_-]*)"`)

// switchAggCaseRegex matches `case "Xxx":` branches inside a
// `switch e.AggregateName` block. The aggregate store name is treated as
// a kebab/snake case identifier, so it matches the same shape as the
// explicit-list literal above.
var switchAggCaseRegex = regexp.MustCompile(`case\s+"([a-z][a-z0-9_-]*)"`)

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
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		if !strings.HasSuffix(name, "_worker.go") {
			continue
		}
		workerName := strings.TrimSuffix(name, "_worker.go")
		// The generated declaration uses CamelCase ("BalanceAggregateNames"),
		// so compare prefixes case-insensitively. Lower-casing both sides
		// also accepts user-edited `var balanceAggregateNames` style.
		expectedPrefix := strings.ToLower(naming.ToPascalCase(workerName))
		path := filepath.Join(dir, name)
		src, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		text := string(src)

		p := Projection{Name: workerName}

		// Multi-aggregate is detected by either an explicit aggregate list
		// (var <prefix>AggregateNames) or a switch on e.AggregateName.
		isMulti := false
		// Iterate over ALL *AggregateNames declarations and use the one
		// whose prefix matches the current worker file name. An
		// unrelated declaration appearing earlier in the file (e.g. a
		// legacyAggregateNames) must not be silently attributed to
		// this worker.
		for _, match := range aggregateNamesVarRegex.FindAllStringSubmatchIndex(text, -1) {
			sub := aggregateNamesVarRegex.FindStringSubmatch(text[match[0]:match[1]])
			if len(sub) < 2 {
				continue
			}
			prefix := strings.ToLower(sub[1])
			if prefix != expectedPrefix {
				continue
			}
			isMulti = true
			// Start at the matched declaration, not the first []string
			// in the file, then stop at the matching close brace —
			// tracked by depth so a literal "}" inside a string does
			// not truncate early.
			rest := text[match[0]:]
			rest = trimToMatchingBrace(rest)
			for _, lit := range aggregateLiteralRegex.FindAllString(rest, -1) {
				v := strings.Trim(lit, `"`)
				if v != "" {
					p.Aggregates = append(p.Aggregates, v)
				}
			}
			break
		}
		if switchAggNameRegex.MatchString(text) {
			isMulti = true
			// No matching declaration was found, so derive the aggregate
			// list from the case branches inside the switch block —
			// again tracked by brace depth so unrelated code below is not
			// pulled in.
			if len(p.Aggregates) == 0 {
				p.Aggregates = extractSwitchAggregateNames(text)
			}
		}
		p.Multi = isMulti

		// Single-aggregate projection listens to the aggregate-store name
		// declared in domain/<name>.go (which may be kebab-case).
		if !isMulti {
			p.Aggregates = []string{aggregateStoreName(aggregateNames, workerName)}
		}
		sort.Strings(p.Aggregates)

		m.Projection = append(m.Projection, p)
	}

	sort.Slice(m.Projection, func(i, j int) bool {
		return m.Projection[i].Name < m.Projection[j].Name
	})
	return nil
}

// trimToMatchingBrace returns the prefix of text up to and including the
// closing brace that matches the first opening brace. If no opening brace
// is present, the input is returned unchanged. This prevents the parser
// from bailing out at the first '}' it sees inside a string literal or
// nested struct in an unrelated declaration.
func trimToMatchingBrace(text string) string {
	open := strings.Index(text, "{")
	if open == -1 {
		return text
	}
	depth := 0
	inString := false
	escape := false
	for i := open; i < len(text); i++ {
		c := text[i]
		if escape {
			escape = false
			continue
		}
		if c == '\\' {
			escape = true
			continue
		}
		if c == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch c {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[:i+1]
			}
		}
	}
	return text
}

// extractSwitchAggregateNames walks the lines of a projection worker file
// and returns the aggregate names declared in `switch e.AggregateName`
// case branches. Brace depth is tracked so a switch that ends early does
// not leak into the next function.
func extractSwitchAggregateNames(text string) []string {
	var out []string
	seen := map[string]bool{}
	lines := strings.Split(text, "\n")
	inSwitch := false
	depth := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inSwitch {
			if switchAggNameRegex.MatchString(trimmed) {
				inSwitch = true
				depth = 0
				if open := strings.Index(trimmed, "{"); open != -1 {
					depth = 1
				}
			}
			continue
		}
		for _, r := range line {
			switch r {
			case '{':
				depth++
			case '}':
				depth--
				if depth <= 0 {
					inSwitch = false
					depth = 0
					goto done
				}
			}
		}
		if m := switchAggCaseRegex.FindStringSubmatch(trimmed); m != nil {
			if !seen[m[1]] {
				seen[m[1]] = true
				out = append(out, m[1])
			}
		}
	}
done:
	return out
}

// queryFuncRegex matches the query stubs the generator injects.
var queryFuncRegex = regexp.MustCompile(`^func\s+([A-Z][A-Za-z0-9]+)\s*\(\s*ctx\s+context\.Context\s*,\s*db\s+\*gorm\.DB`)

// rowReturnRegex matches `[]XxxRow` or `*XxxRow` in a query return type.
var rowReturnRegex = regexp.MustCompile(`\((?:\*|\[\])(\w+)Row\b`)

// scanQueries lists query functions in projection/query.go and derives
// the aggregate they serve from the row type they return.
func scanQueries(path string, aggregateNames map[string]string, m *ProjectModel) error {
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
				agg = aggregateStoreName(aggregateNames, naming.ToSnakeCase(rr[1]))
			}
			m.Query = append(m.Query, Query{Name: mm[1], Aggregate: agg})
		}
	}
	return nil
}

// fieldDeclRegex matches a line like `OrderProjectionWorker *projection.OrderProjectionWorker`
var fieldDeclRegex = regexp.MustCompile(`^\s*([A-Z][A-Za-z0-9]+)\s+(\*?[\w\.]+)\s*$`)

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
