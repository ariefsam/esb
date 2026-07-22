package inspector

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// Print writes a single-screen summary of m to w. When focus is non-empty,
// only the parts of the project that touch that aggregate name are shown
// in detail; the rest appears as a compact header line so the reader does
// not lose context.
func Print(w io.Writer, m ProjectModel, focus string) error {
	printHeader(w, m, focus != "")
	printAggregates(w, m, focus)
	printProjections(w, m, focus)
	printHandlers(w, m, focus)
	printQueries(w, m, focus)
	printStorage(w, m)
	printWire(w, m, focus)
	return nil
}

func printHeader(w io.Writer, m ProjectModel, focused bool) {
	fmt.Fprintln(w, "esb show — domain at a glance")
	fmt.Fprintln(w, strings.Repeat("=", 78))
	fmt.Fprintf(w, "module:  %s\n", fallback(m.ModuleName, "(tidak terdeteksi)"))
	fmt.Fprintf(w, "package: %s\n", fallback(m.PackageName, "(tidak terdeteksi)"))
	if focused {
		fmt.Fprintln(w, "focus:   filtered")
	}
	fmt.Fprintln(w)
}

func printAggregates(w io.Writer, m ProjectModel, focus string) {
	fmt.Fprintln(w, "Aggregates")
	fmt.Fprintln(w, strings.Repeat("-", 78))
	if len(m.Aggregate) == 0 {
		fmt.Fprintln(w, "  (tidak ada — jalankan 'esb add aggregate <name>')")
		fmt.Fprintln(w)
		return
	}
	for _, a := range m.Aggregate {
		marker := "  "
		if focus != "" && a.Name == focus {
			marker = ">>"
		}
		if len(a.Events) == 0 {
			fmt.Fprintf(w, "%s %s  (0 events)\n", marker, a.Name)
		} else {
			fmt.Fprintf(w, "%s %s  (%d events: %s)\n", marker, a.Name, len(a.Events), strings.Join(a.Events, ", "))
		}
	}
	fmt.Fprintln(w)
}

func printProjections(w io.Writer, m ProjectModel, focus string) {
	render := func(title string, list []Projection) {
		fmt.Fprintf(w, "Projections — %s\n", title)
		fmt.Fprintln(w, strings.Repeat("-", 78))
		if len(list) == 0 {
			fmt.Fprintln(w, "  (tidak ada)")
		} else {
			for _, p := range list {
				kind := "single"
				if p.Multi {
					kind = "multi"
				}
				fmt.Fprintf(w, "  %s  [%s]  listens: %s\n", p.Name, kind, strings.Join(p.Aggregates, ", "))
			}
		}
		fmt.Fprintln(w)
	}

	if focus == "" {
		render("semua", m.Projection)
		return
	}
	var matching, rest []Projection
	for _, p := range m.Projection {
		if aggregateTouches(p.Aggregates, focus) {
			matching = append(matching, p)
		} else {
			rest = append(rest, p)
		}
	}
	if len(matching) > 0 {
		render("menyentuh "+focus, matching)
	}
	if len(rest) > 0 {
		render("lainnya", rest)
	}
}

func printHandlers(w io.Writer, m ProjectModel, focus string) {
	if focus == "" {
		printHandlerList(w, "Handlers — semua", m.Handler)
		return
	}
	var matching, rest []Handler
	for _, h := range m.Handler {
		if h.Aggregate == focus {
			matching = append(matching, h)
		} else {
			rest = append(rest, h)
		}
	}
	if len(matching) > 0 {
		printHandlerList(w, "Handlers — "+focus, matching)
	}
	if len(rest) > 0 {
		printHandlerList(w, "Handlers — lainnya", rest)
	}
}

func printHandlerList(w io.Writer, title string, list []Handler) {
	fmt.Fprintf(w, "%s\n", title)
	fmt.Fprintln(w, strings.Repeat("-", 78))
	if len(list) == 0 {
		fmt.Fprintln(w, "  (tidak ada)")
	} else {
		for _, h := range list {
			agg := h.Aggregate
			if agg == "" {
				agg = "(tidak terdeteksi)"
			}
			fmt.Fprintf(w, "  %s  ->  aggregate: %s\n", h.Name, agg)
		}
	}
	fmt.Fprintln(w)
}

func printQueries(w io.Writer, m ProjectModel, focus string) {
	if focus == "" {
		printQueryList(w, "Queries", m.Query)
		return
	}
	var matching, rest []Query
	for _, q := range m.Query {
		if q.Aggregate == focus {
			matching = append(matching, q)
		} else {
			rest = append(rest, q)
		}
	}
	if len(matching) > 0 {
		printQueryList(w, "Queries — "+focus, matching)
	}
	if len(rest) > 0 {
		printQueryList(w, "Queries — lainnya", rest)
	}
}

func printQueryList(w io.Writer, title string, list []Query) {
	fmt.Fprintf(w, "%s\n", title)
	fmt.Fprintln(w, strings.Repeat("-", 78))
	if len(list) == 0 {
		fmt.Fprintln(w, "  (tidak ada)")
	} else {
		for _, q := range list {
			agg := q.Aggregate
			if agg == "" {
				agg = "(tidak terdeteksi)"
			}
			fmt.Fprintf(w, "  %s  ->  row: %s\n", q.Name, agg)
		}
	}
	fmt.Fprintln(w)
}

