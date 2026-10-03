package ui

import (
	"encoding/json"
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

func TestServer_FlowJSON(t *testing.T) {
	ts := flowTestServer(t)

	resp, err := http.Get(ts.URL + "/flow.json")
	if err != nil {
		t.Fatalf("GET /flow.json: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var got inspector.FlowExport
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Version != inspector.FlowExportVersion || len(got.Nodes) == 0 {
		t.Errorf("unexpected export: version=%d nodes=%d", got.Version, len(got.Nodes))
	}

	bad, err := http.Get(ts.URL + "/flow.json?aggregate=nope")
	if err != nil {
		t.Fatalf("GET bad filter: %v", err)
	}
	defer bad.Body.Close()
	if bad.StatusCode != http.StatusNotFound {
		t.Errorf("unknown aggregate status = %d, want 404", bad.StatusCode)
	}
}

func TestServer_FlowSource(t *testing.T) {
	ts := flowTestServer(t)

	get := func(q string) (int, string) {
		resp, err := http.Get(ts.URL + "/flow/source?file=" + q)
		if err != nil {
			t.Fatalf("GET %s: %v", q, err)
		}
		defer resp.Body.Close()
		body, _ := readAll(resp.Body)
		return resp.StatusCode, body
	}

	if code, body := get("service/order.go"); code != http.StatusOK || !strings.Contains(body, "OrderService") {
		t.Errorf("referenced file: status=%d body=%q", code, body)
	}
	// Anything a flow node does not point at must be refused, including
	// traversal attempts and files that merely exist in the project.
	for _, bad := range []string{"", "go.mod", "../go.mod", "..%2F..%2Fetc%2Fpasswd", "/etc/passwd", "service/missing.go"} {
		if code, _ := get(bad); code != http.StatusNotFound {
			t.Errorf("file=%q status = %d, want 404", bad, code)
		}
	}

	resp, err := http.Post(ts.URL+"/flow/source?file=service/order.go", "text/plain", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", resp.StatusCode)
	}
}

func TestServer_FlowPageLinksSource(t *testing.T) {
	ts := flowTestServer(t)
	resp, err := http.Get(ts.URL + "/flow")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := readAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(body, `data-file="service/order.go"`) {
		t.Error("command node missing data-file")
	}
	// Monaco is relaxed to inline styles only on /flow; scripts stay 'self'.
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "style-src 'self' 'unsafe-inline'") || strings.Contains(csp, "script-src") {
		t.Errorf("unexpected CSP on /flow: %q", csp)
	}
}

// TestLayoutFlow_Swimlanes: every aggregate is one band across all columns,
// each node sits inside its own band, nodes with no aggregate go to the
// cross-aggregate band on top, and bands never overlap.
func TestLayoutFlow_Swimlanes(t *testing.T) {
	g := inspector.FlowGraph{Columns: []inspector.FlowColumn{
		{Kind: inspector.FlowCommand, Nodes: []inspector.FlowNode{
			{ID: "command:r.Resolve", Aggregate: ""},
			{ID: "command:a.Do", Aggregate: "a"},
		}},
		{Kind: inspector.FlowEvent, Nodes: []inspector.FlowNode{
			{ID: "event:a/One", Aggregate: "a"},
			{ID: "event:a/Two", Aggregate: "a"},
			{ID: "event:b/Three", Aggregate: "b"},
		}},
		{Kind: inspector.FlowStore, Nodes: []inspector.FlowNode{
			{ID: "store:a", Aggregate: "a"},
			{ID: "store:b", Aggregate: "b"},
		}},
	}}
	svg := layoutFlow(g)
	if len(svg.Lanes) != 3 || svg.Lanes[0].Title != crossLane || svg.Lanes[1].Title != "a" || svg.Lanes[2].Title != "b" {
		t.Fatalf("lanes = %+v, want cross, a, b", svg.Lanes)
	}
	lane := map[string]FlowSVGLane{}
	for _, l := range svg.Lanes {
		lane[l.Title] = l
	}
	want := map[string]string{
		"command:r.Resolve": crossLane, "command:a.Do": "a", "event:a/One": "a",
		"event:a/Two": "a", "event:b/Three": "b", "store:a": "a", "store:b": "b",
	}
	for _, n := range svg.Nodes {
		l := lane[want[n.ID]]
		if n.Y < l.Y || n.Y+n.H > l.Y+l.H {
			t.Errorf("node %s (y %d..%d) outside lane %s %+v", n.ID, n.Y, n.Y+n.H, want[n.ID], l)
		}
	}
	for i := 1; i < len(svg.Lanes); i++ {
		if prev := svg.Lanes[i-1]; prev.Y+prev.H >= svg.Lanes[i].Y {
			t.Errorf("lanes overlap: %+v / %+v", prev, svg.Lanes[i])
		}
	}
	if last := svg.Lanes[len(svg.Lanes)-1]; last.Y+last.H > svg.Height {
		t.Errorf("last lane escapes canvas height %d: %+v", svg.Height, last)
	}
}

// TestLayoutFlow_BarycenterOrder: within a band, a method is placed next to
// the event it leads to rather than in label order, so edges do not cross.
func TestLayoutFlow_BarycenterOrder(t *testing.T) {
	g := inspector.FlowGraph{
		Columns: []inspector.FlowColumn{
			{Kind: inspector.FlowCommand, Nodes: []inspector.FlowNode{
				{ID: "command:a.Alpha", Label: "Alpha", Aggregate: "a"},
				{ID: "command:a.Beta", Label: "Beta", Aggregate: "a"},
			}},
			{Kind: inspector.FlowEvent, Nodes: []inspector.FlowNode{
				{ID: "event:a/First", Label: "First", Aggregate: "a"},
				{ID: "event:a/Second", Label: "Second", Aggregate: "a"},
			}},
		},
		Edges: []inspector.FlowEdge{
			{From: "command:a.Alpha", To: "event:a/Second"},
			{From: "command:a.Beta", To: "event:a/First"},
		},
	}
	y := map[string]int{}
	for _, n := range layoutFlow(g).Nodes {
		y[n.ID] = n.Y
	}
	if y["command:a.Beta"] >= y["command:a.Alpha"] {
		t.Errorf("Beta (→ First) should sit above Alpha (→ Second): %v", y)
	}
}

// TestServer_FlowFilters: several ?aggregate= values, the problems/stubs
// switches render, and the gap table links to graph nodes.
func TestServer_FlowFilters(t *testing.T) {
	ts := flowTestServer(t)
	for path, want := range map[string]int{
		"/flow?aggregate=order&aggregate=order": http.StatusOK,
		"/flow?problems=1&stubs=hide":           http.StatusOK,
		"/flow?aggregate=order&aggregate=nope":  http.StatusNotFound,
	} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := readAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s status = %d, want %d", path, resp.StatusCode, want)
		}
		if want == http.StatusOK && strings.Contains(body, "render error") {
			t.Errorf("%s render error: %q", path, body)
		}
	}

	resp, err := http.Get(ts.URL + "/flow?aggregate=order")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := readAll(resp.Body)
	resp.Body.Close()
	for _, want := range []string{`value="order" checked`, `data-from="`, `id="flow-head"`, `class="flow-lane`} {
		if !strings.Contains(body, want) {
			t.Errorf("flow page missing %q", want)
		}
	}
}
