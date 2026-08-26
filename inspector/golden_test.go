package inspector_test

import (
	"slices"
	"testing"

	"github.com/ariefsam/esb/generator"
	"github.com/ariefsam/esb/inspector"
)

// TestGolden_InspectorParsesGeneratorOutput is the coupling contract the
// assessment (#11) asked for. The inspector parses generated code with
// regexes tied to the exact wording and layout the templates emit (event
// comments, one-line query signatures, gofmt spacing). This test generates a
// real project through the generator and asserts the inspector recovers the
// aggregate, its event and fields, the handler, the query, and the projection
// — so any template reword that silently breaks a scanner regex fails here
// instead of shipping as a silent-empty model.
func TestGolden_InspectorParsesGeneratorOutput(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	if err := generator.InitProject("example.com/shop", dir); err != nil {
		t.Fatal(err)
	}
	if err := generator.AddAggregate("order"); err != nil {
		t.Fatal(err)
	}
	fields, err := generator.ParseFields([]string{"amount:int64", "currency:string"})
	if err != nil {
		t.Fatal(err)
	}
	if err := generator.AddEvent("order", "OrderPlaced", fields); err != nil {
		t.Fatal(err)
	}
	if err := generator.AddHandler("place_order", "order"); err != nil {
		t.Fatal(err)
	}
	if err := generator.AddQuery("orders_by_buyer", "order"); err != nil {
		t.Fatal(err)
	}
	if err := generator.AddProjection("sales_report", []string{"order", "product"}); err != nil {
		t.Fatal(err)
	}

	m, err := inspector.Scan(dir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	// --- aggregate ---
	agg := findAggregate(m, "order")
	if agg == nil {
		t.Fatalf("inspector did not find the 'order' aggregate; got %+v", aggNames(m))
	}
	if !slices.Contains(agg.Events, "OrderPlaced") {
		t.Errorf("aggregate events = %v, want to contain OrderPlaced", agg.Events)
	}

	// --- event fields (regex is tied to the generated struct + json tags) ---
	ed := findEvent(agg, "OrderPlaced")
	if ed == nil {
		t.Fatalf("event OrderPlaced has no detail; details = %+v", agg.EventDetails)
	}
	wantFields := map[string]string{"Amount": "int64", "Currency": "string"}
	got := map[string]string{}
	for _, f := range ed.Fields {
		got[f.Name] = f.Type
	}
	for name, typ := range wantFields {
		if got[name] != typ {
			t.Errorf("event field %s = %q, want %q (all: %+v)", name, got[name], typ, ed.Fields)
		}
	}

	// --- handler ---
	if !hasHandler(m, "place_order") {
		t.Errorf("inspector did not find handler place_order; handlers = %+v", m.Handler)
	}

	// --- query (regex requires a one-line generated signature) ---
	if !hasQuery(m, "OrdersByBuyer") {
		t.Errorf("inspector did not find query OrdersByBuyer; queries = %+v", m.Query)
	}

	// --- multi-aggregate projection ---
	if p := findProjection(m, "sales_report"); p == nil {
		t.Errorf("inspector did not find projection sales_report; projections = %+v", m.Projection)
	} else {
		if !p.Multi {
			t.Errorf("projection sales_report Multi = false, want true")
		}
		// The generated var is `sales_reportAggregateNames` — a multi-word
		// (underscored) name that must still bind to its own aggregate list.
		if !slices.Equal(p.Aggregates, []string{"order", "product"}) {
			t.Errorf("sales_report aggregates = %v, want [order product]", p.Aggregates)
		}
	}
}

func findAggregate(m inspector.ProjectModel, fileName string) *inspector.Aggregate {
	for i := range m.Aggregate {
		if m.Aggregate[i].FileName == fileName {
			return &m.Aggregate[i]
		}
	}
	return nil
}

func findEvent(a *inspector.Aggregate, name string) *inspector.EventDetail {
	for i := range a.EventDetails {
		if a.EventDetails[i].Name == name {
			return &a.EventDetails[i]
		}
	}
	return nil
}

func findProjection(m inspector.ProjectModel, name string) *inspector.Projection {
	for i := range m.Projection {
		if m.Projection[i].Name == name {
			return &m.Projection[i]
		}
	}
	return nil
}

func hasHandler(m inspector.ProjectModel, name string) bool {
	for _, h := range m.Handler {
		if h.Name == name {
			return true
		}
	}
	return false
}

func hasQuery(m inspector.ProjectModel, name string) bool {
	for _, q := range m.Query {
		if q.Name == name {
			return true
		}
	}
	return false
}

func aggNames(m inspector.ProjectModel) []string {
	var out []string
	for _, a := range m.Aggregate {
		out = append(out, a.FileName)
	}
	return out
}

// TestGolden_FlowEdgesFromRecipeOutput is the coupling contract for the
// per-event flow passes. Unlike the aggregate-level scanners, these read call
// expressions inside generated bodies — `s.store(ctx, agg, "X", …)`,
// `h.svc.X(…)`, and `switch e.EventName`. A recipe is the only generator path
// that emits all three, so we scaffold one and assert the full chain
// handler → command → event → projection is recovered. Reword any of those
// three shapes and this fails here instead of shipping a silently empty graph.
func TestGolden_FlowEdgesFromRecipeOutput(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	if err := generator.InitProject("example.com/shop", dir); err != nil {
		t.Fatal(err)
	}
	if err := generator.AddCRUD("product", nil); err != nil {
		t.Fatal(err)
	}

	m, err := inspector.Scan(dir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	// --- service commands and the events they emit ---
	var svc *inspector.Service
	for i := range m.Service {
		if m.Service[i].Name == "product" {
			svc = &m.Service[i]
		}
	}
	if svc == nil {
		t.Fatalf("inspector did not find the product service; services = %+v", m.Service)
	}
	if svc.Aggregate != "product" {
		t.Errorf("service aggregate = %q, want %q", svc.Aggregate, "product")
	}
	emits := map[string][]string{}
	for _, c := range svc.Commands {
		emits[c.Name] = c.Emits
	}
	for cmd, want := range map[string]string{
		"Create":  "ProductCreated",
		"Update":  "ProductUpdated",
		"Archive": "ProductArchived",
	} {
		if !slices.Contains(emits[cmd], want) {
			t.Errorf("command %s emits = %v, want to contain %s", cmd, emits[cmd], want)
		}
	}

	// --- handler methods delegating to those commands ---
	var handlerCalls []string
	for _, h := range m.Handler {
		if h.Name != "product" {
			continue
		}
		for _, method := range h.Methods {
			handlerCalls = append(handlerCalls, method.Calls...)
		}
	}
	for _, want := range []string{"Create", "Update", "Archive"} {
		if !slices.Contains(handlerCalls, want) {
			t.Errorf("handler calls = %v, want to contain %s", handlerCalls, want)
		}
	}

	// --- worker cases ---
	p := findProjection(m, "product")
	if p == nil {
		t.Fatalf("inspector did not find the product projection; projections = %+v", m.Projection)
	}
	for _, want := range []string{"ProductCreated", "ProductUpdated", "ProductArchived"} {
		if !slices.Contains(p.Events, want) {
			t.Errorf("worker events = %v, want to contain %s", p.Events, want)
		}
	}

	// --- the derived graph must contain the whole chain, unbroken ---
	g := inspector.BuildFlow(m, "")
	hasEdge := func(from, to string) bool {
		for _, e := range g.Edges {
			if e.From == from && e.To == to {
				return true
			}
		}
		return false
	}
	for _, want := range [][2]string{
		{"handler:product.Create", "command:product.Create"},
		{"command:product.Create", "event:product/ProductCreated"},
		{"event:product/ProductCreated", "projection:product"},
	} {
		if !hasEdge(want[0], want[1]) {
			t.Errorf("flow graph missing edge %s -> %s; edges = %+v", want[0], want[1], g.Edges)
		}
	}

	// A freshly scaffolded recipe is fully wired, so nothing should be
	// reported as a dead-end event.
	s := inspector.BuildStats(m)
	if s.UnproducedEvents != 0 || s.UnconsumedEvents != 0 {
		t.Errorf("recipe output reported dead ends: unproduced %d, unconsumed %d; gaps = %+v",
			s.UnproducedEvents, s.UnconsumedEvents, s.Gaps)
	}
}
