package generator

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// generatedVaultTest runs inside a freshly generated project. It checks
// the vault support end to end where it lives: EnvSecrets, the embedded
// LocalStore and the FakeStore (Protected round trip and shred), and the
// HTTP client's wire shape against a stub ESB server.
const generatedVaultTest = `package eventstore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"

	"github.com/glebarez/sqlite"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"
)

func TestVault_EnvSecrets(t *testing.T) {
	if got := SecretEnvName("billing/db-1/dsn"); got != "SECRET_BILLING_DB_1_DSN" {
		t.Fatalf("SecretEnvName = %q", got)
	}
	t.Setenv("SECRET_BILLING_DB_DSN", "postgres://x")
	s, err := EnvSecrets{}.GetSecret(context.Background(), "billing/db/dsn")
	if err != nil || string(s.Value) != "postgres://x" {
		t.Fatalf("GetSecret = %+v, %v", s, err)
	}
	if _, err := (EnvSecrets{}).GetSecret(context.Background(), "nope"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("missing ref err = %v", err)
	}
	found, missing, err := EnvSecrets{}.GetSecrets(context.Background(), "billing/db/dsn", "nope")
	if err != nil || len(found) != 1 || len(missing) != 1 || missing[0] != "nope" {
		t.Fatalf("GetSecrets = %v %v %v", found, missing, err)
	}
}

type protectedStore interface {
	EventRepository
	Shredder
}

func checkProtected(t *testing.T, store protectedStore) {
	t.Helper()
	ctx := context.Background()
	stored, err := store.StoreAtomic(ctx, Event{
		AggregateName: "order", AggregateID: "o1", EventName: "Placed",
		Data: json.RawMessage("{\"total\":1}"),
		Protected: map[string]json.RawMessage{
			"customer:c1": json.RawMessage("{\"email\":\"user@example.com\"}"),
			"customer:c2": json.RawMessage("{\"name\":\"Other\"}"),
		},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Protected) != 2 {
		t.Fatalf("stored.Protected = %v", stored.Protected)
	}
	if _, err := store.StoreAtomic(ctx, Event{AggregateName: "order", AggregateID: "o2", EventName: "Placed", Data: json.RawMessage("{}")}, 0); err != nil {
		t.Fatal(err)
	}

	got, err := store.Retrieve(ctx, "o1", "order", 0)
	if err != nil || len(got) != 1 || string(got[0].Protected["customer:c1"]) != "{\"email\":\"user@example.com\"}" {
		t.Fatalf("Retrieve = %+v, %v", got, err)
	}
	all, err := store.FetchAll(ctx, nil, 0, 10)
	if err != nil || len(all) != 2 || len(all[0].Protected) != 2 || all[1].Protected != nil {
		t.Fatalf("FetchAll = %+v, %v", all, err)
	}

	if _, err := store.ShredSubjects(ctx, "customer:c1"); err != nil {
		t.Fatal(err)
	}
	got, _ = store.Retrieve(ctx, "o1", "order", 0)
	if _, ok := got[0].Protected["customer:c1"]; ok {
		t.Fatal("shredded subject still readable")
	}
	if _, ok := got[0].Protected["customer:c2"]; !ok {
		t.Fatal("other subject lost")
	}
	if string(got[0].Data) != "{\"total\":1}" {
		t.Fatalf("Data changed by shred: %s", got[0].Data)
	}
}

func TestVault_LocalStoreProtectedAndShred(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "es.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateLocalStore(db); err != nil {
		t.Fatal(err)
	}
	checkProtected(t, NewLocalStore(db))
}

func TestVault_FakeStoreProtectedAndShred(t *testing.T) {
	checkProtected(t, NewFakeStore())
}

func TestVault_ClientWireShape(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	TokenScopes = []string{ScopeVaultEncrypt, ScopeVaultDecrypt}
	t.Cleanup(func() { TokenScopes = nil })

	secret := base64.StdEncoding.EncodeToString([]byte("dsn-value"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, _, err := jwt.NewParser().ParseUnverified(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), jwt.MapClaims{})
		if err != nil {
			t.Errorf("bad token: %v", err)
		}
		if scopes, _ := tok.Claims.(jwt.MapClaims)["scopes"].([]any); len(scopes) != 2 {
			t.Errorf("scopes claim = %v", tok.Claims.(jwt.MapClaims)["scopes"])
		}
		body, _ := io.ReadAll(r.Body)
		switch r.Method + " " + r.URL.Path {
		case "POST /events/store":
			if !strings.Contains(string(body), "\"protected\":{\"customer:c1\"") {
				t.Errorf("store body without protected: %s", body)
			}
			io.WriteString(w, "{\"ID\":1,\"Version\":1}")
		case "POST /vault/subjects/shred":
			io.WriteString(w, "{\"shredded\":[\"customer:c1\"]}")
		case "PUT /vault/secrets/billing/db/dsn":
			if r.Header.Get("If-None-Match") == "*" {
				w.WriteHeader(http.StatusPreconditionFailed)
				io.WriteString(w, "{\"error\":\"exists\"}")
				return
			}
			if r.Header.Get("If-Match") != "\"1\"" {
				t.Errorf("If-Match = %q", r.Header.Get("If-Match"))
			}
			io.WriteString(w, "{\"ref\":\"billing/db/dsn\",\"version\":2}")
		case "GET /vault/secrets/billing/db/dsn":
			io.WriteString(w, "{\"ref\":\"billing/db/dsn\",\"version\":2,\"value\":\""+secret+"\"}")
		case "GET /vault/secrets/missing":
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, "{\"error\":\"secret not found\"}")
		case "POST /vault/secrets:batchGet":
			io.WriteString(w, "{\"secrets\":{\"billing/db/dsn\":{\"ref\":\"billing/db/dsn\",\"value\":\""+secret+"\"}},\"missing\":[\"x\"]}")
		case "DELETE /vault/secrets/billing/db/dsn":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "t1", "p1", "", key)
	ctx := context.Background()
	if _, err := c.Store(ctx, StoreRequest{AggregateName: "order", AggregateID: "o1", EventName: "Placed",
		Protected: map[string]json.RawMessage{"customer:c1": json.RawMessage("{}")}}); err != nil {
		t.Fatal(err)
	}
	if got, err := c.ShredSubjects(ctx, "customer:c1"); err != nil || len(got) != 1 {
		t.Fatalf("ShredSubjects = %v, %v", got, err)
	}
	if _, err := c.CreateSecret(ctx, "billing/db/dsn", []byte("v")); !errors.Is(err, ErrSecretPrecondition) {
		t.Fatalf("CreateSecret on existing = %v", err)
	}
	if s, err := c.PutSecret(ctx, "billing/db/dsn", []byte("v"), 1); err != nil || s.Version != 2 {
		t.Fatalf("PutSecret = %+v, %v", s, err)
	}
	var reader SecretReader = c
	if s, err := reader.GetSecret(ctx, "billing/db/dsn"); err != nil || string(s.Value) != "dsn-value" {
		t.Fatalf("GetSecret = %+v, %v", s, err)
	}
	if _, err := reader.GetSecret(ctx, "missing"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("GetSecret missing = %v", err)
	}
	found, missing, err := reader.GetSecrets(ctx, "billing/db/dsn", "x")
	if err != nil || string(found["billing/db/dsn"].Value) != "dsn-value" || len(missing) != 1 {
		t.Fatalf("GetSecrets = %v %v %v", found, missing, err)
	}
	if err := c.DeleteSecret(ctx, "billing/db/dsn", 0); err != nil {
		t.Fatal(err)
	}
}

func TestVault_ErrorCode(t *testing.T) {
	if code, ok := VaultErrorCode(json.RawMessage("{\"$error\":\"key_shredded\"}")); !ok || code != "key_shredded" {
		t.Fatalf("VaultErrorCode = %q %v", code, ok)
	}
	if _, ok := VaultErrorCode(json.RawMessage("{\"email\":\"x\"}")); ok {
		t.Fatal("plaintext reported as marker")
	}
}
`

