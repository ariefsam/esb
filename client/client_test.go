package client_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/ariefsam/esb/client"
)

func generateKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// serve starts a stub ESB server. handler errors are reported through t.
func serve(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func expect(t *testing.T, r *http.Request, method, path string) {
	t.Helper()
	if r.Method != method || r.URL.Path != path {
		t.Errorf("request = %s %s, want %s %s", r.Method, r.URL.Path, method, path)
	}
}

func claims(t *testing.T, r *http.Request) jwt.MapClaims {
	t.Helper()
	raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	tok, _, err := jwt.NewParser().ParseUnverified(raw, jwt.MapClaims{})
	if err != nil {
		t.Errorf("token: %v", err)
		return jwt.MapClaims{}
	}
	return tok.Claims.(jwt.MapClaims)
}

func TestClient_Store(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		expect(t, r, http.MethodPost, "/events/store")
		c := claims(t, r)
		if c["realm"] != "tenant" || c["tenant_id"] != "t1" || c["project_id"] != "p1" || c["iss"] != "app" {
			t.Errorf("claims = %v", c)
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["aggregate_name"] != "order" || body["protected"] == nil {
			t.Errorf("body = %v", body)
		}
		writeJSON(w, http.StatusCreated, client.Event{EventID: "evt-1", Version: 1})
	})

	c := client.NewProjectClient(srv.URL, "t1", "p1", generateKey(t), "app")
	e, err := c.Store(context.Background(), client.StoreRequest{
		AggregateName: "order", AggregateID: "o1", EventName: "Placed",
		Protected: map[string]json.RawMessage{"customer:c1": json.RawMessage(`{"email":"user@example.com"}`)},
	})
	if err != nil || e.EventID != "evt-1" || e.Version != 1 {
		t.Fatalf("Store = %+v, %v", e, err)
	}
}

func TestClient_EventsAndEventsAll(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/events":
			if r.URL.Query().Get("aggregate_id") != "o1" {
				t.Errorf("query = %v", r.URL.Query())
			}
			writeJSON(w, http.StatusOK, []client.Event{{EventID: "e1"}, {EventID: "e2"}})
		case "/events/all":
			if r.URL.Query().Get("aggregate_names") != "order,shipment" {
				t.Errorf("query = %v", r.URL.Query())
			}
			writeJSON(w, http.StatusOK, []client.Event{{EventID: "e3"}})
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	})
	c := client.NewProjectClient(srv.URL, "t1", "p1", generateKey(t), "")
	ctx := context.Background()

	events, err := c.Events(ctx, "o1", "order", 0)
	if err != nil || len(events) != 2 {
		t.Fatalf("Events = %v, %v", events, err)
	}
	events, err = c.EventsAll(ctx, []string{"order", "shipment"}, 0, 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("EventsAll = %v, %v", events, err)
	}
}

func TestClient_Snapshots(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST /snapshots/store":
			writeJSON(w, http.StatusCreated, client.Snapshot{ID: 1, Version: 5})
		case "GET /snapshots":
			writeJSON(w, http.StatusOK, client.Snapshot{ID: 2, Version: 10})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	c := client.NewProjectClient(srv.URL, "t1", "p1", generateKey(t), "")
	ctx := context.Background()

	snap, err := c.StoreSnapshot(ctx, "order", "o1", 5, json.RawMessage(`{"status":"ok"}`))
	if err != nil || snap.Version != 5 {
		t.Fatalf("StoreSnapshot = %+v, %v", snap, err)
	}
	snap, err = c.LatestSnapshot(ctx, "o1", "order")
	if err != nil || snap.Version != 10 {
		t.Fatalf("LatestSnapshot = %+v, %v", snap, err)
	}
}

func TestClient_ServerError(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "version conflict"})
	})
	c := client.NewProjectClient(srv.URL, "t1", "p1", generateKey(t), "")

	_, err := c.Store(context.Background(), client.StoreRequest{AggregateName: "order", AggregateID: "o1", EventName: "Placed"})
	var he *client.HTTPError
	if !errors.As(err, &he) || he.StatusCode != http.StatusConflict || he.Message != "version conflict" {
		t.Fatalf("err = %v", err)
	}
}

