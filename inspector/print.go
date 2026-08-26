package inspector

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/ariefsam/esb/naming"
)

// Print writes a single-screen summary of m to w. When focus is non-empty,
// only the parts of the project that touch that aggregate name are shown;
// unrelated aggregate names remain on one compact context line.
func Print(w io.Writer, m ProjectModel, focus string) error {
	if focus != "" {
		resolved, ok := resolveAggregateFocus(m.Aggregate, focus)
		if !ok {
			return fmt.Errorf("aggregate %q tidak ditemukan", focus)
		}
		focus = resolved
	}

	var buf strings.Builder
	printHeader(&buf, m, focus)
	printAggregates(&buf, m, focus)
	printProjections(&buf, m, focus)
	printHandlers(&buf, m, focus)
	printQueries(&buf, m, focus)
	printFlow(&buf, m, focus)
	printStats(&buf, m, focus)
	if focus == "" {
		printStorage(&buf, m)
	}
	printWire(&buf, m, focus)
	if _, err := io.WriteString(w, buf.String()); err != nil {
		return fmt.Errorf("write summary: %w", err)
	}
	return nil
}

func printHeader(w io.Writer, m ProjectModel, focus string) {
	fmt.Fprintln(w, "esb show — domain at a glance")
	if focus == "" {
		fmt.Fprintln(w, strings.Repeat("=", 78))
	}
	fmt.Fprintf(w, "module:  %s\n", fallback(m.ModuleName, "(tidak terdeteksi)"))
	fmt.Fprintf(w, "package: %s\n", fallback(m.PackageName, "(tidak terdeteksi)"))
	if focus != "" {
		fmt.Fprintf(w, "focus:   %s\n", focus)
	}
	fmt.Fprintln(w)
}

func printAggregates(w io.Writer, m ProjectModel, focus string) {
	if focus != "" {
		var selected Aggregate
		var other []string
		for _, a := range m.Aggregate {
			if a.Name == focus {
				selected = a
			} else {
				other = append(other, a.Name)
			}
		}
		fmt.Fprintf(w, "Aggregate: >> %s", selected.Name)
		if len(selected.Events) == 0 {
			fmt.Fprintln(w, "  (0 events)")
		} else {
			fmt.Fprintf(w, "  (%d events: %s)\n", len(selected.Events), strings.Join(selected.Events, ", "))
		}
		if len(other) > 0 {
			fmt.Fprintf(w, "Others:    %s\n", strings.Join(other, ", "))
		}
		fmt.Fprintln(w)
		return
	}

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
		if focus == "" {
			fmt.Fprintln(w, strings.Repeat("-", 78))
		}
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
	var matching []Projection
	for _, p := range m.Projection {
		if aggregateTouches(p.Aggregates, focus) {
			matching = append(matching, p)
		}
	}
	render("menyentuh "+focus, matching)
}

func printHandlers(w io.Writer, m ProjectModel, focus string) {
	if focus == "" {
		printHandlerList(w, "Handlers — semua", m.Handler)
		return
	}
	var matching []Handler
	for _, h := range m.Handler {
		if h.Aggregate == focus {
			matching = append(matching, h)
		}
	}
	printHandlerList(w, "Handlers — "+focus, matching)
}

func printHandlerList(w io.Writer, title string, list []Handler) {
	fmt.Fprintf(w, "%s\n", title)
	if !strings.Contains(title, " — ") || strings.HasSuffix(title, " — semua") {
		fmt.Fprintln(w, strings.Repeat("-", 78))
	}
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
	var matching []Query
	for _, q := range m.Query {
		if q.Aggregate == focus {
			matching = append(matching, q)
		}
	}
	if len(matching) > 0 {
		printQueryList(w, "Queries — "+focus, matching)
	}
}

func printQueryList(w io.Writer, title string, list []Query) {
	fmt.Fprintf(w, "%s\n", title)
	if !strings.Contains(title, " — ") || strings.HasSuffix(title, " — semua") {
		fmt.Fprintln(w, strings.Repeat("-", 78))
	}
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
	fmt.Fprintln(w, "  Event store: EventRepository -> repository.EventStoreAdapter -> eventstore.Client")
	fmt.Fprintf(w, "  Storage mode: %s\n", m.Storage.String())
	fmt.Fprintln(w)
}

