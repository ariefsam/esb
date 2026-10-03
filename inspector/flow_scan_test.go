package inspector_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ariefsam/esb/inspector"
)

// writeProject lays out a minimal ESB-shaped tree from a path→source map so
// each test states exactly the declarations it depends on.
func writeProject(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	files["go.mod"] = "module example.com/flowtest\n\ngo 1.22\n"
	for rel, src := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func scanOrFatal(t *testing.T, dir string) inspector.ProjectModel {
	t.Helper()
	m, err := inspector.Scan(dir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	return m
}

const orderDomain = `package domain

const OrderAggregateName = "order"

// OrderPlaced event.
type OrderPlaced struct {
	Amount int64 ` + "`json:\"amount\"`" + `
}

// OrderPaid event.
type OrderPaid struct {
	Ref string ` + "`json:\"ref\"`" + `
}
`

// TestScanServices_LiteralEmits is the core contract: the event name a command
// stores must be recovered from the string literal the generator writes.
func TestScanServices_LiteralEmits(t *testing.T) {
	dir := writeProject(t, map[string]string{
		"domain/order.go": orderDomain,
		"service/order.go": `package service

import "context"

type OrderService struct{ eventRepo any }

func (s *OrderService) Place(ctx context.Context, id string) error {
	agg, err := s.load(ctx, id)
	if err != nil {
		return err
	}
	return s.store(ctx, agg, "OrderPlaced", domain.OrderPlaced{})
}

func (s *OrderService) Pay(ctx context.Context, id string) error {
	agg, _ := s.load(ctx, id)
	return s.store(ctx, agg, "OrderPaid", domain.OrderPaid{})
}

// Lookup is a read helper: no store() call, so it is not a command.
func (s *OrderService) Lookup(ctx context.Context, id string) error { return nil }

func (s *OrderService) load(ctx context.Context, id string) (any, error) { return nil, nil }
func (s *OrderService) store(ctx context.Context, agg any, eventName string, data any) error {
	return nil
}
`,
	})

	m := scanOrFatal(t, dir)
	if len(m.Service) != 1 {
		t.Fatalf("services = %+v, want exactly one", m.Service)
	}
	svc := m.Service[0]
	if svc.Aggregate != "order" {
		t.Errorf("service aggregate = %q, want %q", svc.Aggregate, "order")
	}

	got := map[string][]string{}
	for _, c := range svc.Commands {
		got[c.Name] = c.Emits
	}
	if _, ok := got["Lookup"]; ok {
		t.Errorf("Lookup was reported as a command; commands = %+v", svc.Commands)
	}
	if !slices.Equal(got["Place"], []string{"OrderPlaced"}) {
		t.Errorf("Place emits = %v, want [OrderPlaced]", got["Place"])
	}
	if !slices.Equal(got["Pay"], []string{"OrderPaid"}) {
		t.Errorf("Pay emits = %v, want [OrderPaid]", got["Pay"])
	}
}

// TestScanServices_DynamicEmitIsNotInvented guards the state-machine recipe:
// when the event name is a variable the scanner must say "unknown" rather than
// guess a name that would then show up as a phantom node in the flow.
func TestScanServices_DynamicEmitIsNotInvented(t *testing.T) {
	dir := writeProject(t, map[string]string{
		"domain/order.go": orderDomain,
		"service/order.go": `package service

import "context"

type OrderService struct{ eventRepo any }

func (s *OrderService) Transition(ctx context.Context, id, to string) error {
	eventName := domain.OrderEventFor(to)
	agg, _ := s.load(ctx, id)
	return s.store(ctx, agg, eventName, struct{}{})
}

func (s *OrderService) load(ctx context.Context, id string) (any, error) { return nil, nil }
func (s *OrderService) store(ctx context.Context, agg any, eventName string, data any) error {
	return nil
}
`,
	})

	m := scanOrFatal(t, dir)
	if len(m.Service) != 1 || len(m.Service[0].Commands) != 1 {
		t.Fatalf("services = %+v, want one service with one command", m.Service)
	}
	cmd := m.Service[0].Commands[0]
	if !cmd.Dynamic {
		t.Errorf("Transition Dynamic = false, want true")
	}
	if len(cmd.Emits) != 0 {
		t.Errorf("Transition emits = %v, want none (name is computed at runtime)", cmd.Emits)
	}
}

// TestScanHandlers_ServiceCalls checks the handler→command edge, including that
// a handler still carrying the generated TODO body reports no methods.
func TestScanHandlers_ServiceCalls(t *testing.T) {
	dir := writeProject(t, map[string]string{
		"domain/order.go": orderDomain,
		"server/handler/order.go": `package handler

import "net/http"

type OrderHandler struct{ svc *service.OrderService }

func (h *OrderHandler) Place(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Place(r.Context(), "id"); err != nil {
		return
	}
}

func (h *OrderHandler) Handle(w http.ResponseWriter, r *http.Request) {
	// TODO: call service — generated stub, no svc call yet.
}
`,
	})

	m := scanOrFatal(t, dir)
	if len(m.Handler) != 1 {
		t.Fatalf("handlers = %+v, want exactly one", m.Handler)
	}
	methods := m.Handler[0].Methods
	if len(methods) != 1 {
		t.Fatalf("handler methods = %+v, want only the one that calls svc", methods)
	}
	if methods[0].Name != "Place" || !slices.Equal(methods[0].Calls, []string{"OrderService.Place"}) {
		t.Errorf("handler method = %+v, want Place calling [OrderService.Place]", methods[0])
	}
}

// TestScanProjections_EventCases covers the worker side of the per-event edge.
func TestScanProjections_EventCases(t *testing.T) {
	dir := writeProject(t, map[string]string{
		"domain/order.go": orderDomain,
		"projection/order_worker.go": `package projection

type OrderProjectionWorker struct{}

func (w *OrderProjectionWorker) applyEvent(e any) error {
	switch e.EventName {
	case "OrderPlaced":
		return nil
	case "OrderPaid":
		return nil
	}
	return nil
}
`,
	})

	m := scanOrFatal(t, dir)
	if len(m.Projection) != 1 {
		t.Fatalf("projections = %+v, want exactly one", m.Projection)
	}
	want := []string{"OrderPaid", "OrderPlaced"} // sorted
	if !slices.Equal(m.Projection[0].Events, want) {
		t.Errorf("worker events = %v, want %v", m.Projection[0].Events, want)
	}
}

// TestScanProjections_NoSwitchYieldsNoEvents makes sure a freshly generated
// worker (marker present, no case injected) reports nothing rather than
// inheriting its aggregate's whole event list.
func TestScanProjections_NoSwitchYieldsNoEvents(t *testing.T) {
	dir := writeProject(t, map[string]string{
		"domain/order.go": orderDomain,
		"projection/order_worker.go": `package projection

type OrderProjectionWorker struct{}

func (w *OrderProjectionWorker) applyEvent(e any) error {
	switch e.EventName {
	// esb:inject:applyevent-cases
	default:
		return nil
	}
}
`,
	})

	m := scanOrFatal(t, dir)
	if len(m.Projection) != 1 {
		t.Fatalf("projections = %+v, want exactly one", m.Projection)
	}
	if len(m.Projection[0].Events) != 0 {
		t.Errorf("worker events = %v, want none", m.Projection[0].Events)
	}
}

// TestScan_StandaloneWorkerFoldedIntoReadModel: a *_worker.go that main.go does
// not start still holds the apply switch a running multi-aggregate worker
// dispatches to. It must not count as a projection, but its events must still
// be consumed by the worker that really runs.
func TestScan_StandaloneWorkerFoldedIntoReadModel(t *testing.T) {
	dir := writeProject(t, map[string]string{
		"domain/order.go": orderDomain,
		"projection/read_model_worker.go": `package projection

var readModelAggregateNames = []string{"order"}

type ReadModelProjectionWorker struct{}
`,
		"projection/order_worker.go": `package projection

type OrderProjectionWorker struct{}

func applyOrder(e any) error {
	switch e.EventName {
	case "OrderPlaced":
		return nil
	}
	return nil
}
`,
		"main.go": `package main

func main() {
	workers := []projection.Worker{
		// esb:inject:projection-workers
		app.ReadModelProjectionWorker,
	}
	_ = workers
}
`,
	})

	m := scanOrFatal(t, dir)
	if len(m.Projection) != 1 || m.Projection[0].Name != "read_model" {
		t.Fatalf("projections = %+v, want only read_model", m.Projection)
	}
	if !slices.Contains(m.Projection[0].Events, "OrderPlaced") {
		t.Errorf("read_model events = %v, want OrderPlaced merged in", m.Projection[0].Events)
	}
	if len(m.UnregisteredWorker) != 1 || m.UnregisteredWorker[0] != (inspector.UnregisteredWorker{Name: "order", Host: "read_model"}) {
		t.Errorf("UnregisteredWorker = %+v", m.UnregisteredWorker)
	}
}

// TestScanServices_StoreWithKeyAndHelpers: `esb add idempotency` emits through
// storeWithKey, and commands often store from an unexported helper. Both must
// count as the command emitting the event.
func TestScanServices_StoreWithKeyAndHelpers(t *testing.T) {
	dir := writeProject(t, map[string]string{
		"domain/order.go": orderDomain,
		"service/order.go": `package service

import "context"

type OrderService struct{ eventRepo any }

func (s *OrderService) Place(ctx context.Context, id, key string) error {
	agg, _ := s.load(ctx, id)
	return s.storeWithKey(ctx, agg, "OrderPlaced", nil, key)
}

func (s *OrderService) Pay(ctx context.Context, id string) error {
	return s.markPaid(ctx, id)
}

func (s *OrderService) markPaid(ctx context.Context, id string) error {
	agg, _ := s.load(ctx, id)
	return s.store(ctx, agg, "OrderPaid", nil)
}
`,
	})

	m := scanOrFatal(t, dir)
	if len(m.Service) != 1 {
		t.Fatalf("services = %+v", m.Service)
	}
	got := map[string][]string{}
	for _, c := range m.Service[0].Commands {
		got[c.Name] = c.Emits
	}
	if !slices.Equal(got["Place"], []string{"OrderPlaced"}) {
		t.Errorf("Place emits %v, want [OrderPlaced]", got["Place"])
	}
	if !slices.Equal(got["Pay"], []string{"OrderPaid"}) {
		t.Errorf("Pay emits %v, want [OrderPaid] via markPaid", got["Pay"])
	}
	if _, ok := got["markPaid"]; ok {
		t.Error("unexported helper must not become a command node")
	}
}

// TestScan_CrossServiceHelpers: a command that stores through an unexported
// helper of another service produces that service's event, on that service's
// aggregate. A non-service struct (a resolver) that stores is a command entry
// of its own, and a handler holding it under any field name links to it.
func TestScan_CrossServiceHelpers(t *testing.T) {
	dir := writeProject(t, map[string]string{
		"domain/order.go": orderDomain,
		"domain/cart.go": `package domain

const CartAggregateName = "cart"

type CartCheckedOut struct{}

type Cart struct{}

func (a *Cart) Apply(e Event) {
	switch e.EventName {
	case "CartCheckedOut":
	}
}
`,
		"service/order.go": `package service

type OrderService struct {
	eventRepo any
	carts     *CartService
}

func (s *OrderService) Place(ctx context.Context, id string) error {
	if err := s.carts.checkout(ctx, id); err != nil {
		return err
	}
	return s.store(ctx, nil, "OrderPlaced", nil)
}
`,
		"service/cart.go": `package service

type CartService struct{ eventRepo any }

func (s *CartService) checkout(ctx context.Context, id string) error {
	return s.store(ctx, nil, "CartCheckedOut", nil)
}
`,
		"service/order_resolver.go": `package service

type OrderResolver struct{ orders *OrderService }

func (r *OrderResolver) Pay(ctx context.Context, id string) error {
	return r.orders.markPaid(ctx, id)
}

func (s *OrderService) markPaid(ctx context.Context, id string) error {
	return s.store(ctx, nil, "OrderPaid", nil)
}
`,
		"server/handler/pay_order.go": `package handler

type PayOrderHandler struct{ resolver *service.OrderResolver }

func (h *PayOrderHandler) Handle(w http.ResponseWriter, r *http.Request) {
	_ = h.resolver.Pay(r.Context(), "id")
}
`,
	})

	m := scanOrFatal(t, dir)
	svc := map[string]inspector.Service{}
	for _, s := range m.Service {
		svc[s.Name] = s
	}

	place := svc["order"].Commands
	if len(place) != 1 || place[0].Name != "Place" ||
		!slices.Equal(place[0].Emits, []string{"OrderPlaced"}) ||
		!slices.Equal(place[0].Other, []inspector.EventRef{{Aggregate: "cart", Event: "CartCheckedOut"}}) {
		t.Errorf("order commands = %+v, want Place emitting OrderPlaced + cart/CartCheckedOut", place)
	}
	if len(svc["cart"].Commands) != 0 {
		t.Errorf("cart commands = %+v, want none (checkout is unexported)", svc["cart"].Commands)
	}
	res, ok := svc["order_resolver"]
	if !ok || res.Aggregate != "order" || res.File != "service/order_resolver.go" ||
		len(res.Commands) != 1 || !slices.Equal(res.Commands[0].Emits, []string{"OrderPaid"}) {
		t.Errorf("order_resolver = %+v, want Pay emitting OrderPaid on order", res)
	}

	g := inspector.BuildFlow(m, "cart")
	edges := map[string]bool{}
	for _, e := range g.Edges {
		edges[e.From+" -> "+e.To] = true
	}
	// Filtering by cart keeps the order command that stores cart's event.
	if !edges["command:order.Place -> event:cart/CartCheckedOut"] {
		t.Errorf("cart view edges = %v, want order.Place -> cart/CartCheckedOut", g.Edges)
	}

	g = inspector.BuildFlow(m, "")
	edges = map[string]bool{}
	for _, e := range g.Edges {
		edges[e.From+" -> "+e.To] = true
	}
	if !edges["handler:pay_order.Handle -> command:order_resolver.Pay"] {
		t.Errorf("edges = %v, want pay_order.Handle -> order_resolver.Pay", g.Edges)
	}
}

// TestScanServices_StoreWrapperIsNotDynamic: store() delegating to
// storeWithKey(ctx, agg, eventName, …) passes a variable, but the literal was
// already read where store() is called, so the command is not dynamic.
func TestScanServices_StoreWrapperIsNotDynamic(t *testing.T) {
	dir := writeProject(t, map[string]string{
		"domain/order.go": orderDomain,
		"service/order.go": `package service

type OrderService struct{ eventRepo any }

func (s *OrderService) Place(ctx context.Context, id string) error {
	return s.store(ctx, nil, "OrderPlaced", nil)
}

func (s *OrderService) store(ctx context.Context, agg any, eventName string, data any) error {
	return s.storeWithKey(ctx, agg, eventName, data, "")
}
`,
	})
	m := scanOrFatal(t, dir)
	c := m.Service[0].Commands
	if len(c) != 1 || c[0].Dynamic || !slices.Equal(c[0].Emits, []string{"OrderPlaced"}) {
		t.Errorf("commands = %+v, want Place emitting OrderPlaced, not dynamic", c)
	}
}

// TestBuildFlow_EventStoreLayer: events are written into their aggregate's
// store node, a method that only loads an aggregate gets a read edge, and a
// command's own load before storing is not drawn. A handler that only reads
// is marked as reading the write model.
func TestBuildFlow_EventStoreLayer(t *testing.T) {
	dir := writeProject(t, map[string]string{
		"domain/order.go": orderDomain,
		"service/order.go": `package service

type OrderService struct{ eventRepo domain.EventRepository }

func (s *OrderService) Place(ctx context.Context, id string) error {
	if _, err := s.load(ctx, id); err != nil {
		return err
	}
	return s.store(ctx, nil, "OrderPlaced", nil)
}

func (s *OrderService) Get(ctx context.Context, id string) error {
	_, err := s.load(ctx, id)
	return err
}

func (s *OrderService) load(ctx context.Context, id string) (any, error) {
	return s.eventRepo.Retrieve(ctx, id, "order", 0)
}

func (s *OrderService) store(ctx context.Context, agg any, name string, data any) error {
	_, err := s.eventRepo.StoreAtomic(ctx, nil, 0)
	return err
}
`,
		"server/handler/place_order.go": `package handler

type PlaceOrderHandler struct{ svc *service.OrderService }

func (h *PlaceOrderHandler) Handle(w http.ResponseWriter, r *http.Request) {
	_ = h.svc.Place(r.Context(), "id")
}
`,
		"server/handler/get_order.go": `package handler

type GetOrderHandler struct{ svc *service.OrderService }

func (h *GetOrderHandler) Handle(w http.ResponseWriter, r *http.Request) {
	_ = h.svc.Get(r.Context(), "id")
}
`,
	})

	m := scanOrFatal(t, dir)
	g := inspector.BuildFlow(m, "order")
	edges := map[string]string{}
	for _, e := range g.Edges {
		edges[e.From+" -> "+e.To] = e.Op
	}
	for edge, op := range map[string]string{
		"handler:place_order.Handle -> command:order.Place": "",
		"handler:get_order.Handle -> command:order.Get":     "",
		"command:order.Place -> event:order/OrderPlaced":    "",
		"event:order/OrderPlaced -> store:order":            "write",
		"command:order.Get -> store:order":                  "read",
	} {
		got, ok := edges[edge]
		if !ok || got != op {
			t.Errorf("edge %s: op=%q present=%v, want op %q", edge, got, ok, op)
		}
	}
	if _, ok := edges["command:order.Place -> store:order"]; ok {
		t.Error("a command's own load before storing must not be drawn")
	}
	for _, c := range g.Columns {
		for _, n := range c.Nodes {
			if n.Label == "get_order.Handle" && n.Sub != "baca write model langsung" {
				t.Errorf("get_order sub = %q, want read-only marker", n.Sub)
			}
			if n.Label == "place_order.Handle" && n.Sub == "baca write model langsung" {
				t.Error("place_order writes, must not be marked read-only")
			}
		}
	}
}

// TestBuildFlow_FilterKeepsMultiProjection: a multi-aggregate worker has no
// single aggregate, yet filtering by one of its aggregates must still show it
// handling that aggregate's events.
func TestBuildFlow_FilterKeepsMultiProjection(t *testing.T) {
	m := inspector.ProjectModel{
		Aggregate: []inspector.Aggregate{{Name: "order", Events: []string{"OrderPlaced"},
			EventDetails: []inspector.EventDetail{{Name: "OrderPlaced"}}}},
		Projection: []inspector.Projection{{Name: "read_model", Multi: true,
			Aggregates: []string{"cart", "order"}, Events: []string{"OrderPlaced"}}},
	}
	g := inspector.BuildFlow(m, "order")
	found := false
	for _, e := range g.Edges {
		if e.From == "event:order/OrderPlaced" && e.To == "projection:read_model" {
			found = true
		}
	}
	if !found {
		t.Errorf("filtered edges = %+v, want OrderPlaced -> read_model", g.Edges)
	}
}

// TestBuildFlowWith_Filters: several aggregates at once, "problems only" and
// hiding stub handlers.
func TestBuildFlowWith_Filters(t *testing.T) {
	m := inspector.ProjectModel{
		Aggregate: []inspector.Aggregate{
			{Name: "order", Events: []string{"OrderPlaced", "OrderLost"},
				EventDetails: []inspector.EventDetail{{Name: "OrderPlaced"}, {Name: "OrderLost"}}},
			{Name: "cart", Events: []string{"CartOpened"},
				EventDetails: []inspector.EventDetail{{Name: "CartOpened"}}},
			{Name: "user", Events: []string{"UserJoined"},
				EventDetails: []inspector.EventDetail{{Name: "UserJoined"}}},
		},
		Service: []inspector.Service{{Name: "order", Struct: "OrderService", Aggregate: "order",
			Commands: []inspector.ServiceCommand{{Name: "Place", Emits: []string{"OrderPlaced"}}}}},
		Handler: []inspector.Handler{
			{Name: "place_order", Aggregate: "order", Methods: []inspector.HandlerMethod{{Name: "Handle", Calls: []string{"OrderService.Place"}}}},
			{Name: "todo_order", Aggregate: "order"},
		},
		Projection: []inspector.Projection{{Name: "order", Aggregates: []string{"order"}, Events: []string{"OrderPlaced"}}},
	}
	ids := func(g inspector.FlowGraph) map[string]bool {
		out := map[string]bool{}
		for _, c := range g.Columns {
			for _, n := range c.Nodes {
				out[n.ID] = true
			}
		}
		return out
	}

	two := ids(inspector.BuildFlowWith(m, inspector.FlowOptions{Aggregates: []string{"order", "cart"}}))
	if !two["event:order/OrderPlaced"] || !two["event:cart/CartOpened"] || two["event:user/UserJoined"] {
		t.Errorf("order+cart filter nodes = %v", two)
	}

	stubs := ids(inspector.BuildFlowWith(m, inspector.FlowOptions{HideStubs: true}))
	if stubs["handler:todo_order."] || !stubs["handler:place_order.Handle"] {
		t.Errorf("hide stubs nodes = %v", stubs)
	}

	problems := ids(inspector.BuildFlowWith(m, inspector.FlowOptions{Aggregates: []string{"order"}, Problems: true}))
	// OrderLost has no producer and no consumer; OrderPlaced is healthy.
	if !problems["event:order/OrderLost"] || problems["command:order.Place"] {
		t.Errorf("problems nodes = %v", problems)
	}
}

// TestScanReadModel: projections and queries are linked through the tables
// they share (row types and raw SQL), a function that writes is a read-model
// writer rather than a query, and handlers and services calling projection
// functions directly get edges to them.
func TestScanReadModel(t *testing.T) {
	dir := writeProject(t, map[string]string{
		"domain/order.go": orderDomain,
		"projection/order_row.go": `package projection

type OrderRow struct{ AggregateID string }

func (OrderRow) TableName() string { return "orders" }
`,
		"projection/read_model_worker.go": `package projection

var readModelAggregateNames = []string{"order"}

type ReadModelProjectionWorker struct{ apply func(tx *gorm.DB, e Event) error }

func NewReadModelProjectionWorker() *ReadModelProjectionWorker {
	return &ReadModelProjectionWorker{apply: applyToReadModel}
}

func (w *ReadModelProjectionWorker) Run() { _ = w.apply(nil, Event{}) }

func applyToReadModel(tx *gorm.DB, e Event) error { return applyOrder(tx, e) }
`,
		"projection/order_worker.go": `package projection

func applyOrder(tx *gorm.DB, e Event) error {
	switch e.EventName {
	case "OrderPlaced":
		return tx.Create(&OrderRow{}).Error
	}
	return nil
}
`,
		"projection/query.go": `package projection

func OrderCount(ctx context.Context, db *gorm.DB) (map[string]int64, error) {
	var n int64
	err := db.Raw("SELECT COUNT(*) FROM orders").Scan(&n).Error
	return nil, err
}

func SyncOrderRow(ctx context.Context, db *gorm.DB, row OrderRow) error {
	return db.Save(&row).Error
}
`,
		"main.go": `package main

func main() {
	workers := []projection.Worker{
		// esb:inject:projection-workers
		app.ReadModelProjectionWorker,
	}
	_ = workers
}
`,
		"service/order.go": `package service

import "example.com/flowtest/projection"

type OrderService struct{ db *gorm.DB }

func (s *OrderService) Fix(ctx context.Context) error {
	return projection.SyncOrderRow(ctx, s.db, projection.OrderRow{})
}
`,
		"server/handler/fix_order.go": `package handler

type FixOrderHandler struct{ svc *service.OrderService }

func (h *FixOrderHandler) Handle(w http.ResponseWriter, r *http.Request) {
	_ = h.svc.Fix(r.Context())
}
`,
		"server/handler/order_count.go": `package handler

import "example.com/flowtest/projection"

type OrderCountHandler struct{ db *gorm.DB }

func (h *OrderCountHandler) Handle(w http.ResponseWriter, r *http.Request) {
	_, _ = projection.OrderCount(r.Context(), h.db)
}
`,
	})

	m := scanOrFatal(t, dir)
	if len(m.Projection) != 1 || !slices.Equal(m.Projection[0].Tables, []string{"orders"}) {
		t.Fatalf("projections = %+v, want read_model writing orders", m.Projection)
	}
	q := map[string]inspector.Query{}
	for _, x := range m.Query {
		q[x.Name] = x
	}
	if c := q["OrderCount"]; c.Writes || c.Aggregate != "order" || !slices.Equal(c.Tables, []string{"orders"}) {
		t.Errorf("OrderCount = %+v, want a read of orders on aggregate order", c)
	}
	if !q["SyncOrderRow"].Writes {
		t.Errorf("SyncOrderRow = %+v, want a writer", q["SyncOrderRow"])
	}

	g := inspector.BuildFlow(m, "")
	edges := map[string]inspector.FlowEdge{}
	for _, e := range g.Edges {
		edges[e.From+" -> "+e.To] = e
	}
	if e, ok := edges["projection:read_model -> query:OrderCount"]; !ok || e.Inferred {
		t.Errorf("want a solid read_model -> OrderCount edge, got %+v (present %v)", e, ok)
	}
	if _, ok := edges["projection:read_model -> query:SyncOrderRow"]; ok {
		t.Error("a read-model writer is not fed by the projection")
	}
	if e := edges["handler:order_count.Handle -> query:OrderCount"]; e.Op != "rm-read" {
		t.Errorf("handler -> OrderCount op = %q, want rm-read", e.Op)
	}
	if e := edges["command:order.Fix -> query:SyncOrderRow"]; e.Op != "rm-write" {
		t.Errorf("order.Fix -> SyncOrderRow op = %q, want rm-write (edges %v)", e.Op, g.Edges)
	}
	found := false
	for _, gap := range inspector.BuildStats(m).Gaps {
		if gap.Subject == "service order.Fix" {
			found = true
		}
	}
	if !found {
		t.Error("want a gap for the service writing the read model directly")
	}
}
