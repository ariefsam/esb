package generator

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRecipeHandlers_BodyLimitAndErrorMasking runs generated handler tests
// against a CRUD recipe: an oversized body must get 413 (not be read into
// memory), a 5xx must not echo internal error text to the client, and a 4xx
// must still explain what the client got wrong.
func TestRecipeHandlers_BodyLimitAndErrorMasking(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	if err := InitProject("example.com/shop", dir); err != nil {
		t.Fatalf("InitProject() error = %v", err)
	}
	fields, err := ParseFields([]string{"name:string"})
	if err != nil {
		t.Fatalf("ParseFields() error = %v", err)
	}
	if err := AddCRUD("product", fields); err != nil {
		t.Fatalf("AddCRUD() error = %v", err)
	}

	testFile := filepath.Join(dir, "server", "handler", "hardening_test.go")
	if err := os.WriteFile(testFile, []byte(handlerHardeningTestSrc), 0o644); err != nil {
		t.Fatalf("write generated test: %v", err)
	}
	cmd := exec.Command("go", "test", "-run", "TestHardening", "./server/handler/")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated handler hardening tests failed: %v\n%s", err, out)
	}
}

// TestAddCRUD_UpgradesLegacyResponseHelper covers projects generated before
// decodeJSON existed: their response.go has only writeJSON/writeError, and a
// new recipe's handlers call decodeJSON. The recipe must add the helper
// instead of leaving the project uncompilable.
func TestAddCRUD_UpgradesLegacyResponseHelper(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	if err := InitProject("example.com/shop", dir); err != nil {
		t.Fatalf("InitProject() error = %v", err)
	}
	respPath := filepath.Join(dir, "server", "handler", "response.go")
	if err := os.MkdirAll(filepath.Dir(respPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(respPath, []byte(legacyResponseSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	fields, _ := ParseFields([]string{"name:string"})
	if err := AddCRUD("product", fields); err != nil {
		t.Fatalf("AddCRUD() error = %v", err)
	}

	b, err := os.ReadFile(respPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(b), "func decodeJSON("); got != 1 {
		t.Fatalf("response.go has %d decodeJSON definitions, want 1:\n%s", got, b)
	}
	build := exec.Command("go", "build", "./...")
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("project with legacy response.go does not compile after AddCRUD: %v\n%s", err, out)
	}
}

// legacyResponseSrc is server/handler/response.go as generated before the
// body-limit helper was added.
const legacyResponseSrc = `package handler

import (
	"encoding/json"
	"net/http"
)

// writeJSON writes v as a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes an {"error": "..."} JSON body with the given status code.
func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
`

const handlerHardeningTestSrc = `package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"example.com/shop/eventstore"
	"example.com/shop/service"
)

// brokenRepo fails every load with an error that carries internals.
type brokenRepo struct{ *eventstore.FakeStore }

func (brokenRepo) LatestSnapshot(ctx context.Context, aggregateID, aggregateName string) (eventstore.Snapshot, error) {
	return eventstore.Snapshot{}, errors.New("open /srv/secret/app.db: permission denied")
}

func post(h func(http.ResponseWriter, *http.Request), body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
	return rec
}

func TestHardeningOversizedBodyIs413(t *testing.T) {
	h := NewProductHandler(service.NewProductService(eventstore.NewFakeStore()))
	body := ` + "`" + `{"id":"p1","name":"` + "`" + ` + strings.Repeat("a", 2<<20) + ` + "`" + `"}` + "`" + `
	if rec := post(h.Create, body); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHardeningInternalErrorIsMasked(t *testing.T) {
	h := NewProductHandler(service.NewProductService(brokenRepo{eventstore.NewFakeStore()}))
	rec := post(h.Create, ` + "`" + `{"id":"p1","name":"x"}` + "`" + `)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("500 response leaks internal error: %s", rec.Body.String())
	}
}

func TestHardeningClientErrorKeepsMessage(t *testing.T) {
	h := NewProductHandler(service.NewProductService(eventstore.NewFakeStore()))
	rec := post(h.Create, "{not json")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), http.StatusText(http.StatusBadRequest)) {
		t.Fatalf("400 response lost the decode error detail: %s", rec.Body.String())
	}
}
`
