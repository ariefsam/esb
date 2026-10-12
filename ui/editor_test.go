package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func editorServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := flowProjectRoot(t)
	srv, err := NewServer(Options{ProjectRoot: dir})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return srv, dir
}

func postJSON(t *testing.T, srv *Server, path string, body any, origin bool) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	if origin {
		req.Header.Set("Origin", "http://"+req.Host)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("response is not JSON: %v\n%s", err, rec.Body)
	}
	return m
}

func loadFile(t *testing.T, srv *Server, rel string) (code, hash string) {
	t.Helper()
	rec := doGet(t, srv, "/editor/order/file?path="+rel)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET file %s: %d %s", rel, rec.Code, rec.Body)
	}
	m := decode(t, rec)
	return m["code"].(string), m["hash"].(string)
}

func TestEditor_PageListsAggregateFilesAndDiagram(t *testing.T) {
	srv, _ := editorServer(t)
	rec := doGet(t, srv, "/editor/order")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d\n%s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`value="domain/order.go"`,
		`value="service/order.go"`,
		`value="server/handler/order.go"`,
		`value="projection/order_worker.go"`,
		`class="flow-svg"`,
		`/static/editor.js`,
		// Sidebar: the open aggregate is highlighted.
		`href="/editor/order" data-name="order" class="active"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("editor page missing %q", want)
		}
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "style-src 'self' 'unsafe-inline'") {
		t.Errorf("editor CSP = %q, want Monaco's inline styles allowed", csp)
	}
}

func TestEditor_UnknownAggregate404(t *testing.T) {
	srv, _ := editorServer(t)
	if rec := doGet(t, srv, "/editor/nope"); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if rec := doGet(t, srv, "/editor/nope/file?path=domain/order.go"); rec.Code != http.StatusNotFound {
		t.Errorf("file of unknown aggregate: status = %d, want 404", rec.Code)
	}
}

func TestEditor_FileOnlyServesAggregateFiles(t *testing.T) {
	srv, _ := editorServer(t)
	code, hash := loadFile(t, srv, "domain/order.go")
	if !strings.Contains(code, "OrderPlaced") || len(hash) != 64 {
		t.Errorf("file = %q hash %q", code, hash)
	}
	for _, p := range []string{"go.mod", "../go.mod", "/etc/passwd", "domain/../go.mod"} {
		if rec := doGet(t, srv, "/editor/order/file?path="+p); rec.Code != http.StatusNotFound {
			t.Errorf("path %q: status = %d, want 404", p, rec.Code)
		}
	}
}

func TestEditor_SaveFormatsAndWrites(t *testing.T) {
	srv, dir := editorServer(t)
	_, hash := loadFile(t, srv, "domain/order.go")
	src := "package domain\nconst OrderAggregateName=\"order\"\ntype OrderPlaced struct{Amount int64}\n"

	rec := postJSON(t, srv, "/editor/order/save", map[string]string{"path": "domain/order.go", "code": src, "hash": hash}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	m := decode(t, rec)
	onDisk, _ := os.ReadFile(filepath.Join(dir, "domain", "order.go"))
	if string(onDisk) != m["code"] || !strings.Contains(string(onDisk), `const OrderAggregateName = "order"`) {
		t.Errorf("file on disk = %q, want the gofmt'ed source the response returned", onDisk)
	}
	if m["hash"] == hash {
		t.Error("hash did not change after save")
	}

	// The diagram endpoint re-reads the saved code.
	if rec := doGet(t, srv, "/editor/order/diagram"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<svg") {
		t.Errorf("diagram: %d %s", rec.Code, rec.Body)
	}
}

func TestEditor_SaveRejectsStaleInvalidAndCrossOrigin(t *testing.T) {
	srv, dir := editorServer(t)
	code, hash := loadFile(t, srv, "domain/order.go")

	// Invalid Go: 422 with a positioned problem, nothing written.
	rec := postJSON(t, srv, "/editor/order/save", map[string]string{"path": "domain/order.go", "code": "package domain\nfunc {", "hash": hash}, true)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid Go: status = %d, want 422", rec.Code)
	}
	if probs, _ := decode(t, rec)["problems"].([]any); len(probs) == 0 || probs[0].(map[string]any)["line"].(float64) != 2 {
		t.Errorf("problems = %v, want one on line 2", probs)
	}

	// Changed on disk since it was loaded: 409 with the disk version.
	if err := os.WriteFile(filepath.Join(dir, "domain", "order.go"), []byte(code+"\n// edited elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec = postJSON(t, srv, "/editor/order/save", map[string]string{"path": "domain/order.go", "code": code, "hash": hash}, true)
	if rec.Code != http.StatusConflict || decode(t, rec)["stale"] != true {
		t.Errorf("stale save: %d %s, want 409 stale", rec.Code, rec.Body)
	}

	// No Origin header: refused like every other POST in the UI.
	if rec := postJSON(t, srv, "/editor/order/save", map[string]string{"path": "domain/order.go", "code": code, "hash": hash}, false); rec.Code != http.StatusForbidden {
		t.Errorf("no Origin: status = %d, want 403", rec.Code)
	}
	// A file outside the aggregate cannot be written.
	if rec := postJSON(t, srv, "/editor/order/save", map[string]string{"path": "go.mod", "code": "module x\n", "hash": ""}, true); rec.Code != http.StatusNotFound {
		t.Errorf("go.mod save: status = %d, want 404", rec.Code)
	}
	// Save is POST-only.
	if rec := doGet(t, srv, "/editor/order/save"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET save: status = %d, want 405", rec.Code)
	}
}

func TestEditor_SaveWaitsForRunningCommand(t *testing.T) {
	srv, _ := editorServer(t)
	_, hash := loadFile(t, srv, "domain/order.go")
	srv.runs.mu.Lock()
	srv.runs.active = true
	srv.runs.mu.Unlock()

	rec := postJSON(t, srv, "/editor/order/save", map[string]string{"path": "domain/order.go", "code": "package domain\n", "hash": hash}, true)
	if rec.Code != http.StatusConflict {
		t.Errorf("save during a run: status = %d, want 409", rec.Code)
	}
}

func TestResolveProjectFile_SymlinkOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.go")
	if err := os.WriteFile(outside, []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.go")); err != nil {
		t.Skip("symlinks not supported:", err)
	}
	if _, err := resolveProjectFile(root, "link.go"); err == nil {
		t.Error("a symlink leading outside the root resolved")
	}
}

func TestEditor_CheckReportsProblems(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH")
	}
	srv, _ := editorServer(t)
	// The fixture's handler uses service without importing it.
	rec := postJSON(t, srv, "/editor/order/check", map[string]string{}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("check: %d %s", rec.Code, rec.Body)
	}
	m := decode(t, rec)
	probs, _ := m["problems"].([]any)
	if m["ok"] != false || len(probs) == 0 {
		t.Fatalf("check = %v, want problems", m)
	}
	found := false
	for _, p := range probs {
		pm := p.(map[string]any)
		if pm["file"] == "server/handler/order.go" && pm["line"].(float64) > 0 {
			found = true
		}
	}
	if !found {
		t.Errorf("problems = %v, want one positioned in server/handler/order.go", probs)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func TestEditor_CommitAndHistory(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	srv, dir := editorServer(t)
	git(t, dir, "init", "-q")
	git(t, dir, "config", "user.email", "test@example.com")
	git(t, dir, "config", "user.name", "Test")
	git(t, dir, "config", "commit.gpgsign", "false")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "initial")

	original, hash := loadFile(t, srv, "domain/order.go")
	edited := original + "\n// v2\n"
	if rec := postJSON(t, srv, "/editor/order/save", map[string]string{"path": "domain/order.go", "code": edited, "hash": hash}, true); rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	// A change outside the aggregate must stay out of the commit.
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module github.com/example/ui-test\n\ngo 1.23\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if rec := postJSON(t, srv, "/editor/order/commit", map[string]string{"message": "  "}, true); rec.Code != http.StatusBadRequest {
		t.Errorf("empty message: status = %d, want 400", rec.Code)
	}
	rec := postJSON(t, srv, "/editor/order/commit", map[string]string{"message": "order: v2"}, true)
	if rec.Code != http.StatusOK || decode(t, rec)["committed"] != true {
		t.Fatalf("commit: %d %s", rec.Code, rec.Body)
	}
	if st := git(t, dir, "status", "--porcelain"); !strings.Contains(st, "go.mod") || strings.Contains(st, "domain/order.go") {
		t.Errorf("git status after commit = %q, want only go.mod still modified", st)
	}
	rec = postJSON(t, srv, "/editor/order/commit", map[string]string{"message": "again"}, true)
	if rec.Code != http.StatusOK || decode(t, rec)["committed"] != false {
		t.Errorf("commit without changes: %d %s, want committed false", rec.Code, rec.Body)
	}

	rec = doGet(t, srv, "/editor/order/history?path=domain/order.go")
	entries, _ := decode(t, rec)["entries"].([]any)
	if len(entries) != 2 || entries[0].(map[string]any)["subject"] != "order: v2" {
		t.Fatalf("history = %s, want 2 entries, newest first", rec.Body)
	}
	first := entries[1].(map[string]any)["commit"].(string)
	rec = doGet(t, srv, "/editor/order/version?path=domain/order.go&commit="+first)
	if rec.Code != http.StatusOK || decode(t, rec)["code"] != original {
		t.Errorf("version %s: %d %s, want the original source", first, rec.Code, rec.Body)
	}
	if rec := doGet(t, srv, "/editor/order/version?path=domain/order.go&commit=HEAD~1"); rec.Code != http.StatusBadRequest {
		t.Errorf("non-SHA commit: status = %d, want 400", rec.Code)
	}
}

func TestEditor_HistoryWithoutGit(t *testing.T) {
	srv, _ := editorServer(t)
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(srv.projectRoot))
	rec := doGet(t, srv, "/editor/order/history?path=domain/order.go")
	if m := decode(t, rec); m["available"] != false {
		t.Errorf("history outside a repo = %v, want available false", m)
	}
	if rec := postJSON(t, srv, "/editor/order/commit", map[string]string{"message": "x"}, true); rec.Code != http.StatusConflict {
		t.Errorf("commit outside a repo: status = %d, want 409", rec.Code)
	}
}

func TestLayout_SidebarOnEveryPage(t *testing.T) {
	srv, _ := editorServer(t)
	for _, p := range []string{"/", "/flow", "/storage", "/commands"} {
		body := doGet(t, srv, p).Body.String()
		for _, want := range []string{`class="rail"`, `id="nav-panel"`, `href="/editor/order"`, `data-nav-toggle`, `/static/nav.js`} {
			if !strings.Contains(body, want) {
				t.Errorf("%s missing %q", p, want)
			}
		}
	}
}
