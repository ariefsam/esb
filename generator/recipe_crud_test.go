package generator

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestAddCRUD_GeneratedProjectBuildsAndTests generates a CRUD recipe into a
// fresh project and asserts the result both compiles and passes its own
// generated Given-When-Then scenario tests. This is the end-to-end safety net
// for the recipe: a template that produces invalid Go, a broken wiring
// injection, or a scenario that does not hold would all fail here.
func TestAddCRUD_GeneratedProjectBuildsAndTests(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	if err := InitProject("example.com/shop", dir); err != nil {
		t.Fatalf("InitProject() error = %v", err)
	}

	fields, err := ParseFields([]string{"name:string", "price:int64", "in_stock:bool"})
	if err != nil {
		t.Fatalf("ParseFields() error = %v", err)
	}
	if err := AddCRUD("product", fields); err != nil {
		t.Fatalf("AddCRUD() error = %v", err)
	}

	// A second CRUD on the same project must also wire cleanly (shared
	// response.go, actor.go and activity.go generated once, distinct
	// workers/handlers/services).
	custFields, _ := ParseFields([]string{"email:string"})
	if err := AddCRUD("customer", custFields, "email"); err != nil {
		t.Fatalf("AddCRUD(customer) error = %v", err)
	}

	// The activity log is checked against the real worker and SQLite, and
	// through the HTTP handler.
	for path, src := range map[string]string{
		"projection/activity_e2e_test.go":     crudActivityProjectionTest,
		"server/handler/activity_e2e_test.go": crudActivityHandlerTest,
	} {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(path)), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	build := exec.Command("go", "build", "./...")
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("generated CRUD project does not compile: %v\n%s", err, out)
	}

	// The generated scenario tests must pass — they exercise the real
	// Create/Update/Archive/Restore command methods against the in-memory store.
	test := exec.Command("go", "test", "./service/...", "./projection/...", "./server/...")
	test.Dir = dir
	if out, err := test.CombinedOutput(); err != nil {
		t.Fatalf("generated CRUD scenario tests failed: %v\n%s", err, out)
	}

	// response.go must be generated exactly once (shared helper).
	if _, err := os.Stat(filepath.Join(dir, "server", "handler", "response.go")); err != nil {
		t.Fatalf("shared response.go not generated: %v", err)
	}

	// Soft delete, not hard delete: the aggregate carries an Archived flag.
	domainSrc, err := os.ReadFile(filepath.Join(dir, "domain", "product.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(domainSrc), "Archived") {
		t.Error("generated CRUD aggregate has no Archived flag (soft delete)")
	}
	if !strings.Contains(string(domainSrc), "ProductRestored") {
		t.Error("generated CRUD aggregate has no Restored event (undo archive)")
	}

	routesSrc, err := os.ReadFile(filepath.Join(dir, "server", "routes.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"/product/restore", "/product/activities"} {
		if !strings.Contains(string(routesSrc), want) {
			t.Errorf("routes.go has no %s route hint", want)
		}
	}
}

func TestAddCRUD_RejectsReservedAndUnknownRedactFields(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := InitProject("example.com/shop", dir); err != nil {
		t.Fatal(err)
	}

	reserved, _ := ParseFields([]string{"actor_id:string"})
	if err := AddCRUD("product", reserved); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Errorf("AddCRUD with actor_id field: err = %v, want reserved-field error", err)
	}

	fields, _ := ParseFields([]string{"name:string"})
	if err := AddCRUD("product", fields, "email"); err == nil || !strings.Contains(err.Error(), "activity-redact") {
		t.Errorf("AddCRUD redacting unknown field: err = %v, want --activity-redact error", err)
	}
}

