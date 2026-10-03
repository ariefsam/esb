package inspector

import (
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ariefsam/esb/naming"
)

// scanReadModel links projections and queries through the read-model tables
// they share, instead of guessing from names.
//
// A table is a struct type named <X>Row in projection/ that GORM maps: it has
// a TableName() method or is listed in AutoMigrate. A function touches a table
// when it names the row type anywhere (Model(&EnvelopeRow{}),
// NewRepository[TransactionRow], var rows []UserRow) or names the table in a
// string literal (raw SQL: FROM profiles p JOIN profile_members). It writes
// when it calls a GORM write method. Both follow references to other functions
// of the package, including function values (apply: applyToReadModel), so a
// worker's tables are everything its methods reach.
//
// It fills Projection.Tables, Query.Tables and Query.Writes, and replaces a
// query's aggregate when its tables are written by one aggregate's apply
// function (projection/<aggregate>_worker.go).
func scanReadModel(dir string, aggregateNames map[string]string, m *ProjectModel) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	type fileAST struct {
		base string
		f    *ast.File
	}
	var files []fileAST
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, _, err := parseGoFile(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		if f != nil {
			files = append(files, fileAST{base: strings.TrimSuffix(name, ".go"), f: f})
		}
	}

	// Tables: row type → table name.
	migrated := map[string]bool{}
	for _, r := range m.Migrate {
		migrated[r] = true
	}
	tableNames := map[string]string{}
	rowTypes := map[string]bool{}
	for _, fa := range files {
		for _, st := range structTypes(fa.f) {
			if strings.HasSuffix(st.name, "Row") {
				rowTypes[st.name] = true
			}
		}
		for _, d := range fa.f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "TableName" || fn.Body == nil {
				continue
			}
			_, recvType, ok := receiverOfAnyName(fn)
			if !ok {
				continue
			}
			if lit := returnedString(fn.Body); lit != "" {
				tableNames[recvType] = lit
			}
		}
	}
	tables := map[string]string{} // row type → table name, only real tables
	for row := range rowTypes {
		if t, ok := tableNames[row]; ok {
			tables[row] = t
		} else if migrated[row] {
			// GORM's default: snake_case plural of the type name.
			tables[row] = naming.ToSnakeCase(row) + "s"
		}
	}
	tablePattern := map[string]*regexp.Regexp{}
	for _, t := range tables {
		tablePattern[t] = regexp.MustCompile(`\b` + regexp.QuoteMeta(t) + `\b`)
	}

	// Functions: key is "Name" or "Type.Method".
	type fnInfo struct {
		file    string
		touches map[string]bool
		writes  bool
		refs    []string
	}
	funcs := map[string]*fnInfo{}
	topLevel := map[string]bool{}
	for _, fa := range files {
		for _, d := range fa.f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil {
				topLevel[fn.Name.Name] = true
			}
		}
	}
	for _, fa := range files {
		for _, d := range fa.f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			key := fn.Name.Name
			recvName, recvType, isMethod := receiverOfAnyName(fn)
			if fn.Recv != nil {
				if !isMethod {
					continue
				}
				key = recvType + "." + fn.Name.Name
			}
			info := &fnInfo{file: fa.base, touches: map[string]bool{}}
			ast.Inspect(fn, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.Ident:
					if t, ok := tables[x.Name]; ok {
						info.touches[t] = true
					}
					if topLevel[x.Name] && x.Name != fn.Name.Name {
						info.refs = append(info.refs, x.Name)
					}
				case *ast.BasicLit:
					if x.Kind == token.STRING {
						for t, re := range tablePattern {
							if re.MatchString(x.Value) {
								info.touches[t] = true
							}
						}
					}
				case *ast.SelectorExpr:
					if id, ok := x.X.(*ast.Ident); ok && isMethod && id.Name == recvName {
						info.refs = append(info.refs, recvType+"."+x.Sel.Name)
					}
				case *ast.CallExpr:
					if sel, ok := x.Fun.(*ast.SelectorExpr); ok && gormWrites[sel.Sel.Name] {
						info.writes = true
					}
				}
				return true
			})
			funcs[key] = info
		}
	}

	// closure walks key and everything it references.
	closure := func(roots []string, visit func(*fnInfo)) {
		seen := map[string]bool{}
		stack := append([]string{}, roots...)
		for len(stack) > 0 {
			k := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if seen[k] {
				continue
			}
			seen[k] = true
			info, ok := funcs[k]
			if !ok {
				continue
			}
			visit(info)
			stack = append(stack, info.refs...)
		}
	}

	// Which aggregate's apply function writes each table.
	tableAggs := map[string]map[string]bool{}
	for _, info := range funcs {
		if !info.writes || !strings.HasSuffix(info.file, "_worker") {
			continue
		}
		agg := aggregateNames[strings.TrimSuffix(info.file, "_worker")]
		if agg == "" {
			continue
		}
		for t := range info.touches {
			if tableAggs[t] == nil {
				tableAggs[t] = map[string]bool{}
			}
			tableAggs[t][agg] = true
		}
	}

	for i := range m.Projection {
		p := &m.Projection[i]
		worker := naming.ToPascalCase(p.Name) + "ProjectionWorker"
		var roots []string
		for k := range funcs {
			if strings.HasPrefix(k, worker+".") || k == "New"+worker {
				roots = append(roots, k)
			}
		}
		written := map[string]bool{}
		closure(roots, func(info *fnInfo) {
			if info.writes {
				for t := range info.touches {
					written[t] = true
				}
			}
		})
		p.Tables = sortedKeys(written)
	}

	realAggregate := map[string]bool{}
	for _, a := range aggregateNames {
		realAggregate[a] = true
	}
	for i := range m.Query {
		q := &m.Query[i]
		touched := map[string]bool{}
		writes := false
		closure([]string{q.Name}, func(info *fnInfo) {
			for t := range info.touches {
				touched[t] = true
			}
			writes = writes || info.writes
		})
		q.Tables = sortedKeys(touched)
		q.Writes = writes
		aggs := map[string]bool{}
		for t := range touched {
			for a := range tableAggs[t] {
				aggs[a] = true
			}
		}
		if len(aggs) == 1 {
			q.Aggregate = sortedKeys(aggs)[0]
		} else if !realAggregate[q.Aggregate] {
			q.Aggregate = ""
		}
	}
	return nil
}

