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
	if methods[0].Name != "Place" || !slices.Equal(methods[0].Calls, []string{"Place"}) {
		t.Errorf("handler method = %+v, want Place calling [Place]", methods[0])
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
