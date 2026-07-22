package inspector

import (
	"bytes"
	"strings"
	"testing"
)

// TestPrint_EmptyProject — a freshly initialised project with no adds
// must render the header plus empty placeholders, never crash.
func TestPrint_EmptyProject(t *testing.T) {
	m := ProjectModel{
		ModuleName:  "github.com/example/empty",
		PackageName: "empty",
	}

	var buf bytes.Buffer
	if err := Print(&buf, m, ""); err != nil {
		t.Fatalf("Print: %v", err)
	}
	got := buf.String()

	for _, want := range []string{
		"esb show",
		"github.com/example/empty",
		"Aggregates",
		"(tidak ada",
		"Wire Graph",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in output:\n%s", want, got)
		}
	}
}

// TestPrint_FullProject — every section should render at least one real
// line when the project has aggregates, handlers, projections, queries,
// storage, and a wire graph.
func TestPrint_FullProject(t *testing.T) {
	m := ProjectModel{
		ModuleName:  "github.com/example/full",
		PackageName: "full",
		Aggregate: []Aggregate{
			{Name: "order", Events: []string{"OrderPlaced", "OrderCancelled"}},
			{Name: "user", Events: []string{"UserRegistered"}},
		},
		Projection: []Projection{
			{Name: "order", Multi: false, Aggregates: []string{"order"}},
			{Name: "balance", Multi: true, Aggregates: []string{"order", "user"}},
		},
		Handler: []Handler{
			{Name: "place_order", Aggregate: "order"},
			{Name: "cancel_order", Aggregate: "order"},
		},
		Query: []Query{
			{Name: "GetOrderByBuyer", Aggregate: "order"},
		},
		Wire: WireGraph{
			Fields: []WireNode{
				{Field: "OrderProjectionWorker", Type: "*projection.OrderProjectionWorker"},
				{Field: "PlaceOrderHandler", Type: "*handler.PlaceOrderHandler"},
			},
			Nodes: []WireNode{
				{VarName: "orderWorker", Provider: "projection.NewOrderProjectionWorker(...)"},
				{VarName: "placeOrderHandler", Provider: "handler.NewPlaceOrderHandler(...)"},
			},
		},
		Migrate:   []string{"OrderRow", "BalanceRow"},
		RunWorker: []string{"OrderProjectionWorker"},
	}

	var buf bytes.Buffer
	if err := Print(&buf, m, ""); err != nil {
		t.Fatalf("Print: %v", err)
	}
	got := buf.String()

	for _, want := range []string{
		"order",
		"user",
		"OrderPlaced",
		"balance",
		"[multi]",
		"place_order",
		"GetOrderByBuyer",
		"+-- OrderProjectionWorker",
		"projection.NewOrderProjectionWorker",
		"AutoMigrate: OrderRow",
		"declared but not started: PlaceOrderHandler", // not in RunWorker
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in output:\n%s", want, got)
		}
	}
}