const crudActivityProjectionTest = `package projection

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"example.com/shop/domain"
	"example.com/shop/eventstore"
)

func event(id uint, aggregate, aggregateID string, version int64, name string, data any) eventstore.Event {
	b, _ := json.Marshal(data)
	return eventstore.Event{ID: id, AggregateName: aggregate, AggregateID: aggregateID, EventName: name, Version: version, Data: b, TimeMillis: version * 1000}
}

func TestProductActivityLog(t *testing.T) {
	ctx := context.Background()
	db, err := NewProjectionDB(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	w := NewProductProjectionWorker(nil, db)
	user := domain.Actor{ActorID: "user-1", ActorName: "Test User"}
	events := []eventstore.Event{
		event(1, "product", "p-1", 1, "ProductCreated", domain.ProductCreated{Actor: user, Name: "a", Price: 1, InStock: true}),
		event(2, "product", "p-1", 2, "ProductUpdated", domain.ProductUpdated{Name: "b", Price: 1, InStock: true}),
		event(3, "product", "p-1", 3, "ProductArchived", domain.ProductArchived{Actor: user}),
		event(4, "product", "p-1", 4, "ProductRestored", domain.ProductRestored{Actor: user}),
	}
	for _, e := range append(events, events[1]) { // re-applying an event must not log it twice
		if err := w.applyEvent(ctx, e); err != nil {
			t.Fatalf("applyEvent(%s): %v", e.EventName, err)
		}
	}

	rows, total, err := ListProductActivities(ctx, db, "p-1", 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 4 || len(rows) != 4 {
		t.Fatalf("got %d rows, total %d; want 4", len(rows), total)
	}
	want := []struct{ action, actorID, changes string }{
		{"restored", "user-1", ` + "`" + `{"archived":{"from":true,"to":false}}` + "`" + `},
		{"archived", "user-1", ` + "`" + `{"archived":{"from":false,"to":true}}` + "`" + `},
		{"updated", domain.SystemActorID, ` + "`" + `{"name":{"from":"a","to":"b"}}` + "`" + `},
		{"created", "user-1", ` + "`" + `{"in_stock":{"from":null,"to":true},"name":{"from":null,"to":"a"},"price":{"from":null,"to":1}}` + "`" + `},
	}
	for i, wnt := range want {
		r := rows[i]
		if r.Action != wnt.action || r.ActorID != wnt.actorID || string(r.Changes) != wnt.changes {
			t.Errorf("row %d = {%s %s %s}, want {%s %s %s}", i, r.Action, r.ActorID, r.Changes, wnt.action, wnt.actorID, wnt.changes)
		}
	}
	if rows[2].ActorName != domain.SystemActorName {
		t.Errorf("event without actor logged as %q, want %q", rows[2].ActorName, domain.SystemActorName)
	}

	page, total, err := ListRecentProductActivities(ctx, db, 2, 3)
	if err != nil || total != 4 || len(page) != 1 || page[0].Action != "created" {
		t.Errorf("ListRecentProductActivities page 2 = %+v, total %d, err %v; want the created entry, total 4", page, total, err)
	}
}

func TestCustomerActivityRedactsEmail(t *testing.T) {
	ctx := context.Background()
	db, err := NewProjectionDB(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	w := NewCustomerProjectionWorker(nil, db)
	for _, e := range []eventstore.Event{
		event(1, "customer", "c-1", 1, "CustomerCreated", domain.CustomerCreated{Email: "old@example.com"}),
		event(2, "customer", "c-1", 2, "CustomerUpdated", domain.CustomerUpdated{Email: "new@example.com"}),
	} {
		if err := w.applyEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	rows, _, err := ListCustomerActivities(ctx, db, "c-1", 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if got := string(r.Changes); got != ` + "`" + `{"email":{"from":null,"to":null,"redacted":true}}` + "`" + ` {
			t.Errorf("%s changes = %s, want the email redacted", r.Action, got)
		}
	}
}
`

const crudActivityHandlerTest = `package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"example.com/shop/projection"
)

func TestProductQueryHandler_Activities(t *testing.T) {
	db, err := projection.NewProjectionDB(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithContext(context.Background()).Create(&projection.ProductActivityRow{
		AggregateID: "p-1", Version: 1, Action: "created", Changes: []byte("{}"), ActorID: "xxxx", ActorName: "system",
	}).Error; err != nil {
		t.Fatal(err)
	}
	h := NewProductQueryHandler(db)

	rec := httptest.NewRecorder()
	h.Activities(rec, httptest.NewRequest(http.MethodGet, "/product/activities?id=p-1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var body struct {
		Items []projection.ProductActivityRow ` + "`" + `json:"items"` + "`" + `
		Total int64                           ` + "`" + `json:"total"` + "`" + `
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Total != 1 || len(body.Items) != 1 || body.Items[0].ActorName != "system" {
		t.Errorf("body = %s", rec.Body)
	}

	rec = httptest.NewRecorder()
	h.Activities(rec, httptest.NewRequest(http.MethodGet, "/product/activities?page_size=1000", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("page_size=1000: status = %d, want 400", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.Get(rec, httptest.NewRequest(http.MethodGet, "/product/get?id=missing", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("get missing: status = %d, want 404", rec.Code)
	}
}
`
