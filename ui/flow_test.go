package ui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ariefsam/esb/inspector"
)

// flowProjectRoot writes a small but fully wired project so the page has a
// real chain to draw rather than an empty canvas.
func flowProjectRoot(t *testing.T) string {
	t.Helper()
	dir := makeProjectRoot(t)
	files := map[string]string{
		"domain/order.go": `package domain

const OrderAggregateName = "order"

// OrderPlaced event.
type OrderPlaced struct {
	Amount int64 ` + "`json:\"amount\"`" + `
}
`,
		"service/order.go": `package service

import "context"

type OrderService struct{ eventRepo any }

func (s *OrderService) Place(ctx context.Context, id string) error {
	agg, _ := s.load(ctx, id)
	return s.store(ctx, agg, "OrderPlaced", nil)
}

func (s *OrderService) load(ctx context.Context, id string) (any, error) { return nil, nil }
func (s *OrderService) store(ctx context.Context, agg any, eventName string, data any) error {
	return nil
}
`,
		"server/handler/order.go": `package handler

import "net/http"

type OrderHandler struct{ svc *service.OrderService }

func (h *OrderHandler) Place(w http.ResponseWriter, r *http.Request) {
	_ = h.svc.Place(r.Context(), "id")
}
`,
		"projection/order_worker.go": `package projection

type OrderProjectionWorker struct{}

func (w *OrderProjectionWorker) applyEvent(e any) error {
	switch e.EventName {
	case "OrderPlaced":
		return nil
	}
	return nil
}
`,
	}
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

func flowTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv, err := NewServer(Options{ProjectRoot: flowProjectRoot(t)})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func TestServer_FlowPageRenders(t *testing.T) {
	ts := flowTestServer(t)

	resp, err := http.Get(ts.URL + "/flow")
	if err != nil {
		t.Fatalf("GET /flow: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := readAll(resp.Body)
	if strings.Contains(body, "render error") {
		t.Fatalf("flow template failed to render: %q", body)
	}
	// The whole chain must be visible, plus the SVG itself.
	for _, want := range []string{
		"<svg", "order.Place", "OrderPlaced", "Statistik proyek", "flow-node-event",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("flow page missing %q", want)
		}
	}
}

func TestServer_FlowPageRejectsPost(t *testing.T) {
	ts := flowTestServer(t)

	resp, err := http.Post(ts.URL+"/flow", "text/plain", strings.NewReader(""))
	if err != nil {
		t.Fatalf("POST /flow: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}

// TestServer_FlowPageUnknownAggregate404s: silently ignoring a bad filter would
// render the whole project while the URL claims it is filtered.
func TestServer_FlowPageUnknownAggregate404s(t *testing.T) {
	ts := flowTestServer(t)

	resp, err := http.Get(ts.URL + "/flow?aggregate=nope")
	if err != nil {
		t.Fatalf("GET /flow?aggregate=nope: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestServer_FlowPageNoCDNURLs(t *testing.T) {
	ts := flowTestServer(t)

	resp, err := http.Get(ts.URL + "/flow")
	if err != nil {
		t.Fatalf("GET /flow: %v", err)
	}
	body, _ := readAll(resp.Body)
	resp.Body.Close()
	// The graph is server-rendered precisely so this page needs no network.
	for _, bad := range []string{"cdn.", "googleapis", "unpkg", "jsdelivr", "<script src=\"http"} {
		if strings.Contains(body, bad) {
			t.Errorf("flow page references %q", bad)
		}
	}
}

// TestLayoutFlow_EmptyGraph guards the divide-by-nothing case: a brand-new
// project must render a sane canvas instead of a zero-height SVG.
func TestLayoutFlow_EmptyGraph(t *testing.T) {
	svg := layoutFlow(inspector.BuildFlow(inspector.ProjectModel{}, ""))
	if !svg.Empty {
		t.Errorf("Empty = false, want true for a model with nothing in it")
	}
	if svg.Height <= 0 || svg.Width <= 0 {
		t.Errorf("canvas = %dx%d, want positive dimensions", svg.Width, svg.Height)
	}
	if len(svg.Nodes) != 0 || len(svg.Edges) != 0 {
		t.Errorf("empty graph produced %d nodes / %d edges", len(svg.Nodes), len(svg.Edges))
	}
}

// TestLayoutFlow_NodesStayInsideCanvas is the invariant that keeps the diagram
// from being clipped: every box must fit within the reported canvas.
func TestLayoutFlow_NodesStayInsideCanvas(t *testing.T) {
	dir := flowProjectRoot(t)
	m, err := inspector.Scan(dir)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	svg := layoutFlow(inspector.BuildFlow(m, ""))
	if len(svg.Nodes) == 0 {
		t.Fatal("expected nodes for a wired project")
	}
	for _, n := range svg.Nodes {
		if n.X < 0 || n.Y < 0 || n.X+n.W > svg.Width || n.Y+n.H > svg.Height {
			t.Errorf("node %s at (%d,%d,%dx%d) escapes canvas %dx%d",
				n.ID, n.X, n.Y, n.W, n.H, svg.Width, svg.Height)
		}
	}
}

func TestTruncateLabel(t *testing.T) {
	cases := []struct{ in, want string }{
		{"short", "short"},
		{"exactlyten", "exactlyten"},
		{"waytoolongforthebox", "waytoolon…"},
	}
	for _, c := range cases {
		if got := truncateLabel(c.in, 10); got != c.want {
			t.Errorf("truncateLabel(%q, 10) = %q, want %q", c.in, got, c.want)
		}
	}
	// Multi-byte input must not be cut mid-character.
	if got := truncateLabel("héllo wörld ünïcode", 8); strings.ContainsRune(got, '�') {
		t.Errorf("truncateLabel produced an invalid rune: %q", got)
	}
}
