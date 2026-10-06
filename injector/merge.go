package injector

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/printer"
	"go/scanner"
	"go/token"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// MergeResult is what MergeDecls did to one file.
type MergeResult struct {
	// Source is the merged file, gofmt'd. It equals the existing source
	// (formatted) when nothing was added.
	Source []byte
	// Added names every declaration copied in from the generated file,
	// as "func F", "method T.M", "type T", "var V" or "const C".
	Added []string
	// Imports lists the import paths added for those declarations.
	Imports []string
	// Differs names declarations both files have whose code is not the
	// same. They are left exactly as the project has them; a project
	// that changed one on purpose keeps its change.
	Differs []string
	// Skipped names declarations that could not be copied on their own,
	// with the reason — e.g. one constant of an iota group.
	Skipped []string
}

// MergeDecls adds to existing every top-level declaration of generated
// that existing does not have, and the imports those declarations need.
// It never changes, moves or removes anything existing already has: this
// is how `esb update-client` brings new client features into a project
// whose generated files have since been edited by hand.
//
// Declarations are matched by name — a function by its name, a method by
// its receiver type and name, a type, var or const by its name — not by
// position or text, so reordering or reformatting a file does not make a
// declaration look new. A declaration both files have but with different
// code is reported in Differs and left alone.
func MergeDecls(existing, generated []byte) (MergeResult, error) {
	return mergeDecls(existing, generated, nil)
}

// MergeSelectedDecls is MergeDecls limited to the functions and methods
// named by keys ("func F", "method T.M", as MergeResult reports them):
// every other declaration of generated is ignored, neither added nor
// compared. It brings one feature into a file without bringing back
// declarations the project removed on purpose.
func MergeSelectedDecls(existing, generated []byte, keys ...string) (MergeResult, error) {
	only := make(map[string]bool, len(keys))
	for _, k := range keys {
		only[k] = true
	}
	return mergeDecls(existing, generated, only)
}

// SameDecl reports whether a and b both declare the function or method
// key with the same code, compared as MergeDecls compares (layout and
// comments aside).
func SameDecl(a, b []byte, key string) (bool, error) {
	afset := token.NewFileSet()
	af, err := parser.ParseFile(afset, "", a, parser.ParseComments)
	if err != nil {
		return false, fmt.Errorf("parse: %w", err)
	}
	bfset := token.NewFileSet()
	bf, err := parser.ParseFile(bfset, "", b, parser.ParseComments)
	if err != nil {
		return false, fmt.Errorf("parse reference: %w", err)
	}
	an, bn := findFunc(afset, af, key), findFunc(bfset, bf, key)
	return an != nil && bn != nil && sameCode(afset, an, bfset, bn), nil
}

func findFunc(fset *token.FileSet, f *ast.File, key string) *ast.FuncDecl {
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && funcKey(fset, fd) == key {
			return fd
		}
	}
	return nil
}

// mergeDecls is MergeDecls, limited to the function and method keys in
// only when only is not nil.
func mergeDecls(existing, generated []byte, only map[string]bool) (MergeResult, error) {
	var res MergeResult
	efset := token.NewFileSet()
	ef, err := parser.ParseFile(efset, "", existing, parser.ParseComments)
	if err != nil {
		return res, fmt.Errorf("parse existing file: %w", err)
	}
	gfset := token.NewFileSet()
	gf, err := parser.ParseFile(gfset, "", generated, parser.ParseComments)
	if err != nil {
		return res, fmt.Errorf("parse generated file: %w", err)
	}
	if ef.Name.Name != gf.Name.Name {
		return res, fmt.Errorf("package %s in the project, %s in the template", ef.Name.Name, gf.Name.Name)
	}

	have := map[string]ast.Node{}
	for _, d := range ef.Decls {
		for key, n := range declKeys(efset, d) {
			have[key] = n
		}
	}

	var chunks []string
	var addedNodes []ast.Node
	for _, d := range gf.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			key := funcKey(gfset, d)
			if only != nil && !only[key] {
				continue
			}
			if old, ok := have[key]; ok {
				if !sameCode(efset, old, gfset, d) {
					res.Differs = append(res.Differs, key)
				}
				continue
			}
			chunks = append(chunks, nodeText(generated, gfset, docPos(d.Doc, d.Pos()), d.End()))
			addedNodes = append(addedNodes, d)
			res.Added = append(res.Added, key)
		case *ast.GenDecl:
			if d.Tok == token.IMPORT || only != nil {
				continue
			}
			text, added, skipped, differs := mergeGenDecl(generated, gfset, d, have, efset)
			res.Differs = append(res.Differs, differs...)
			res.Skipped = append(res.Skipped, skipped...)
			if text != "" {
				chunks = append(chunks, text)
				addedNodes = append(addedNodes, d)
				res.Added = append(res.Added, added...)
			}
		}
	}

	out := string(existing)
	if len(chunks) > 0 {
		out = strings.TrimRight(out, "\n") + "\n\n" + strings.Join(chunks, "\n\n") + "\n"
		imports, err := neededImports(ef, gf, addedNodes)
		if err != nil {
			return res, err
		}
		for _, imp := range imports {
			out, err = addImport(out, imp)
			if err != nil {
				return res, err
			}
			res.Imports = append(res.Imports, imp.path)
		}
	}
	formatted, err := format.Source([]byte(out))
	if err != nil {
		return res, fmt.Errorf("merged file does not gofmt: %w", err)
	}
	res.Source = formatted
	return res, nil
}