// TestVaultSupport_GeneratedProject generates a project and runs vault
// tests inside it, so the templates are checked as compiled, running code.
func TestVaultSupport_GeneratedProject(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := InitProject("example.com/shop", dir); err != nil {
		t.Fatalf("InitProject() error = %v", err)
	}

	wire := readGenerated(t, dir, "wire", "wire.go")
	for _, want := range []string{
		"Secrets domain.SecretReader",
		"Shredder domain.Shredder",
		`eventstore.TokenScopes = strings.Fields(env.ESBScopes)`,
		"var secrets domain.SecretReader = eventstore.EnvSecrets{}",
	} {
		if !strings.Contains(wire, want) {
			t.Errorf("wire.go lacks %q", want)
		}
	}
	if env := readGenerated(t, dir, ".env.example"); !strings.Contains(env, "ESB_SCOPES=") || !strings.Contains(env, "SECRET_BILLING_DB_DSN") {
		t.Error(".env.example does not document ESB_SCOPES and SECRET_*")
	}

	if err := os.WriteFile(filepath.Join(dir, "eventstore", "vault_generated_test.go"), []byte(generatedVaultTest), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"build", "./..."}, {"vet", "./..."}, {"test", "./eventstore/", "-run", "TestVault_", "-count=1"}} {
		cmd := exec.Command("go", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
}