func TestClient_Locks(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/locks/acquire":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			switch body["key"] {
			case "order-1":
				if body["owner_token"] == "" || body["ttl_seconds"] != float64(30) {
					t.Errorf("acquire body = %v", body)
				}
				writeJSON(w, http.StatusOK, client.Lock{Key: "order-1", OwnerToken: "tok-A"})
			case "busy":
				writeJSON(w, http.StatusConflict, map[string]string{"error": "lock held by another owner"})
			case "slow":
				writeJSON(w, http.StatusRequestTimeout, map[string]string{"error": "lock busy, wait timed out"})
			}
		case "/locks/release":
			writeJSON(w, http.StatusConflict, map[string]string{"error": "lock held by another owner"})
		case "/locks/refresh":
			writeJSON(w, http.StatusOK, client.Lock{Key: "k", OwnerToken: "tok-A"})
		case "/locks/order-1":
			writeJSON(w, http.StatusOK, map[string]any{"key": "order-1", "owner_token": "tok-A", "expires_at": time.Now().Add(time.Minute), "held": true})
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	})
	c := client.NewProjectClient(srv.URL, "t1", "p1", generateKey(t), "")
	ctx := context.Background()

	lock, err := c.AcquireLock(ctx, client.LockRequest{Key: "order-1", TTL: 30 * time.Second})
	if err != nil || lock.OwnerToken != "tok-A" {
		t.Fatalf("AcquireLock = %+v, %v", lock, err)
	}
	if _, err := c.TryAcquireLock(ctx, "busy", "tok-A", 30*time.Second); !errors.Is(err, client.ErrLockBusy) {
		t.Errorf("busy = %v", err)
	}
	if _, err := c.AcquireLock(ctx, client.LockRequest{Key: "slow", TTL: time.Second, WaitTimeout: 5 * time.Second}); !errors.Is(err, client.ErrLockWaitTimeout) {
		t.Errorf("wait timeout = %v", err)
	}
	if _, err := c.AcquireLock(ctx, client.LockRequest{Key: "k", TTL: 0}); err == nil || !strings.Contains(err.Error(), "ttl") {
		t.Errorf("zero ttl = %v", err)
	}
	if err := c.ReleaseLock(ctx, "k", "tok-B"); !errors.Is(err, client.ErrLockNotHolder) {
		t.Errorf("release wrong owner = %v", err)
	}
	if lock, err := c.RefreshLock(ctx, "k", "tok-A", 30*time.Second); err != nil || lock.OwnerToken != "tok-A" {
		t.Errorf("RefreshLock = %+v, %v", lock, err)
	}
	if lock, held, err := c.GetLock(ctx, "order-1"); err != nil || !held || lock.OwnerToken != "tok-A" {
		t.Errorf("GetLock = %+v, %v, %v", lock, held, err)
	}
}