// printFlow renders one line per event: which command produces it and which
// worker consumes it. It is the CLI half of the /flow page — the same
// BuildEventFlows data, so the two views cannot drift apart.
//
// Like the storage and stats sections it is skipped in focused mode: the
// focused summary is contractually a single screen (TestPrint_LineCount), and
// a per-event block does not fit inside that budget. `esb show` without an
// argument, or the /flow page, is where the per-event detail lives.
func printFlow(w io.Writer, m ProjectModel, focus string) {
	if focus != "" {
		return
	}
	flows := BuildEventFlows(m)

	fmt.Fprintln(w, "Flow — command → event → projection")
	fmt.Fprintln(w, strings.Repeat("-", 78))
	if len(flows) == 0 {
		fmt.Fprintln(w, "  (belum ada event)")
		fmt.Fprintln(w)
		return
	}

	current := ""
	for _, f := range flows {
		if f.Aggregate != current {
			current = f.Aggregate
			fmt.Fprintf(w, "  %s\n", current)
		}
		fmt.Fprintf(w, "    %-28s %-24s %s\n",
			f.Event,
			"emit: "+fallback(strings.Join(f.Producers, ", "), "-"),
			"handled: "+fallback(strings.Join(f.Consumers, ", "), "-"))
		if f.Warn != "" {
			fmt.Fprintf(w, "    %-28s ⚠ %s\n", "", f.Warn)
		}
	}
	fmt.Fprintln(w)
}

// printStats renders the derived counters plus a one-line gap tally. The gap
// details live in the UI; here we only say how many there are so `esb show`
// stays a single screen.
func printStats(w io.Writer, m ProjectModel, focus string) {
	if focus != "" {
		return
	}
	s := BuildStats(m)

	fmt.Fprintln(w, "Stats")
	fmt.Fprintln(w, strings.Repeat("-", 78))
	fmt.Fprintf(w, "  aggregates %d   events %d   commands %d   handler methods %d\n",
		s.Aggregates, s.Events, s.Commands, s.HandlerMethods)
	fmt.Fprintf(w, "  projections %d (%d multi)   queries %d   avg field/event %.1f\n",
		s.Projections, s.MultiProjections, s.Queries, s.AvgFieldsPerEvent)
	fmt.Fprintf(w, "  event tanpa producer %d   event tanpa consumer %d   command dinamis %d\n",
		s.UnproducedEvents, s.UnconsumedEvents, s.DynamicCommands)

	warn := 0
	for _, g := range s.Gaps {
		if g.Severity == "warn" {
			warn++
		}
	}
	if len(s.Gaps) == 0 {
		fmt.Fprintln(w, "  gaps: tidak ada")
	} else {
		fmt.Fprintf(w, "  gaps: %d (%d warn, %d info) — detail di 'esb ui' halaman /flow\n",
			len(s.Gaps), warn, len(s.Gaps)-warn)
	}
	fmt.Fprintln(w)
}

func printWire(w io.Writer, m ProjectModel, focus string) {
	fmt.Fprintln(w, "Wire Graph")
	if focus == "" {
		fmt.Fprintln(w, strings.Repeat("-", 78))
	}

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
		provider := node.Provider
		if provider == "" {
			provider = "(no init)"
		}
		if focus == "" {
			fmt.Fprintf(w, "  +-- %s  %s\n", f.Field, f.Type)
			fmt.Fprintf(w, "  |     %s\n", provider)
		} else {
			fmt.Fprintf(w, "  +-- %s  %s  <- %s\n", f.Field, f.Type, provider)
		}
	}
	if focus != "" {
		for _, n := range m.Wire.Nodes {
			if isFocusedServiceNode(n, focus) {
				fmt.Fprintf(w, "  +-- %s\n", n.Provider)
			}
		}
	}
	fmt.Fprintln(w)

	// Note if any projection worker in the App graph is not run in main.go.
	if focus == "" {
		var dead []string
		run := map[string]bool{}
		for _, r := range m.RunWorker {
			run[r] = true
		}
		for _, f := range m.Wire.Fields {
			if !run[f.Field] && strings.HasSuffix(f.Field, "ProjectionWorker") {
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

func isFocusedServiceNode(node WireNode, focus string) bool {
	service := strings.TrimSuffix(node.VarName, "Svc")
	return service != node.VarName && aggregateNameMatches(service, focus)
}

func aggregateNameMatches(identifier, aggregateName string) bool {
	normalized := strings.ReplaceAll(aggregateName, "-", "_")
	return naming.ToSnakeCase(identifier) == normalized
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
			if handlers[naming.ToSnakeCase(base)] {
				out = append(out, f.Field)
				seen[f.Field] = true
				continue
			}
		}
		if strings.HasSuffix(f.Field, "ProjectionWorker") {
			base := strings.TrimSuffix(f.Field, "ProjectionWorker")
			if workers[naming.ToSnakeCase(base)] {
				out = append(out, f.Field)
				seen[f.Field] = true
			}
		}
	}
	return out
}

// aggregateTouches reports whether the given aggregate list contains
// target. target is the aggregate-store name resolved from the user's input.
func aggregateTouches(list []string, target string) bool {
	for _, a := range list {
		if a == target {
			return true
		}
	}
	return false
}

func resolveAggregateFocus(aggregates []Aggregate, target string) (string, bool) {
	for _, aggregate := range aggregates {
		if aggregate.Name == target || aggregate.FileName == target {
			return aggregate.Name, true
		}
	}
	return "", false
}

func fallback(s, alt string) string {
	if s == "" {
		return alt
	}
	return s
}
