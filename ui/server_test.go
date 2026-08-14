package ui

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ariefsam/esb/inspector"
)

// testFixture re-uses the inspector fixture file layout. We import the
// fixture indirectly via inspector.Scan and only need a directory
// containing a representative go.mod for the scan error tests.
func makeProjectRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module github.com/example/ui-test\n\ngo 1.22\n"), 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func newTestServer(t *testing.T, runner ProcessRunner) *Server {
	t.Helper()
	srv, err := NewServer(Options{ProjectRoot: makeProjectRoot(t), Runner: runner})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return srv
}

func TestServer_Healthz(t *testing.T) {
	srv := newTestServer(t, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", got)
	}
}

func TestServer_HealthzRejectsPost(t *testing.T) {
	srv := newTestServer(t, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/healthz", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}

func TestServer_OverviewRenders(t *testing.T) {
	srv := newTestServer(t, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := readAll(resp.Body)
	t.Logf("body len=%d head=%q", len(body), body[:min(120, len(body))])
	if !strings.Contains(body, "Project Overview") {
		t.Errorf("body missing title: %q", body)
	}
	if !strings.Contains(body, "/static/app.css") {
		t.Errorf("body missing css link: %q", body)
	}
	// The overview body template renders .Title and .Subtitle from the
	// page struct. If a future change drops those fields the template
	// engine returns a render error which is silently inlined as
	// "<!-- render error: ... -->". Asserting the hero subtitle ("N
	// aggregates") makes sure the body executed cleanly.
	if strings.Contains(body, "render error") {
		t.Errorf("body template failed to render: %q", body)
	}
	if !strings.Contains(body, "aggregates") {
		t.Errorf("body missing 'aggregates' subtitle: %q", body)
	}
}

func TestServer_OverviewRejectsPost(t *testing.T) {
	srv := newTestServer(t, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/", "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatalf("POST /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}

func TestServer_StaticAsset(t *testing.T) {
	srv := newTestServer(t, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/static/app.css")
	if err != nil {
		t.Fatalf("GET /static/app.css: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := readAll(resp.Body)
	if !strings.Contains(body, ":root") || !strings.Contains(body, "--bg") {
		t.Errorf("css body missing expected content: %q", body[:min(80, len(body))])
	}
}

func TestServer_CommandsPage(t *testing.T) {
	srv := newTestServer(t, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/commands")
	if err != nil {
		t.Fatalf("GET /commands: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := readAll(resp.Body)
	for _, want := range []string{"Add aggregate", "Add event", "Add projection", "Show project"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
	if strings.Contains(body, "render error") {
		t.Errorf("body template failed to render: %q", body)
	}
}

func TestServer_AggregateNotFound(t *testing.T) {
	srv := newTestServer(t, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/aggregates/missing")
	if err != nil {
		t.Fatalf("GET /aggregates/missing: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// TestServer_AggregatePageRenders builds a tiny aggregate inside the
// test project root, hits /aggregates/<name>, and asserts the page
// rendered without a swallowed template error. This guards against
// future template regressions that silently substitute
// `<!-- render error: ... -->` for a missing field.
func TestServer_AggregatePageRenders(t *testing.T) {
	dir := makeProjectRoot(t)
	if err := os.MkdirAll(filepath.Join(dir, "domain"), 0755); err != nil {
		t.Fatal(err)
	}
	src := `package domain

const OrderAggregateName = "order"

// OrderPlaced event.
type OrderPlaced struct {
	Amount int64
}
`
	if err := os.WriteFile(filepath.Join(dir, "domain", "order.go"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}

	srv, err := NewServer(Options{ProjectRoot: dir})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/aggregates/order")
	if err != nil {
		t.Fatalf("GET /aggregates/order: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := readAll(resp.Body)
	if strings.Contains(body, "render error") {
		t.Errorf("aggregate body template failed to render: %q", body)
	}
	for _, want := range []string{"Aggregate", "Events", "OrderPlaced"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
}

func TestServer_ExecuteRejectsGet(t *testing.T) {
	srv := newTestServer(t, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/commands/execute")
	if err != nil {
		t.Fatalf("GET /commands/execute: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}

func TestServer_ExecuteRejectsUnknownCommand(t *testing.T) {
	srv := newTestServer(t, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	form := url.Values{}
	form.Set("command", "rm-rf")
	resp, err := http.PostForm(ts.URL+"/commands/execute", form)
	if err != nil {
		t.Fatalf("POST /commands/execute: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestServer_ExecuteRejectsExtraField(t *testing.T) {
	srv := newTestServer(t, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	form := url.Values{}
	form.Set("command", "show")
	form.Set("rm", "-rf")
	resp, err := postFormWithOrigin(ts.URL+"/commands/execute", form, ts.URL)
	if err != nil {
		t.Fatalf("POST /commands/execute: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	body, _ := readAll(resp.Body)
	if !strings.Contains(body, "tidak dikenal") {
		t.Errorf("body missing 'tidak dikenal': %q", body)
	}
}

func TestServer_ExecuteRejectsShellMeta(t *testing.T) {
	srv := newTestServer(t, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	form := url.Values{}
	form.Set("command", "add-aggregate")
	form.Set("name", "order;touch /tmp/pwned")
	resp, err := postFormWithOrigin(ts.URL+"/commands/execute", form, ts.URL)
	if err != nil {
		t.Fatalf("POST /commands/execute: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestCheckSameOriginRejectsDifferentScheme(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/commands/execute", nil)
	r.Host = "127.0.0.1:8787"
	r.Header.Set("Origin", "https://127.0.0.1:8787")
	if err := checkSameOrigin(r); err == nil {
		t.Fatal("expected HTTPS origin to be rejected for HTTP request")
	}
}

func TestServer_ExecuteRejectsCrossOrigin(t *testing.T) {
	srv := newTestServer(t, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	form := url.Values{}
	form.Set("command", "show")
	// Origin is a different scheme+host; should be rejected even when
	// every other field is valid.
	resp, err := postFormWithOrigin(ts.URL+"/commands/execute", form, "http://attacker.example")
	if err != nil {
		t.Fatalf("POST /commands/execute: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	body, _ := readAll(resp.Body)
	if !strings.Contains(body, "tidak sama dengan") {
		t.Errorf("body missing cross-origin message: %q", body)
	}
}

func TestServer_ExecuteRejectsMissingOrigin(t *testing.T) {
	srv := newTestServer(t, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	form := url.Values{}
	form.Set("command", "show")
	resp, err := postFormWithOrigin(ts.URL+"/commands/execute", form, "")
	if err != nil {
		t.Fatalf("POST /commands/execute: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	body, _ := readAll(resp.Body)
	if !strings.Contains(body, "origin header wajib") {
		t.Errorf("body missing missing-origin message: %q", body)
	}
}

func TestServer_ExecuteHappyPath(t *testing.T) {
	runner := &fakeRunner{}
	srv := newTestServer(t, runner)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	form := url.Values{}
	form.Set("command", "show")
	resp, err := postFormWithOriginClient(client, ts.URL+"/commands/execute", form, ts.URL)
	if err != nil {
		t.Fatalf("POST /commands/execute: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "/commands/runs/") {
		t.Fatalf("redirect location = %q, want /commands/runs/...", loc)
	}

	runResp, err := http.Get(ts.URL + loc)
	if err != nil {
		t.Fatalf("GET %s: %v", loc, err)
	}
	defer runResp.Body.Close()
	if runResp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", runResp.StatusCode)
	}

	// Wait for run to settle and then re-fetch.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if status, _, ok := srv.RunStore().StatusSnapshot(strings.TrimPrefix(loc, "/commands/runs/")); ok && status != RunRunning {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	r := srv.RunStore().Get(strings.TrimPrefix(loc, "/commands/runs/"))
	if r == nil || r.Status != RunSucceed {
		t.Errorf("run = %+v, want succeeded", r)
	}

	if len(runner.calls) != 1 {
		t.Errorf("runner.calls = %d, want 1", len(runner.calls))
	} else {
		if !equalStrings(runner.calls[0].Argv, []string{"esb", "show"}) {
			t.Errorf("argv passed = %v", runner.calls[0].Argv)
		}
		if runner.calls[0].Dir == "" {
			t.Errorf("runner called with empty Dir")
		}
	}
}

func TestServer_RunNotFound(t *testing.T) {
	srv := newTestServer(t, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/commands/runs/run-99999-deadbeef")
	if err != nil {
		t.Fatalf("GET /commands/runs/...: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestServer_InvalidProject(t *testing.T) {
	dir := t.TempDir() // no go.mod
	srv, err := NewServer(Options{ProjectRoot: dir})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
	body, _ := readAll(resp.Body)
	if !strings.Contains(body, "Bukan proyek ESB") {
		t.Errorf("body missing 'Bukan proyek ESB': %q", body[:min(120, len(body))])
	}
}

func TestServer_NewServerRejectsEmptyRoot(t *testing.T) {
	if _, err := NewServer(Options{}); err == nil {
		t.Fatal("expected error for empty project root")
	}
}

func TestServer_NoCDNURLs(t *testing.T) {
	srv := newTestServer(t, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	pages := []string{"/", "/commands"}
	for _, p := range pages {
		resp, err := http.Get(ts.URL + p)
		if err != nil {
			t.Fatalf("GET %s: %v", p, err)
		}
		body, _ := readAll(resp.Body)
		resp.Body.Close()
		for _, bad := range []string{"cdn.", "googleapis", "unpkg", "jsdelivr"} {
			if strings.Contains(body, bad) {
				t.Errorf("page %s references %q", p, bad)
			}
		}
	}
}

func TestServer_RunDetailStatusPolling(t *testing.T) {
	srv := newTestServer(t, nil)
	// Manually seed a running run.
	srv.runs.active = true
	run := &Run{
		ID:        "run-test-1",
		CommandID: "show",
		Argv:      []string{"esb", "show"},
		Dir:       srv.ProjectRoot(),
		StartedAt: time.Now(),
		Status:    RunRunning,
	}
	srv.runs.runs[run.ID] = run

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/commands/runs/run-test-1")
	if err != nil {
		t.Fatalf("GET /commands/runs/run-test-1: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := readAll(resp.Body)
	if !strings.Contains(body, `data-run-status="running"`) {
		t.Errorf("body missing data-run-status=running: %q", body)
	}

	// Clean up so other tests are not affected.
	srv.runs.mu.Lock()
	srv.runs.active = false
	srv.runs.mu.Unlock()
}

// TestServer_NoRunnerLeak verifies that closing an httptest server
// cleanly stops runStore goroutines. We just make sure the runner
// completes without leaking.
func TestServer_NoRunnerLeak(t *testing.T) {
	runner := &fakeRunner{}
	srv := newTestServer(t, runner)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	form := url.Values{}
	form.Set("command", "show")
	resp, err := postFormWithOriginClient(client, ts.URL+"/commands/execute", form, ts.URL)
	if err != nil {
		t.Fatalf("POST /commands/execute: %v", err)
	}
	loc := resp.Header.Get("Location")
	resp.Body.Close()
	id := strings.TrimPrefix(loc, "/commands/runs/")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if status, _, ok := srv.RunStore().StatusSnapshot(id); ok && status != RunRunning {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("run did not finish in time")
}

// TestParseTemplates_RendersAllRoutes locks in the discipline the
// 82409ae follow-up commit paid for: html/template must parse every
// embedded file up-front, and every body template referenced by
// renderBody must render a synthetic page without silently inlining
// a "<!-- render error: ... -->" marker. If a future contributor
// renames a template file or drops a field from a Page struct, this
// test fails loudly at `go test` time rather than leaving the user
// staring at a blank browser tab.
func TestParseTemplates_RendersAllRoutes(t *testing.T) {
	tmpl, err := parseTemplates()
	if err != nil {
		t.Fatalf("parseTemplates: %v", err)
	}

	// Every template the layout extends or the body switch dispatches
	// to must be present. A missing file fails parseTemplates above,
	// but a silent typo (e.g. overview.html written as overveiw.html)
	// would parse fine while leaving the body branch to fail at
	// render time. Asserting the names here catches that.
	wantNames := []string{
		"layout", "overview", "aggregate_detail",
		"commands", "run_detail", "storage", "migrate", "error",
	}
	for _, name := range wantNames {
		if tmpl.Lookup(name) == nil {
			t.Errorf("template %q not registered", name)
		}
	}

	// Now build a Server against a synthetic project and render each
	// body kind. If a route's template expects a field the page
	// struct no longer provides, renderBody silently substitutes an
	// error comment; assert that doesn't happen.
	srv, err := NewServer(Options{ProjectRoot: makeProjectRoot(t)})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	cases := []struct {
		name string
		page interface{}
	}{
		{"overview", OverviewPage{Kind: PageOverview, Title: "Project Overview", Subtitle: "demo — 0 aggregates", AggregateRows: []AggregateRow{}}},
		{"aggregate_detail", AggregateDetailPage{Kind: PageAggregateDetail, Name: "demo", Events: []string{"DemoEvent"}}},
		{"commands", CommandsPage{Kind: PageCommands, Commands: PublicCommands()}},
		{"run_detail", RunPage{Kind: PageRunDetail, Found: true, Running: false, Run: &Run{ID: "r-1", CommandID: "show", Argv: []string{"esb", "show"}, Dir: "/tmp", Status: RunSucceed, ExitCode: 0}}},
		{"storage", StoragePage{Kind: PageStorage, Mode: "embedded", ModeLabel: "embedded", DSN: "/tmp/demo.db", TotalEvents: 3, HasSQLite: true, CanMigrateToESB: true, Rows: []StorageAggregateRow{{Name: "order", Count: 3}}}},
		{"migrate", MigrateFormPage{Kind: PageMigrate, Direction: "to-esb", DSN: "/tmp/demo.db", ESBURL: "http://esb.internal:8080", TenantID: "demo", ProjectID: "toko"}},
		{"error", ErrorPage{Kind: PageError, Title: "t", Message: "m", Code: 400}},
	}
	for _, tc := range cases {
		body := srv.renderBody(tc.page)
		if strings.Contains(body, "render error") {
			t.Errorf("%s body contained render error: %q", tc.name, body)
		}
		if strings.Contains(body, "render error:") {
			t.Errorf("%s body contained render error: %q", tc.name, body)
		}
	}
}

// TestSecurityHeaders_NoSniff_NoCacheBypass locks in the minimum
// security headers every response should carry. The middleware that
// originally shipped set only Content-Type; a future contributor
// removing X-Content-Type-Options would silently re-enable MIME
// sniffing, and a future caching layer that omits Cache-Control would
// let /commands/runs/<id> leak historical data through shared caches.
func TestSecurityHeaders_NoSniff_NoCacheBypass(t *testing.T) {
	srv := newTestServer(t, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	pages := []struct {
		path   string
		method string
		body   string
	}{
		{"/healthz", http.MethodGet, ""},
		{"/", http.MethodGet, ""},
		{"/commands", http.MethodGet, ""},
		{"/commands/runs/missing", http.MethodGet, ""},
	}
	for _, p := range pages {
		var resp *http.Response
		var err error
		if p.method == http.MethodGet {
			resp, err = http.Get(ts.URL + p.path)
		} else {
			resp, err = http.Post(ts.URL+p.path, "application/x-www-form-urlencoded", strings.NewReader(p.body))
		}
		if err != nil {
			t.Fatalf("%s %s: %v", p.method, p.path, err)
		}
		resp.Body.Close()

		if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s %s: X-Content-Type-Options = %q, want nosniff", p.method, p.path, got)
		}
		if got := resp.Header.Get("Cache-Control"); got == "" {
			t.Errorf("%s %s: Cache-Control missing (got empty)", p.method, p.path)
		}
		if got := resp.Header.Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'self'") {
			t.Errorf("%s %s: CSP = %q, want default-src 'self'", p.method, p.path, got)
		}
		if got := resp.Header.Get("X-Frame-Options"); got != "DENY" {
			t.Errorf("%s %s: X-Frame-Options = %q, want DENY", p.method, p.path, got)
		}
	}
}

// TestUI_RejectsCommandForm_EmptyName is the regression that locks in
// the 350eec9 hardening: a form submission whose required field is
// empty (or whitespace-only, which ParseForm strips) must be
// rejected with 400 and must not start a run. The run store must be
// untouched afterwards.
func TestUI_RejectsCommandForm_EmptyName(t *testing.T) {
	runner := &fakeRunner{}
	srv := newTestServer(t, runner)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	cases := []struct {
		name  string
		field string
		value string
	}{
		{"empty", "name", ""},
		{"whitespace", "name", "   \n   "},
		{"newlines_only", "name", "\n\n"},
	}
	for _, tc := range cases {
		form := url.Values{}
		form.Set("command", "add-aggregate")
		form.Set(tc.field, tc.value)
		resp, err := postFormWithOrigin(ts.URL+"/commands/execute", form, ts.URL)
		if err != nil {
			t.Fatalf("POST /commands/execute (%s): %v", tc.name, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", tc.name, resp.StatusCode)
		}
		if srv.RunStore().Busy() {
			t.Errorf("%s: run store became busy after rejected submission", tc.name)
		}
	}
	if len(runner.calls) != 0 {
		t.Errorf("runner.calls = %d, want 0 (no run should have started)", len(runner.calls))
	}
	// The store should still be empty — no successful runs were
	// registered.
	if got := len(srv.RunStore().runs); got != 0 {
		t.Errorf("runs map len = %d, want 0", got)
	}
}

// TestServer_StoragePageRenders exercises GET /storage against a
// project with .env containing EVENT_STORE_MODE=embedded. Asserts
// the page renders the embedded badge, DSN, and the migrate-to-esb
// button. Without the badge the user cannot tell whether they are
// running locally or against a server.
func TestServer_StoragePageRenders(t *testing.T) {
	dir := makeProjectRoot(t)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(
		"EVENT_STORE_MODE=embedded\nEVENT_STORE_DSN=demo.db\n",
	), 0644); err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(Options{ProjectRoot: dir})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/storage")
	if err != nil {
		t.Fatalf("GET /storage: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := readAll(resp.Body)
	for _, want := range []string{"Storage", "embedded", "demo.db", "Pindah ke Event Sourcing Builder"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q: %q", want, body[:min(200, len(body))])
		}
	}
	if strings.Contains(body, "render error") {
		t.Errorf("storage body contained render error: %q", body)
	}
}

// TestServer_StorageFormPageRenders seeds an .env with ESB
// targets and asserts GET /storage/migrate pre-fills them.
func TestServer_StorageFormPageRenders(t *testing.T) {
	dir := makeProjectRoot(t)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(`
EVENT_STORE_MODE=embedded
EVENT_STORE_DSN=demo.db
ESB_URL=http://esb.internal:8080
TENANT_ID=demo
PROJECT_ID=toko
`), 0644); err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(Options{ProjectRoot: dir})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/storage/migrate?direction=to-esb")
	if err != nil {
		t.Fatalf("GET /storage/migrate: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := readAll(resp.Body)
	for _, want := range []string{"http://esb.internal:8080", "demo", "toko"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing prefill %q", want)
		}
	}
}

// TestServer_StorageFormRequiresDirection asserts the GET handler
// rejects a request without the direction query param.
func TestServer_StorageFormRequiresDirection(t *testing.T) {
	srv := newTestServer(t, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/storage/migrate")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// TestServer_StorageFormRejectsBadURL covers the validator path:
// an injected javascript: URL must not make it through BuildArgv.
func TestServer_StorageFormRejectsBadURL(t *testing.T) {
	runner := &fakeRunner{}
	srv := newTestServer(t, runner)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	form := url.Values{}
	form.Set("direction", "to-esb")
	form.Set("esb_url", "javascript:alert(1)")
	form.Set("tenant_id", "demo")
	form.Set("project_id", "toko")
	resp, err := postFormWithOrigin(ts.URL+"/storage/migrate?direction=to-esb", form, ts.URL)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	body, _ := readAll(resp.Body)
	if !strings.Contains(body, "esb_url") {
		t.Errorf("body missing esb_url mention: %q", body[:min(200, len(body))])
	}
}

// TestServer_StorageFormHappyPath locks in the redirect to
// /commands/runs/<id> when the form passes validation. The run
// store then shows the recorded argv.
func TestServer_StorageFormHappyPath(t *testing.T) {
	runner := &fakeRunner{}
	srv := newTestServer(t, runner)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	form := url.Values{}
	form.Set("direction", "to-esb")
	form.Set("esb_url", "http://esb.internal:8080")
	form.Set("tenant_id", "demo")
	form.Set("project_id", "toko")
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := postFormWithOriginClient(client, ts.URL+"/storage/migrate?direction=to-esb", form, ts.URL)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "/commands/runs/") {
		t.Errorf("redirect = %q, want /commands/runs/...", loc)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if status, _, ok := srv.RunStore().StatusSnapshot(strings.TrimPrefix(loc, "/commands/runs/")); ok && status != RunRunning {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("runner.calls = %d, want 1", len(runner.calls))
	}
	wantArgv := []string{"esb", "migrate", "to-esb", "--source", "app.db", "--esb-url", "http://esb.internal:8080", "--tenant", "demo", "--project", "toko"}
	if !equalStrings(runner.calls[0].Argv, wantArgv) {
		t.Errorf("argv = %v, want %v", runner.calls[0].Argv, wantArgv)
	}
}

// TestBuildStoragePage_ModeRules verifies the migration eligibility
// rules: embedded → to-esb is allowed, esb-server → to-embedded is
// allowed, and a typo'd mode gets nothing.
func TestBuildStoragePage_ModeRules(t *testing.T) {
	cases := []struct {
		name           string
		mode           string
		wantToESB      bool
		wantToEmbedded bool
	}{
		{"embedded", inspector.StorageModeEmbedded, true, false},
		{"esb-server", inspector.StorageModeESBServer, false, true},
		{"unknown", "garbage", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page := buildStoragePage(inspector.ProjectModel{}, inspector.StorageInfo{Mode: tc.mode})
			if page.CanMigrateToESB != tc.wantToESB {
				t.Errorf("CanMigrateToESB = %v, want %v", page.CanMigrateToESB, tc.wantToESB)
			}
			if page.CanMigrateToEmbedded != tc.wantToEmbedded {
				t.Errorf("CanMigrateToEmbedded = %v, want %v", page.CanMigrateToEmbedded, tc.wantToEmbedded)
			}
		})
	}
}

// helpers ------------------------------------------------------------

func readAll(r io.Reader) (string, error) {
	data, err := io.ReadAll(r)
	return string(data), err
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// postFormWithOrigin POSTs a form with an explicit Origin header so the
// same-origin check in handleExecute accepts it. Pass an empty origin
// to simulate a non-browser client that omits the header.
func postFormWithOrigin(url string, form url.Values, origin string) (*http.Response, error) {
	return postFormWithOriginClient(http.DefaultClient, url, form, origin)
}

func postFormWithOriginClient(client *http.Client, url string, form url.Values, origin string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	return client.Do(req)
}

// guard against unused import linter complaint if context shrinks.
var _ = context.Background