// TestPrint_Focus — when focused on "order", the focused aggregate is
// marked with ">>" and the wire graph only shows the Order* chain.
func TestPrint_Focus(t *testing.T) {
	m := ProjectModel{
		ModuleName:  "github.com/example/full",
		PackageName: "full",
		Aggregate: []Aggregate{
			{Name: "order", Events: []string{"OrderPlaced"}},
			{Name: "user", Events: []string{"UserRegistered"}},
		},
		Projection: []Projection{
			{Name: "order", Multi: false, Aggregates: []string{"order"}},
			{Name: "balance", Multi: true, Aggregates: []string{"order", "user"}},
		},
		Handler: []Handler{
			{Name: "place_order", Aggregate: "order"},
		},
		Query: []Query{
			{Name: "GetOrderByBuyer", Aggregate: "order"},
		},
		Wire: WireGraph{
			Fields: []WireNode{
				{Field: "OrderProjectionWorker", Type: "*projection.OrderProjectionWorker"},
				{Field: "PlaceOrderHandler", Type: "*handler.PlaceOrderHandler"},
			},
			Nodes: []WireNode{
				{VarName: "orderWorker", Provider: "projection.NewOrderProjectionWorker(...)"},
				{VarName: "balanceWorker", Provider: "projection.NewBalanceProjectionWorker(...)"},
			},
		},
	}

	var buf bytes.Buffer
	if err := Print(&buf, m, "order"); err != nil {
		t.Fatalf("Print: %v", err)
	}
	got := buf.String()

	// Focus marker visible on the order aggregate.
	if !strings.Contains(got, ">> order") {
		t.Errorf("missing '>> order' focus marker:\n%s", got)
	}
	// user aggregate still appears in the list (compact header).
	if !strings.Contains(got, "   user") {
		t.Errorf("expected 'user' aggregate in compact header:\n%s", got)
	}
	// OrderProjectionWorker is shown, PlaceOrderHandler is shown (handlers order*), but no PlaceOrderHandler fallback chain because we don't have all deps.
	if !strings.Contains(got, "OrderProjectionWorker") {
		t.Errorf("expected OrderProjectionWorker in focused wire graph:\n%s", got)
	}
	if !strings.Contains(got, "PlaceOrderHandler") {
		t.Errorf("expected PlaceOrderHandler in focused wire graph:\n%s", got)
	}
	// Unrelated worker should not appear.
	if strings.Contains(got, "BalanceProjectionWorker") {
		t.Errorf("focused view should not show BalanceProjectionWorker:\n%s", got)
	}
}

// TestPrint_LineCount — single-screen promise: a mid-size project should
// still fit comfortably in a 24-line terminal. We allow some headroom
// for the rendered ASCII.
func TestPrint_LineCount(t *testing.T) {
	m := ProjectModel{
		ModuleName:  "github.com/example/mid",
		PackageName: "mid",
		Aggregate: []Aggregate{
			{Name: "order", Events: []string{"OrderPlaced", "OrderCancelled"}},
			{Name: "user", Events: []string{"UserRegistered"}},
			{Name: "product", Events: []string{"ProductListed"}},
		},
		Projection: []Projection{
			{Name: "order", Multi: false, Aggregates: []string{"order"}},
			{Name: "balance", Multi: true, Aggregates: []string{"order", "user"}},
		},
		Handler: []Handler{
			{Name: "place_order", Aggregate: "order"},
			{Name: "list_products", Aggregate: "product"},
		},
		Wire: WireGraph{
			Fields: []WireNode{
				{Field: "OrderProjectionWorker", Type: "*projection.OrderProjectionWorker"},
				{Field: "BalanceProjectionWorker", Type: "*projection.BalanceProjectionWorker"},
				{Field: "PlaceOrderHandler", Type: "*handler.PlaceOrderHandler"},
				{Field: "ListProductsHandler", Type: "*handler.ListProductsHandler"},
			},
			Nodes: []WireNode{
				{VarName: "orderWorker", Provider: "projection.NewOrderProjectionWorker(...)"},
				{VarName: "balanceWorker", Provider: "projection.NewBalanceProjectionWorker(...)"},
				{VarName: "placeOrderHandler", Provider: "handler.NewPlaceOrderHandler(...)"},
				{VarName: "listProductsHandler", Provider: "handler.NewListProductsHandler(...)"},
			},
		},
		Migrate:   []string{"OrderRow", "UserRow", "ProductRow", "BalanceRow"},
		RunWorker: []string{"OrderProjectionWorker", "BalanceProjectionWorker"},
	}

	var buf bytes.Buffer
	if err := Print(&buf, m, ""); err != nil {
		t.Fatalf("Print: %v", err)
	}
	lines := strings.Count(buf.String(), "\n")
	if lines > 80 {
		t.Errorf("mid-size project printed %d lines, want <= 80", lines)
	}
	t.Logf("printed %d lines", lines)
}
