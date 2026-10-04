package inspector_test

import (
	"slices"
	"testing"

	"github.com/ariefsam/esb/inspector"
)

const annotatedDomain = `package domain

const OrderAggregateName = "order"

type OrderPlaced struct{}

// OrderAudited is only for the write model.
// esb:no-projection
type OrderAudited struct{}

type Order struct{}

func (a *Order) Apply(e Event) {
	switch e.EventName {
	case "OrderPlaced":
	case "OrderAudited":
	}
}
`

// TestAnnotations: esb:emits names the events of a computed store call and
// clears the dynamic flag, esb:no-projection silences "no projection handles
// it" for that event, and esb:ignore leaves a handler method out.
func TestAnnotations(t *testing.T) {
	dir := writeProject(t, map[string]string{
		"domain/order.go": annotatedDomain,
		"domain/cart.go": `package domain

// esb:no-projection
const CartAggregateName = "cart"

type CartClosed struct{}
`,
		"service/order.go": `package service

type OrderService struct{ eventRepo any }

// Place stores the event its state machine picks.
// esb:emits OrderPlaced, cart/CartClosed
func (s *OrderService) Place(ctx context.Context, name string) error {
	return s.store(ctx, nil, name, nil)
}

// Audit has no annotation, so its computed name is reported.
func (s *OrderService) Audit(ctx context.Context, name string) error {
	return s.store(ctx, nil, name, nil)
}
`,
		"server/handler/order.go": `package handler

type OrderHandler struct{ svc *service.OrderService }

func (h *OrderHandler) Place(w http.ResponseWriter, r *http.Request) {
	_ = h.svc.Place(r.Context(), "x")
}

// esb:ignore
func (h *OrderHandler) Health(w http.ResponseWriter, r *http.Request) {
	_ = h.svc.Ping()
}
`,
	})

	m := scanOrFatal(t, dir)
	cmds := map[string]inspector.ServiceCommand{}
	for _, c := range m.Service[0].Commands {
		cmds[c.Name] = c
	}
	place := cmds["Place"]
	if place.Dynamic || !slices.Equal(place.Emits, []string{"OrderPlaced"}) ||
		!slices.Equal(place.Other, []inspector.EventRef{{Aggregate: "cart", Event: "CartClosed"}}) {
		t.Errorf("Place = %+v, want declared OrderPlaced + cart/CartClosed, not dynamic", place)
	}
	if !cmds["Audit"].Dynamic {
		t.Errorf("Audit = %+v, want dynamic (no annotation)", cmds["Audit"])
	}

	for _, h := range m.Handler {
		for _, hm := range h.Methods {
			if hm.Name == "Health" {
				t.Error("esb:ignore handler method must be left out")
			}
		}
	}

	gaps := map[string][]string{}
	for _, g := range inspector.BuildStats(m).Gaps {
		gaps[g.Subject] = append(gaps[g.Subject], g.Message)
	}
	// Audit emits a computed name on order, which already silences "no
	// producer"; no-projection on the type silences "no consumer".
	if g := gaps["event order/OrderAudited"]; len(g) != 0 {
		t.Errorf("OrderAudited gaps = %v, want none", g)
	}
	if !slices.Contains(gaps["event order/OrderPlaced"], "no projection handles it") {
		t.Errorf("OrderPlaced gaps = %v, an unmarked event keeps its warning", gaps["event order/OrderPlaced"])
	}
	if slices.Contains(gaps["aggregate cart"], "belum punya projection") {
		t.Errorf("cart gaps = %v, file-level esb:no-projection must drop 'belum punya projection'", gaps["aggregate cart"])
	}
	if !slices.Contains(gaps["aggregate order"], "belum punya projection") {
		t.Errorf("order gaps = %v, an event-level marker must not cover the whole aggregate", gaps["aggregate order"])
	}

	var dynamic []string
	for _, d := range m.Diagnostics {
		if d.Code == "dynamic-event" {
			dynamic = append(dynamic, d.Message)
			if d.File != "service/order.go" || d.Line == 0 {
				t.Errorf("dynamic-event location = %s:%d", d.File, d.Line)
			}
		}
	}
	if len(dynamic) != 1 {
		t.Errorf("dynamic-event diagnostics = %v, want exactly one (Audit)", dynamic)
	}
}

// TestDiagnostics: a file that does not parse, a handler whose call leads
// nowhere known, and a query without a recognisable table are each reported
// with where they are.
func TestDiagnostics(t *testing.T) {
	dir := writeProject(t, map[string]string{
		"domain/order.go": orderDomain,
		"service/broken.go": `package service

func (s *X) oops( {
`,
		"service/session.go": `package service

type SessionService struct{}

func (s *SessionService) Revoke(ctx context.Context) error { return nil }
`,
		"server/handler/logout.go": `package handler

type LogoutHandler struct{ sessions *service.SessionService }

func (h *LogoutHandler) Handle(w http.ResponseWriter, r *http.Request) {
	_ = h.sessions.Revoke(r.Context())
}
`,
		"projection/query.go": `package projection

func CountThings(ctx context.Context, db *gorm.DB) (int64, error) {
	return 0, nil
}
`,
	})

	m := scanOrFatal(t, dir)
	byCode := map[string]inspector.Diagnostic{}
	for _, d := range m.Diagnostics {
		byCode[d.Code] = d
	}
	if d := byCode["parse-error"]; d.File != "service/broken.go" || d.Line == 0 || d.Level != "warn" {
		t.Errorf("parse-error = %+v", d)
	}
	if d := byCode["handler-unknown-call"]; d.File != "server/handler/logout.go" || d.Line != 5 {
		t.Errorf("handler-unknown-call = %+v", d)
	}
	if d := byCode["query-no-table"]; d.File != "projection/query.go" || d.Line != 3 {
		t.Errorf("query-no-table = %+v", d)
	}
}

// TestSourceFilesIncludeDiagnostics: the code viewer may open the file a
// diagnostic points at, even one no node comes from (it does not parse).
func TestSourceFilesIncludeDiagnostics(t *testing.T) {
	dir := writeProject(t, map[string]string{
		"domain/order.go":   orderDomain,
		"service/broken.go": "package service\n\nfunc (s *X) oops( {\n",
	})
	m := scanOrFatal(t, dir)
	if !inspector.SourceFiles(m, dir)["service/broken.go"] {
		t.Error("service/broken.go has a parse-error diagnostic, so it must be viewable")
	}
	if inspector.SourceFiles(m, dir)["go.mod"] {
		t.Error("go.mod is neither a node nor a diagnostic and must not be served")
	}
}