// mergeGenDecl handles one type/var/const declaration of the generated
// file. A declaration whose names are all missing is copied whole, doc
// comment and grouping included. When only some names of a group are
// missing, each missing one is copied as a declaration of its own —
// except a constant with no value of its own (an iota continuation),
// which means nothing outside its group.
func mergeGenDecl(src []byte, fset *token.FileSet, d *ast.GenDecl, have map[string]ast.Node, efset *token.FileSet) (text string, added, skipped, differs []string) {
	var missing []ast.Spec
	for _, s := range d.Specs {
		key := specKey(fset, d.Tok, s)
		if old, ok := have[key]; ok {
			if !sameCode(efset, old, fset, s) {
				differs = append(differs, key)
			}
			continue
		}
		missing = append(missing, s)
	}
	if len(missing) == 0 {
		return "", nil, nil, differs
	}
	if len(missing) == len(d.Specs) {
		for _, s := range missing {
			added = append(added, specKey(fset, d.Tok, s))
		}
		return nodeText(src, fset, docPos(d.Doc, d.Pos()), d.End()), added, nil, differs
	}
	var parts []string
	for _, s := range missing {
		key := specKey(fset, d.Tok, s)
		if vs, ok := s.(*ast.ValueSpec); ok && d.Tok == token.CONST && len(vs.Values) == 0 {
			skipped = append(skipped, key+": part of an iota group; add it by hand")
			continue
		}
		var doc *ast.CommentGroup
		switch s := s.(type) {
		case *ast.ValueSpec:
			doc = s.Doc
		case *ast.TypeSpec:
			doc = s.Doc
		}
		body := nodeText(src, fset, s.Pos(), s.End())
		chunk := d.Tok.String() + " " + body
		if doc != nil {
			chunk = nodeText(src, fset, doc.Pos(), doc.End()) + "\n" + chunk
		}
		parts = append(parts, chunk)
		added = append(added, key)
	}
	return strings.Join(parts, "\n\n"), added, skipped, differs
}

// declKeys maps each name a declaration introduces to the node that
// introduces it.
func declKeys(fset *token.FileSet, d ast.Decl) map[string]ast.Node {
	out := map[string]ast.Node{}
	switch d := d.(type) {
	case *ast.FuncDecl:
		out[funcKey(fset, d)] = d
	case *ast.GenDecl:
		if d.Tok == token.IMPORT {
			return out
		}
		for _, s := range d.Specs {
			out[specKey(fset, d.Tok, s)] = s
		}
	}
	return out
}

// funcKey names a function or method. init may be declared many times
// in one file, so it is keyed by its code instead of its name.
func funcKey(fset *token.FileSet, d *ast.FuncDecl) string {
	if d.Recv == nil || len(d.Recv.List) == 0 {
		if d.Name.Name == "init" {
			return "func init " + printNode(fset, d)
		}
		return "func " + d.Name.Name
	}
	return "method " + recvTypeName(d.Recv.List[0].Type) + "." + d.Name.Name
}

func recvTypeName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return recvTypeName(t.X)
	case *ast.IndexExpr:
		return recvTypeName(t.X)
	case *ast.IndexListExpr:
		return recvTypeName(t.X)
	case *ast.Ident:
		return t.Name
	}
	return "?"
}

