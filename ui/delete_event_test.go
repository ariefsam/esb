package ui

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func doGet(t *testing.T, srv *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func doPostForm(t *testing.T, srv *Server, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://"+req.Host) // same-origin, as the browser sends
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// projectWithOrderEvent builds a minimal project the inspector recognizes: an
// "order" aggregate with an "OrderPlaced" event (detected via its doc comment).
func projectWithOrderEvent(t *testing.T) string {
	t.Helper()
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
	return dir
}

func seedOrderEvents(t *testing.T, dir string, event string, n int) {
	t.Helper()
	dsn := filepath.Join(dir, "app.db")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS events (aggregate_name TEXT, aggregate_id TEXT, event_name TEXT, version INTEGER, data BLOB)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, err := db.Exec(`INSERT INTO events VALUES ('order','o1',?,?, '{}')`, event, i+1); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("EVENT_STORE_MODE=embedded\nDB_DSN=app.db\n"), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteEvent_ConfirmPageRenders(t *testing.T) {
	srv, err := NewServer(Options{ProjectRoot: projectWithOrderEvent(t)})
	if err != nil {
		t.Fatal(err)
	}
	rec := doGet(t, srv, "/aggregates/order/events/OrderPlaced/delete")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Cek data tersimpan", "OrderPlaced", "Konfirmasi hapus"} {
		if !strings.Contains(body, want) {
			t.Errorf("confirm page missing %q", want)
		}
	}
}

func TestDeleteEvent_UnknownEvent404(t *testing.T) {
	srv, err := NewServer(Options{ProjectRoot: projectWithOrderEvent(t)})
	if err != nil {
		t.Fatal(err)
	}
	rec := doGet(t, srv, "/aggregates/order/events/Ghost/delete")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for unknown event", rec.Code)
	}
}

// The hidden delete-event command must not be runnable via the generic execute
// endpoint — otherwise the stored-data check could be bypassed.
func TestDeleteEvent_HiddenNotRunnableViaExecute(t *testing.T) {
	srv := newTestServer(t, &fakeRunner{})
	form := url.Values{"command": {"delete-event"}, "aggregate": {"order"}, "event": {"OrderPlaced"}}
	rec := doPostForm(t, srv, "/commands/execute", form)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (hidden command rejected)", rec.Code)
	}
}

func TestDeleteEvent_CountGateRequiresAck(t *testing.T) {
	dir := projectWithOrderEvent(t)
	seedOrderEvents(t, dir, "OrderPlaced", 3)
	runner := &fakeRunner{}
	srv, err := NewServer(Options{ProjectRoot: dir, Runner: runner})
	if err != nil {
		t.Fatal(err)
	}

	// The confirm page warns and requires a checkbox.
	page := doGet(t, srv, "/aggregates/order/events/OrderPlaced/delete").Body.String()
	if !strings.Contains(page, `name="ack"`) {
		t.Error("confirm page must require an ack checkbox when events are stored")
	}

	// POST without ack is rejected and starts no run.
	rec := doPostForm(t, srv, "/aggregates/order/events/OrderPlaced/delete", url.Values{})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST without ack = %d, want 400", rec.Code)
	}

	// POST with ack starts the run (303 redirect to the run detail).
	rec = doPostForm(t, srv, "/aggregates/order/events/OrderPlaced/delete", url.Values{"ack": {"on"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST with ack = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/commands/runs/") {
		t.Errorf("redirect = %q, want /commands/runs/...", loc)
	}
}

func TestDeleteEvent_ArgvUsesFileName(t *testing.T) {
	got, err := BuildArgv("delete-event", FormInput{"aggregate": {"bank_account"}, "event": {"AccountOpened"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"esb", "delete", "event", "bank_account", "AccountOpened"}
	if !equalStrings(got, want) {
		t.Errorf("argv = %v, want %v", got, want)
	}
}