// gormWrites are the *gorm.DB methods that change data.
var gormWrites = map[string]bool{
	"Create": true, "CreateInBatches": true, "Save": true, "Update": true,
	"Updates": true, "UpdateColumn": true, "UpdateColumns": true,
	"Delete": true, "Exec": true, "FirstOrCreate": true,
}

// receiverOfAnyName is receiverOf without requiring a named receiver, so
// `func (EnvelopeRow) TableName()` resolves too.
func receiverOfAnyName(fn *ast.FuncDecl) (varName, typeName string, ok bool) {
	if fn.Recv == nil || len(fn.Recv.List) != 1 {
		return "", "", false
	}
	field := fn.Recv.List[0]
	expr := field.Type
	if star, isStar := expr.(*ast.StarExpr); isStar {
		expr = star.X
	}
	ident, isIdent := expr.(*ast.Ident)
	if !isIdent {
		return "", "", false
	}
	if len(field.Names) == 1 {
		varName = field.Names[0].Name
	}
	return varName, ident.Name, true
}

// returnedString is the string literal of the first `return "…"` in body.
func returnedString(body *ast.BlockStmt) string {
	for _, stmt := range body.List {
		ret, ok := stmt.(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			continue
		}
		if lit, ok := ret.Results[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if s, err := strconv.Unquote(lit.Value); err == nil {
				return s
			}
		}
	}
	return ""
}

// projectionCalls lists F for every <pkg>.F(…) call in body where pkg is the
// file's local name for the project's projection package.
func projectionCalls(body *ast.BlockStmt, pkg string) []string {
	if pkg == "" || body == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && id.Name == pkg && !seen[sel.Sel.Name] {
			seen[sel.Sel.Name] = true
			out = append(out, sel.Sel.Name)
		}
		return true
	})
	sort.Strings(out)
	return out
}

// projectionImportName is the name file uses for an import whose path ends in
// "/projection" ("" when it does not import it).
func projectionImportName(file *ast.File) string {
	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || !strings.HasSuffix(path, "/projection") {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return "projection"
	}
	return ""
}