func TestClient_VaultScopesAndShred(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		expect(t, r, http.MethodPost, "/vault/subjects/shred")
		scopes, _ := claims(t, r)["scopes"].([]any)
		if len(scopes) != 1 || scopes[0] != client.ScopeVaultShred {
			t.Errorf("scopes = %v", scopes)
		}
		var body struct {
			Subjects []string `json:"subjects"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		writeJSON(w, http.StatusOK, map[string][]string{"shredded": body.Subjects})
	})
	base := client.NewProjectClient(srv.URL, "t1", "p1", generateKey(t), "")

	got, err := base.WithScopes(client.ScopeVaultShred).ShredSubjects(context.Background(), "customer:c1", "profile:p1")
	if err != nil || len(got) != 2 {
		t.Fatalf("ShredSubjects = %v, %v", got, err)
	}
}

func TestClient_VaultErrorCode(t *testing.T) {
	if code, ok := client.VaultErrorCode(json.RawMessage(`{"$error":"key_shredded"}`)); !ok || code != "key_shredded" {
		t.Errorf("marker = %q, %v", code, ok)
	}
	if _, ok := client.VaultErrorCode(json.RawMessage(`{"email":"user@example.com"}`)); ok {
		t.Error("plaintext reported as a marker")
	}
}

func TestClient_Secrets(t *testing.T) {
	value := base64.StdEncoding.EncodeToString([]byte("dsn-value"))
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "PUT /vault/secrets/billing/db/dsn":
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if body["value"] != base64.StdEncoding.EncodeToString([]byte("v")) {
				t.Errorf("put body = %v", body)
			}
			switch {
			case r.Header.Get("If-None-Match") == "*":
				writeJSON(w, http.StatusPreconditionFailed, map[string]string{"error": "exists"})
			case r.Header.Get("If-Match") == `"1"`:
				writeJSON(w, http.StatusOK, map[string]any{"ref": "billing/db/dsn", "version": 2})
			default:
				t.Errorf("headers = %v", r.Header)
			}
		case "GET /vault/secrets/billing/db/dsn":
			writeJSON(w, http.StatusOK, map[string]any{"ref": "billing/db/dsn", "version": 2, "value": value})
		case "GET /vault/secrets/missing":
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "secret not found"})
		case "POST /vault/secrets:batchGet":
			writeJSON(w, http.StatusOK, map[string]any{
				"secrets": map[string]any{"billing/db/dsn": map[string]any{"ref": "billing/db/dsn", "value": value}},
				"missing": []string{"nope"},
			})
		case "DELETE /vault/secrets/billing/db/dsn":
			if r.Header.Get("If-Match") != `"2"` {
				t.Errorf("delete If-Match = %q", r.Header.Get("If-Match"))
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	c := client.NewProjectClient(srv.URL, "t1", "p1", generateKey(t), "")
	ctx := context.Background()

	if _, err := c.CreateSecret(ctx, "billing/db/dsn", []byte("v")); !errors.Is(err, client.ErrSecretPrecondition) {
		t.Errorf("CreateSecret on existing = %v", err)
	}
	if s, err := c.PutSecret(ctx, "billing/db/dsn", []byte("v"), 1); err != nil || s.Version != 2 || string(s.Value) != "v" {
		t.Errorf("PutSecret = %+v, %v", s, err)
	}
	if s, err := c.GetSecret(ctx, "billing/db/dsn"); err != nil || string(s.Value) != "dsn-value" {
		t.Errorf("GetSecret = %+v, %v", s, err)
	}
	if _, err := c.GetSecret(ctx, "missing"); !errors.Is(err, client.ErrSecretNotFound) {
		t.Errorf("GetSecret missing = %v", err)
	}
	found, missing, err := c.GetSecrets(ctx, "billing/db/dsn", "nope")
	if err != nil || string(found["billing/db/dsn"].Value) != "dsn-value" || len(missing) != 1 {
		t.Errorf("GetSecrets = %v, %v, %v", found, missing, err)
	}
	if err := c.DeleteSecret(ctx, "billing/db/dsn", 2); err != nil {
		t.Errorf("DeleteSecret = %v", err)
	}
}

func TestClient_AdminSecretAudit(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		expect(t, r, http.MethodGet, "/admin/tenants/t1/projects/p1/vault/audit")
		if claims(t, r)["realm"] != "admin" {
			t.Error("admin realm expected")
		}
		if q := r.URL.Query(); q.Get("ref") != "billing/db/dsn" || q.Get("limit") != "2" {
			t.Errorf("query = %v", q)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"entries":        []client.SecretAuditEntry{{ID: 9, Ref: "billing/db/dsn", Action: "get", Result: "ok", KeyID: "k1"}},
			"next_before_id": 9,
		})
	})
	admin := client.NewAdminClient(srv.URL, generateKey(t), "")

	entries, next, err := admin.SecretAudit(context.Background(), "t1", "p1", "billing/db/dsn", 0, 2)
	if err != nil || len(entries) != 1 || entries[0].KeyID != "k1" || next != 9 {
		t.Fatalf("SecretAudit = %+v, %d, %v", entries, next, err)
	}
}