// specKey names a type or value spec. A var or const spec that declares
// several names at once is keyed by all of them. One that declares only
// the blank identifier (var _ I = (*T)(nil)) may appear many times, so
// it is keyed by its code.
func specKey(fset *token.FileSet, tok token.Token, s ast.Spec) string {
	switch s := s.(type) {
	case *ast.TypeSpec:
		return "type " + s.Name.Name
	case *ast.ValueSpec:
		names := make([]string, len(s.Names))
		blank := true
		for i, n := range s.Names {
			names[i] = n.Name
			blank = blank && n.Name == "_"
		}
		if blank {
			return tok.String() + " " + printNode(fset, s)
		}
		return tok.String() + " " + strings.Join(names, ", ")
	}
	return "?"
}

// sameCode compares two nodes token by token, so layout, comments and
// semicolons (written or inserted at a line end) are not a difference.
func sameCode(afset *token.FileSet, a ast.Node, bfset *token.FileSet, b ast.Node) bool {
	return slices.Equal(codeTokens(printNode(afset, a)), codeTokens(printNode(bfset, b)))
}

func codeTokens(src string) []string {
	var s scanner.Scanner
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	s.Init(file, []byte(src), nil, 0)
	var out []string
	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			return out
		}
		if tok == token.SEMICOLON {
			continue // a(); b() on one line is a()\nb() on two
		}
		out = append(out, tok.String()+lit)
	}
}

func printNode(fset *token.FileSet, n ast.Node) string {
	switch d := n.(type) {
	case *ast.FuncDecl:
		c := *d
		c.Doc = nil
		n = &c
	case *ast.TypeSpec:
		c := *d
		c.Doc, c.Comment = nil, nil
		n = &c
	case *ast.ValueSpec:
		c := *d
		c.Doc, c.Comment = nil, nil
		n = &c
	}
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, n); err != nil {
		return ""
	}
	return buf.String()
}

func nodeText(src []byte, fset *token.FileSet, start, end token.Pos) string {
	return string(src[fset.Position(start).Offset:fset.Position(end).Offset])
}

type importSpec struct {
	name string // explicit alias, "" for none
	path string
}

// neededImports returns the generated file's imports that the added
// declarations use and existing does not import yet. Use is read from
// selector expressions (pkg.Name), so an import nothing added refers to
// is never copied — it would not compile.
func neededImports(ef, gf *ast.File, added []ast.Node) ([]importSpec, error) {
	used := map[string]bool{}
	for _, n := range added {
		ast.Inspect(n, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok {
					used[id.Name] = true
				}
			}
			return true
		})
	}
	existing := map[string]string{} // path → local name
	for _, imp := range ef.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		existing[p] = importName(imp, p)
	}
	var out []importSpec
	for _, imp := range gf.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		local := importName(imp, p)
		if local == "_" || local == "." || !used[local] {
			continue
		}
		if have, ok := existing[p]; ok {
			if have != local {
				return nil, fmt.Errorf("the project imports %q as %s, the template as %s", p, have, local)
			}
			continue
		}
		spec := importSpec{path: p}
		if imp.Name != nil {
			spec.name = imp.Name.Name
		}
		out = append(out, spec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out, nil
}

// importName is the name a file refers to an import by: its alias, or
// the last element of its path (ignoring a /vN major version suffix).
func importName(imp *ast.ImportSpec, path string) string {
	if imp.Name != nil {
		return imp.Name.Name
	}
	parts := strings.Split(path, "/")
	last := parts[len(parts)-1]
	if len(parts) > 1 && len(last) > 1 && last[0] == 'v' {
		if _, err := strconv.Atoi(last[1:]); err == nil {
			last = parts[len(parts)-2]
		}
	}
	return strings.ReplaceAll(last, "-", "_")
}

// addImport adds one import to src: into the first parenthesised import
// block, else as a new import declaration after the last one, else right
// after the package clause.
func addImport(src string, imp importSpec) (string, error) {
	line := strconv.Quote(imp.path)
	if imp.name != "" {
		line = imp.name + " " + line
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.ImportsOnly)
	if err != nil {
		return "", fmt.Errorf("parse imports: %w", err)
	}
	var last *ast.GenDecl
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.IMPORT {
			continue
		}
		if gd.Lparen.IsValid() {
			at := fset.Position(gd.Rparen).Offset
			return src[:at] + "\t" + line + "\n" + src[at:], nil
		}
		last = gd
	}
	if last != nil {
		at := fset.Position(last.End()).Offset
		return src[:at] + "\nimport " + line + src[at:], nil
	}
	at := fset.Position(f.Name.End()).Offset
	return src[:at] + "\n\nimport " + line + src[at:], nil
}