func printStorage(w io.Writer, m ProjectModel) {
	fmt.Fprintln(w, "Storage")
	fmt.Fprintln(w, strings.Repeat("-", 78))
	if len(m.Migrate) == 0 {
		fmt.Fprintln(w, "  AutoMigrate: (tidak ada model terdaftar)")
	} else {
		fmt.Fprintf(w, "  AutoMigrate: %s\n", strings.Join(m.Migrate, ", "))
	}
	if len(m.RunWorker) == 0 {
		fmt.Fprintln(w, "  Run workers: (tidak ada — projection_workers marker kosong)")
	} else {
		fmt.Fprintf(w, "  Run workers: %s\n", strings.Join(m.RunWorker, ", "))
	}
	fmt.Fprintln(w)
}

func printWire(w io.Writer, m ProjectModel, focus string) {
	fmt.Fprintln(w, "Wire Graph")
	fmt.Fprintln(w, strings.Repeat("-", 78))

	if len(m.Wire.Fields) == 0 {
		fmt.Fprintln(w, "  (tidak ada field — app-fields marker kosong)")
		fmt.Fprintln(w)
		return
	}

	// Index nodes by their field name (the constructor type) so we can
	// stitch fields to concrete constructors in the right column.
	nodesByField := map[string]WireNode{}
	for _, n := range m.Wire.Nodes {
		// The WireNode.Provider string is "path.NewXxxType(...)"; the
		// word after "New" is the App field name.
		prov := n.Provider
		if i := strings.Index(prov, "New"); i != -1 {
			rest := prov[i+len("New"):]
			if j := strings.Index(rest, "("); j != -1 {
				nodesByField[rest[:j]] = n
			}
		}
	}

	// When focused, only show the chain(s) that touch the focused aggregate.
	chains := map[string]bool{"App": true}
	if focus != "" {
		for _, f := range focusedChains(m, focus) {
			chains[f] = true
		}
		if len(chains) == 1 {
			fmt.Fprintf(w, "  (tidak ada wire node yang menyentuh %s)\n", focus)
			fmt.Fprintln(w)
			return
		}
	}

	fmt.Fprintln(w, "  App")
	for _, f := range m.Wire.Fields {
		if focus != "" && !chains[f.Field] {
			continue
		}
		node := nodesByField[f.Field]
		fmt.Fprintf(w, "  +-- %s  %s\n", f.Field, f.Type)
		provider := node.Provider
		if provider == "" {
			provider = "(no init)"
		}
		fmt.Fprintf(w, "  |     %s\n", provider)
	}
	fmt.Fprintln(w)

	// Note if any *Field* in the App graph is not run in main.go.
	if focus == "" {
		var dead []string
		run := map[string]bool{}
		for _, r := range m.RunWorker {
			run[r] = true
		}
		for _, f := range m.Wire.Fields {
			if !run[f.Field] {
				dead = append(dead, f.Field)
			}
		}
		if len(dead) > 0 {
			sort.Strings(dead)
			fmt.Fprintf(w, "  (declared but not started: %s)\n", strings.Join(dead, ", "))
			fmt.Fprintln(w)
		}
	}
}

// focusedChains returns the PascalCase field names in the wire graph that
// touch the focused aggregate. A field is "in" when:
//   - it is a handler referencing the focused aggregate, or
//   - it is a projection worker that listens to the focused aggregate.
func focusedChains(m ProjectModel, focus string) []string {
	handlers := map[string]bool{}
	for _, h := range m.Handler {
		if h.Aggregate == focus {
			handlers[h.Name] = true
		}
	}
	workers := map[string]bool{}
	for _, p := range m.Projection {
		if aggregateTouches(p.Aggregates, focus) {
			workers[p.Name] = true
		}
	}

	var out []string
	seen := map[string]bool{}
	for _, f := range m.Wire.Fields {
		if seen[f.Field] {
			continue
		}
		if strings.HasSuffix(f.Field, "Handler") {
			base := strings.TrimSuffix(f.Field, "Handler")
			if handlers[camelToSnake(base)] {
				out = append(out, f.Field)
				seen[f.Field] = true
				continue
			}
		}
		if strings.HasSuffix(f.Field, "ProjectionWorker") {
			base := strings.TrimSuffix(f.Field, "ProjectionWorker")
			if workers[camelToSnake(base)] {
				out = append(out, f.Field)
				seen[f.Field] = true
			}
		}
	}
	return out
}

// camelToSnake converts a CamelCase identifier to snake_case (best-effort
// for matching wire field names like "PlaceOrderHandler" against
// "place_order").
func camelToSnake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			b.WriteByte('_')
		}
		if r >= 'A' && r <= 'Z' {
			b.WriteRune(r + 'a' - 'A')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// aggregateTouches reports whether the given aggregate list contains
// target. target is the snake_case form the user typed on the command line.
func aggregateTouches(list []string, target string) bool {
	for _, a := range list {
		if a == target {
			return true
		}
	}
	return false
}

func fallback(s, alt string) string {
	if s == "" {
		return alt
	}
	return s
}
