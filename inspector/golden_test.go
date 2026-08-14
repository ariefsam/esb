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
	} else if !p.Multi {
		t.Errorf("projection sales_report Multi = false, want true")
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
