package inspector_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/ariefsam/esb/inspector"
)

// flowModel is a hand-built model so the derivation tests do not depend on the
// scanners. "order" is fully wired; "audit" is deliberately broken in both
// directions so the gap logic has something to find.
func flowModel() inspector.ProjectModel {
	return inspector.ProjectModel{
		Aggregate: []inspector.Aggregate{
			{
				Name:     "order",
				FileName: "order",
				Events:   []string{"OrderPlaced", "OrderPaid"},
				EventDetails: []inspector.EventDetail{
					{Name: "OrderPlaced", Fields: []inspector.EventField{{Name: "Amount", Type: "int64"}}},
					{Name: "OrderPaid"},
				},
			},
			{
				Name:         "audit",
				FileName:     "audit",
				Events:       []string{"AuditLogged"},
				EventDetails: []inspector.EventDetail{{Name: "AuditLogged"}},
			},
		},
		Service: []inspector.Service{{
			Name:      "order",
			Aggregate: "order",
			Commands: []inspector.ServiceCommand{
				{Name: "Place", Emits: []string{"OrderPlaced"}},
				{Name: "Pay", Emits: []string{"OrderPaid"}},
			},
		}},
		Handler: []inspector.Handler{{
			Name:      "order",
			Aggregate: "order",
			Methods:   []inspector.HandlerMethod{{Name: "Place", Calls: []string{"Place"}}},
		}},
		Projection: []inspector.Projection{{
			Name:       "order",
			Aggregates: []string{"order"},
			Events:     []string{"OrderPlaced"},
		}},
		Query: []inspector.Query{{Name: "ListOrders", Aggregate: "order"}},
	}
}

// TestBuildFlow_ChainsHandlerToProjection asserts the whole write path exists
// as edges, and that the projection→query hop is marked inferred rather than
// presented as a parsed call.
func TestBuildFlow_ChainsHandlerToProjection(t *testing.T) {
	g := inspector.BuildFlow(flowModel(), "")

	edge := func(from, to string) *inspector.FlowEdge {
		for i := range g.Edges {
			if g.Edges[i].From == from && g.Edges[i].To == to {
				return &g.Edges[i]
			}
		}
		return nil
	}

	for _, want := range [][2]string{
		{"handler:order.Place", "command:order.Place"},
		{"command:order.Place", "event:order/OrderPlaced"},
		{"event:order/OrderPlaced", "projection:order"},
	} {
		e := edge(want[0], want[1])
		if e == nil {
			t.Fatalf("missing edge %s -> %s; edges = %+v", want[0], want[1], g.Edges)
		}
		if e.Inferred {
			t.Errorf("edge %s -> %s marked inferred, want exact", want[0], want[1])
		}
	}

	q := edge("projection:order", "query:ListOrders")
	if q == nil {
		t.Fatalf("missing projection -> query edge; edges = %+v", g.Edges)
	}
	if !q.Inferred {
		t.Errorf("projection -> query edge Inferred = false; it is derived from naming, not a call")
	}
}

// TestBuildFlow_FilterDropsDanglingEdges guards the rule that a filtered graph
// must never render a line to a node that was filtered out.
func TestBuildFlow_FilterDropsDanglingEdges(t *testing.T) {
	g := inspector.BuildFlow(flowModel(), "audit")

	ids := map[string]bool{}
	for _, col := range g.Columns {
		for _, n := range col.Nodes {
			ids[n.ID] = true
			if n.Aggregate != "audit" {
				t.Errorf("node %s has aggregate %q, want only audit", n.ID, n.Aggregate)
			}
		}
	}
	for _, e := range g.Edges {
		if !ids[e.From] || !ids[e.To] {
			t.Errorf("edge %s -> %s dangles outside the filtered node set", e.From, e.To)
		}
	}
}

// TestBuildFlow_WarnsOnDeadEnds checks the two dead-end directions are reported
// on the event node itself, since that is what the UI colours orange.
func TestBuildFlow_WarnsOnDeadEnds(t *testing.T) {
	g := inspector.BuildFlow(flowModel(), "")

	warn := map[string]string{}
	for _, col := range g.Columns {
		for _, n := range col.Nodes {
			warn[n.ID] = n.Warn
		}
	}
	if got := warn["event:order/OrderPlaced"]; got != "" {
		t.Errorf("fully wired event warned %q, want no warning", got)
	}
	if got := warn["event:order/OrderPaid"]; !strings.Contains(got, "projection") {
		t.Errorf("OrderPaid warn = %q, want it to mention no projection handles it", got)
	}
	if got := warn["event:audit/AuditLogged"]; got != "no producer, no consumer" {
		t.Errorf("AuditLogged warn = %q, want %q", got, "no producer, no consumer")
	}
}

// TestBuildFlow_DynamicEmitSuppressesProducerWarning: for a state machine the
// scanner genuinely cannot name the event, so claiming "no producer" would be
// a false alarm.
func TestBuildFlow_DynamicEmitSuppressesProducerWarning(t *testing.T) {
	m := flowModel()
	m.Service = []inspector.Service{{
		Name:      "order",
		Aggregate: "order",
		Commands:  []inspector.ServiceCommand{{Name: "Transition", Dynamic: true}},
	}}

	s := inspector.BuildStats(m)
	if s.UnproducedEvents != 1 {
		t.Errorf("UnproducedEvents = %d, want 1 (only audit, not the dynamic order events)", s.UnproducedEvents)
	}
	if s.DynamicCommands != 1 {
		t.Errorf("DynamicCommands = %d, want 1", s.DynamicCommands)
	}
}

func TestBuildStats_Counts(t *testing.T) {
	s := inspector.BuildStats(flowModel())

	if s.Aggregates != 2 || s.Events != 3 || s.Commands != 2 || s.HandlerMethods != 1 {
		t.Errorf("counts = agg %d, events %d, commands %d, handlerMethods %d; want 2/3/2/1",
			s.Aggregates, s.Events, s.Commands, s.HandlerMethods)
	}
	// One field across three events.
	if got := s.AvgFieldsPerEvent; got < 0.33 || got > 0.34 {
		t.Errorf("AvgFieldsPerEvent = %v, want ~0.333", got)
	}
	if s.UnproducedEvents != 1 {
		t.Errorf("UnproducedEvents = %d, want 1 (AuditLogged)", s.UnproducedEvents)
	}
	if s.UnconsumedEvents != 2 {
		t.Errorf("UnconsumedEvents = %d, want 2 (OrderPaid, AuditLogged)", s.UnconsumedEvents)
	}
}

func TestBuildEventFlows_ProducersAndConsumers(t *testing.T) {
	flows := inspector.BuildEventFlows(flowModel())

	byEvent := map[string]inspector.EventFlow{}
	for _, f := range flows {
		byEvent[f.Aggregate+"/"+f.Event] = f
	}

	placed := byEvent["order/OrderPlaced"]
	if !slices.Equal(placed.Producers, []string{"order.Place"}) {
		t.Errorf("OrderPlaced producers = %v, want [order.Place]", placed.Producers)
	}
	if !slices.Equal(placed.Consumers, []string{"order"}) {
		t.Errorf("OrderPlaced consumers = %v, want [order]", placed.Consumers)
	}

	audit := byEvent["audit/AuditLogged"]
	if len(audit.Producers) != 0 || len(audit.Consumers) != 0 {
		t.Errorf("AuditLogged should be orphaned, got producers %v consumers %v",
			audit.Producers, audit.Consumers)
	}
}
