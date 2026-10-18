// Package client provides a typed Go client for the event-sourcing-builder
// (ESB) HTTP API: events, snapshots, KV, distributed locks, the vault
// (protected data, shredding, secrets) and admin calls.
//
//	go get github.com/ariefsam/esb/client
//
// Each request is authenticated with a short-lived ES256 JWT signed by the
// caller's ECDSA P-256 private key. The matching public key must be
// registered on the ESB server (admin UI /ui/keys or the admin API).
//
// Basic usage (tenant project client):
//
//	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
//	c := client.NewProjectClient("http://localhost:8080", "tenant-1", "project-1", key, "my-issuer")
//
//	// store an event
//	event, err := c.Store(ctx, client.StoreRequest{
//	    AggregateName: "order",
//	    AggregateID:   "ord-001",
//	    EventName:     "OrderPlaced",
//	    Data:          json.RawMessage(`{"amount": 100}`),
//	})
//
//	// long-poll for new events
//	events, err := c.EventsAll(ctx, []string{"order"}, 0, 50)
//
// Admin usage:
//
//	admin := client.NewAdminClient("http://localhost:8080", key, "my-issuer")
//	tenants, err := admin.ListTenants(ctx)
package client

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Client communicates with the event-sourcing-builder HTTP API.
// It is safe for concurrent use. Create one instance per application and reuse it.
type Client struct {
	baseURL    string
	realm      string
	tenantID   string
	projectID  string
	privateKey *ecdsa.PrivateKey
	issuer     string
	scopes     []string
	httpClient *http.Client
}

// Vault scopes. Grant them with [Client.WithScopes].
const (
	ScopeVaultEncrypt = "vault:encrypt" // store events/snapshots carrying Protected
	ScopeVaultDecrypt = "vault:decrypt" // receive Protected in plaintext on reads
	ScopeVaultShred   = "vault:shred"   // call [Client.ShredSubjects]
)

// WithScopes returns a copy of c whose tokens carry the given scopes.
// The server trusts the scopes a token carries, and any holder of the
// private key can request any scope: scopes keep code paths from touching
// personal data by accident, they do not separate credentials.
func (c *Client) WithScopes(scopes ...string) *Client {
	cp := *c
	cp.scopes = append([]string(nil), scopes...)
	return &cp
}

// NewProjectClient connects as a tenant to a specific project.
//
// baseURL is the scheme+host of the event-sourcing-builder server,
// e.g. "http://localhost:8080". A trailing slash is stripped automatically.
//
// tenantID and projectID scope every request to the given tenant and project.
// privateKey must be an ECDSA P-256 key whose corresponding public key is
// trusted by the server (configured via PUBLIC_KEYS).
// issuer sets the "iss" claim in the JWT; pass "" to omit it.
func NewProjectClient(baseURL, tenantID, projectID string, privateKey *ecdsa.PrivateKey, issuer string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		realm:      "tenant",
		tenantID:   tenantID,
		projectID:  projectID,
		privateKey: privateKey,
		issuer:     issuer,
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
}

// NewAdminClient connects as admin (no project scope).
//
// baseURL is the scheme+host of the event-sourcing-builder server,
// e.g. "http://localhost:8080". A trailing slash is stripped automatically.
//
// privateKey must be an ECDSA P-256 key whose corresponding public key is
// trusted by the server (configured via PUBLIC_KEYS).
// issuer sets the "iss" claim in the JWT; pass "" to omit it.
func NewAdminClient(baseURL string, privateKey *ecdsa.PrivateKey, issuer string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		realm:      "admin",
		privateKey: privateKey,
		issuer:     issuer,
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
}

// New is kept for backward compatibility — defaults to tenant realm with no project scope.
//
// Prefer [NewProjectClient] or [NewAdminClient] for new code.
func New(baseURL string, privateKey *ecdsa.PrivateKey) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		realm:      "tenant",
		privateKey: privateKey,
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
}

// CreateOnlyExpectedVersion is an ExpectedVersion sentinel meaning "this
// aggregate must not already have any events" — i.e. this write must be
// the very first event ever stored for AggregateID. Distinct from the
// default ExpectedVersion:0, which means "skip the concurrency check,
// just append" — see StoreRequest.ExpectedVersion.
const CreateOnlyExpectedVersion int64 = -1

// StoreRequest is the request body for [Client.Store].
type StoreRequest struct {
	// AggregateName is the type of the aggregate, e.g. "order". Required.
	AggregateName string `json:"aggregate_name"`

	// AggregateID is the unique identifier of the aggregate instance. Required.
	AggregateID string `json:"aggregate_id"`

	// EventName is the name of the event, e.g. "OrderPlaced". Required.
	EventName string `json:"event_name"`

	// Data is the event payload as raw JSON. Optional; omitted when nil.
	Data json.RawMessage `json:"data,omitempty"`

	// ExpectedVersion enables optimistic concurrency control. When > 0 the
	// server rejects the write with 409 Conflict if the aggregate's current
	// version does not match. Pass 0 (the default) to skip the check —
	// note that 0 does NOT mean "this must be the first event"; it means
	// "don't check at all". To atomically guarantee this write is the
	// first event ever stored for AggregateID (e.g. AggregateID is a
	// user-chosen value like an email address being claimed, where two
	// callers could otherwise race to "create" the same one), pass
	// [CreateOnlyExpectedVersion] instead.
	ExpectedVersion int64 `json:"expected_version,omitempty"`

	// IdempotencyKey is a client-generated UUID that makes the store operation
	// idempotent. Retrying a request with the same key returns the original
	// event without creating a duplicate. Optional.
	IdempotencyKey string `json:"idempotency_key,omitempty"`

	// CorrelationID links this event to a broader business transaction.
	// Typically propagated from an incoming request or command. Optional.
	CorrelationID string `json:"correlation_id,omitempty"`

	// CausationID is the EventID of the event or command that caused this one.
	// Together with CorrelationID it forms the causation chain. Optional.
	CausationID string `json:"causation_id,omitempty"`

	// Protected holds personal data, keyed by subject "type:id" (e.g.
	// "customer:cust-7f3a"). Each value must be a JSON object. The server
	// encrypts it with that subject's key before storing; shredding the
	// subject later makes it unreadable everywhere. Requires
	// [ScopeVaultEncrypt]. Subject IDs are stored in plaintext, so use
	// opaque internal IDs, never emails or phone numbers. Optional.
	Protected map[string]json.RawMessage `json:"protected,omitempty"`
}

// Event is the server representation of a stored event returned by the API.
type Event struct {
	// ID is the auto-increment database row ID. Use it as the afterID cursor
	// in [Client.EventsAll] for pagination.
	ID uint `json:"ID"`

	// EventID is the server-assigned unique identifier (shortid format).
	EventID string `json:"EventID"`

	// AggregateName is the type of the aggregate.
	AggregateName string `json:"AggregateName"`

	// AggregateID is the unique identifier of the aggregate instance.
	AggregateID string `json:"AggregateID"`

	// EventName is the name of the event.
	EventName string `json:"EventName"`

	// Version is the monotonically increasing version number within the
	// aggregate. Version 1 is the first event for a given aggregate instance.
	Version int64 `json:"Version"`

	// Data is the event payload as raw JSON.
	Data json.RawMessage `json:"Data"`

	// TimeMillis is the Unix timestamp in milliseconds at which the event was
	// stored on the server.
	TimeMillis int64 `json:"TimeMillis"`

	// CorrelationID links this event to a broader business transaction.
	CorrelationID string `json:"CorrelationID"`

	// CausationID is the EventID of the event or command that caused this one.
	CausationID string `json:"CausationID"`

	// IdempotencyKey is the client-supplied key, if any was provided on store.
	IdempotencyKey string `json:"IdempotencyKey"`

	// Protected is the decrypted per-subject data. It is only returned to
	// tokens holding [ScopeVaultDecrypt]. A subject that cannot be opened
	// (for example because it was shredded) maps to an error marker; check
	// each value with [VaultErrorCode].
	Protected map[string]json.RawMessage `json:"Protected,omitempty"`
}

// Snapshot is the server representation of an aggregate snapshot.
type Snapshot struct {
	// ID is the auto-increment database row ID.
	ID uint `json:"ID"`

	// AggregateName is the type of the aggregate.
	AggregateName string `json:"AggregateName"`

	// AggregateID is the unique identifier of the aggregate instance.
	AggregateID string `json:"AggregateID"`

	// Version is the aggregate version at the time the snapshot was taken.
	// Replay only events with Version > Snapshot.Version after loading the snapshot.
	Version int64 `json:"Version"`

	// State is the serialised aggregate state as raw JSON.
	State json.RawMessage `json:"State"`

	// TimeMillis is the Unix timestamp in milliseconds at which the snapshot
	// was stored on the server.
	TimeMillis int64 `json:"TimeMillis"`

	// Protected behaves exactly like [Event.Protected].
	Protected map[string]json.RawMessage `json:"Protected,omitempty"`
}

// VaultErrorCode reports whether a Protected value is an error marker
// instead of plaintext, and returns its code: "key_shredded",
// "key_not_found", "decrypt_failed", "vault_unavailable" or "internal_error".
func VaultErrorCode(v json.RawMessage) (string, bool) {
	var marker struct {
		Code string `json:"$error"`
	}
	if json.Unmarshal(v, &marker) != nil || marker.Code == "" {
		return "", false
	}
	return marker.Code, true
}

// Store appends a new event to the event store by calling POST /events/store.
//
// On success it returns the stored [Event] with server-assigned fields
// (ID, EventID, Version, TimeMillis) populated.
//
// Errors:
//   - HTTP 409 — version conflict (ExpectedVersion mismatch).
//   - HTTP 200 — duplicate idempotency key; the original event is returned without error.
//   - HTTP 4xx/5xx — wrapped as a non-nil error containing the status code.
func (c *Client) Store(ctx context.Context, req StoreRequest) (Event, error) {
	var e Event
	err := c.do(ctx, http.MethodPost, "/events/store", req, &e)
	return e, err
}

// Events retrieves all events for a single aggregate instance by calling
// GET /events.
//
// aggregateID and aggregateName together identify the aggregate. afterVersion
// filters out events whose Version is ≤ afterVersion; pass 0 to receive all
// events from the beginning.
//
// Events are returned in ascending version order.
func (c *Client) Events(ctx context.Context, aggregateID, aggregateName string, afterVersion int64) ([]Event, error) {
	q := url.Values{}
	q.Set("aggregate_id", aggregateID)
	q.Set("aggregate_name", aggregateName)
	q.Set("after_version", strconv.FormatInt(afterVersion, 10))
	var events []Event
	err := c.do(ctx, http.MethodGet, "/events?"+q.Encode(), nil, &events)
	return events, err
}

// EventsAll fetches events across one or more aggregate types by calling
// GET /events/all, which supports long polling.
//
// If events are available immediately they are returned at once. Otherwise the
// server holds the request open (up to ~30 s) until at least one matching event
// arrives, then returns. Use the returned slice with [Event.ID] of the last
// element as the next afterID to build a continuous tail loop:
//
//	var afterID uint
//	for {
//	    events, err := c.EventsAll(ctx, []string{"order"}, afterID, 50)
//	    if err != nil { /* handle */ }
//	    for _, e := range events {
//	        process(e)
//	        afterID = e.ID
//	    }
//	}
//
// aggregateNames filters by aggregate type. Pass nil or an empty slice to
// receive events from all aggregate types.
// afterID is the exclusive lower bound on [Event.ID]; pass 0 to start from
// the beginning.
// limit caps the number of events returned in one call; 0 uses the server default.
func (c *Client) EventsAll(ctx context.Context, aggregateNames []string, afterID uint, limit int) ([]Event, error) {
	q := url.Values{}
	if len(aggregateNames) > 0 {
		q.Set("aggregate_names", strings.Join(aggregateNames, ","))
	}
	q.Set("after_id", strconv.FormatUint(uint64(afterID), 10))
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var events []Event
	err := c.do(ctx, http.MethodGet, "/events/all?"+q.Encode(), nil, &events)
	return events, err
}

// StoreSnapshot persists an aggregate snapshot by calling POST /snapshots/store.
//
// Snapshots are stored in a separate database (SNAPSHOT_DB_DSN on the server).
// They are not deduplicated — calling StoreSnapshot multiple times for the same
// aggregate creates multiple rows; [Client.LatestSnapshot] always returns the
// most recent one.
//
// aggregateName and aggregateID identify the aggregate. version should match
// the aggregate's current version so that callers know which events to replay
// on top of the snapshot. state is the serialised aggregate state.
func (c *Client) StoreSnapshot(ctx context.Context, aggregateName, aggregateID string, version int64, state json.RawMessage) (Snapshot, error) {
	body := map[string]any{
		"aggregate_name": aggregateName,
		"aggregate_id":   aggregateID,
		"version":        version,
		"state":          state,
	}
	var snap Snapshot
	err := c.do(ctx, http.MethodPost, "/snapshots/store", body, &snap)
	return snap, err
}

// StoreSnapshotProtected is [Client.StoreSnapshot] plus per-subject
// protected data, sealed exactly like [StoreRequest.Protected]. Requires
// [ScopeVaultEncrypt].
func (c *Client) StoreSnapshotProtected(ctx context.Context, aggregateName, aggregateID string, version int64, state json.RawMessage, protected map[string]json.RawMessage) (Snapshot, error) {
	body := map[string]any{
		"aggregate_name": aggregateName,
		"aggregate_id":   aggregateID,
		"version":        version,
		"state":          state,
		"protected":      protected,
	}
	var snap Snapshot
	err := c.do(ctx, http.MethodPost, "/snapshots/store", body, &snap)
	return snap, err
}

// ShredSubjects permanently destroys the keys of the given subjects
// ("type:id") by calling POST /vault/subjects/shred. Their protected data
// becomes unreadable in every event and snapshot, and the subjects cannot
// be used again. All subjects are shredded in one transaction. Requires
// [ScopeVaultShred]. It returns the canonical subject names shredded.
func (c *Client) ShredSubjects(ctx context.Context, subjects ...string) ([]string, error) {
	var out struct {
		Shredded []string `json:"shredded"`
	}
	err := c.do(ctx, http.MethodPost, "/vault/subjects/shred", map[string]any{"subjects": subjects}, &out)
	return out.Shredded, err
}

// LatestSnapshot retrieves the most recently stored snapshot for an aggregate
// instance by calling GET /snapshots.
//
// Returns an error wrapping HTTP 404 when no snapshot exists yet. To
// reconstruct aggregate state efficiently: load the snapshot, then call
// [Client.Events] with afterVersion set to [Snapshot.Version].
func (c *Client) LatestSnapshot(ctx context.Context, aggregateID, aggregateName string) (Snapshot, error) {
	q := url.Values{}
	q.Set("aggregate_id", aggregateID)
	q.Set("aggregate_name", aggregateName)
	var snap Snapshot
	err := c.do(ctx, http.MethodGet, "/snapshots?"+q.Encode(), nil, &snap)
	return snap, err
}

// HTTPError is returned by Client methods when the server responds with a
// 4xx or 5xx status. Use [errors.As] to inspect the status code:
//
//	var he *client.HTTPError
//	if errors.As(err, &he) && he.StatusCode == 404 { ... }
type HTTPError struct {
	StatusCode int
	Message    string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("server error %d: %s", e.StatusCode, e.Message)
}

// ErrKVNotFound is returned by [Client.KVGet] when the key does not exist or
// has expired. Check with errors.Is(err, client.ErrKVNotFound).
var ErrKVNotFound = errors.New("kv: key not found or expired")

// ErrLockBusy is returned by [Client.AcquireLock] (with WaitTimeout = 0) and
// [Client.TryAcquireLock] when the lock is currently held by a different owner.
// Check with errors.Is(err, client.ErrLockBusy).
var ErrLockBusy = errors.New("lock: held by another owner")

// ErrLockWaitTimeout is returned by [Client.AcquireLock] (with WaitTimeout > 0)
// when the wait deadline expires before the lock becomes free.
// Check with errors.Is(err, client.ErrLockWaitTimeout).
var ErrLockWaitTimeout = errors.New("lock: wait timed out")

// ErrLockNotHolder is returned by [Client.ReleaseLock] and [Client.RefreshLock]
// when the supplied owner_token does not match the current holder, or when the
// lock has already expired. Check with errors.Is(err, client.ErrLockNotHolder).
var ErrLockNotHolder = errors.New("lock: not the holder or expired")

// KVSet stores value under key with the given TTL. The entry becomes invisible
// once ttlSeconds have elapsed. Overwrites any existing entry for the same key.
func (c *Client) KVSet(ctx context.Context, key, value string, ttlSeconds int) error {
	body := map[string]any{"value": value, "ttl_seconds": ttlSeconds}
	return c.do(ctx, http.MethodPut, "/kv/"+url.PathEscape(key), body, nil)
}

// KVGet retrieves the value stored under key. Returns [ErrKVNotFound] if the
// key does not exist or has expired.
func (c *Client) KVGet(ctx context.Context, key string) (string, error) {
	var result struct {
		Value string `json:"value"`
	}
	err := c.do(ctx, http.MethodGet, "/kv/"+url.PathEscape(key), nil, &result)
	if err != nil {
		var he *HTTPError
		if errors.As(err, &he) && he.StatusCode == http.StatusNotFound {
			return "", ErrKVNotFound
		}
		return "", err
	}
	return result.Value, nil
}

// KVDel deletes the entry stored under key. It is a no-op if the key does not
// exist or has already expired.
func (c *Client) KVDel(ctx context.Context, key string) error {
	return c.do(ctx, http.MethodDelete, "/kv/"+url.PathEscape(key), nil, nil)
}

// do executes an authenticated HTTP request and decodes the JSON response into out.
// A fresh ES256 JWT is signed and attached as a Bearer token on every call.
func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	_, err := c.doWithHeaders(ctx, method, path, body, nil, out)
	return err
}

// doWithHeaders is do plus request headers in and response headers out.
func (c *Client) doWithHeaders(ctx context.Context, method, path string, body any, headers map[string]string, out any) (http.Header, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	token, err := c.signedToken()
	if err != nil {
		return nil, fmt.Errorf("sign token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		var errBody map[string]string
		json.NewDecoder(resp.Body).Decode(&errBody)
		return resp.Header, &HTTPError{StatusCode: resp.StatusCode, Message: errBody["error"]}
	}

	if out != nil {
		return resp.Header, json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.Header, nil
}

// signedToken mints a short-lived ES256 JWT valid for 5 minutes.
// It embeds realm, tenant_id, and project_id claims when set on the client.
func (c *Client) signedToken() (string, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		"sub": "client",
		"exp": now.Add(5 * time.Minute).Unix(),
		"iat": now.Unix(),
	}
	if c.issuer != "" {
		claims["iss"] = c.issuer
	}
	if c.realm != "" {
		claims["realm"] = c.realm
	}
	if c.tenantID != "" {
		claims["tenant_id"] = c.tenantID
	}
	if c.projectID != "" {
		claims["project_id"] = c.projectID
	}
	if len(c.scopes) > 0 {
		claims["scopes"] = c.scopes
	}
	return jwt.NewWithClaims(jwt.SigningMethodES256, claims).SignedString(c.privateKey)
}

// Tenant is the server representation of a tenant returned by admin API calls.
type Tenant struct {
	ID        string `json:"ID"`
	Name      string `json:"Name"`
	Active    bool   `json:"Active"`
	CreatedAt int64  `json:"CreatedAt"`
}

// Project is the server representation of a project returned by admin API calls.
type Project struct {
	ID        string `json:"ID"`
	TenantID  string `json:"TenantID"`
	Name      string `json:"Name"`
	Active    bool   `json:"Active"`
	CreatedAt int64  `json:"CreatedAt"`
}

// CreateTenant creates a new tenant by calling POST /admin/tenants.
// Requires an admin-realm client (see [NewAdminClient]).
func (c *Client) CreateTenant(ctx context.Context, id, name string) (Tenant, error) {
	var t Tenant
	err := c.do(ctx, http.MethodPost, "/admin/tenants", map[string]string{"id": id, "name": name}, &t)
	return t, err
}

// ListTenants returns all tenants by calling GET /admin/tenants.
// Requires an admin-realm client (see [NewAdminClient]).
func (c *Client) ListTenants(ctx context.Context) ([]Tenant, error) {
	var ts []Tenant
	err := c.do(ctx, http.MethodGet, "/admin/tenants", nil, &ts)
	return ts, err
}

// CreateProject creates a new project under tenantID by calling
// POST /admin/tenants/{tenantID}/projects.
// Requires an admin-realm client (see [NewAdminClient]).
func (c *Client) CreateProject(ctx context.Context, tenantID, id, name string) (Project, error) {
	var p Project
	err := c.do(ctx, http.MethodPost, "/admin/tenants/"+tenantID+"/projects",
		map[string]string{"id": id, "name": name}, &p)
	return p, err
}

// ListProjects returns all projects for tenantID by calling
// GET /admin/tenants/{tenantID}/projects.
// Requires an admin-realm client (see [NewAdminClient]).
func (c *Client) ListProjects(ctx context.Context, tenantID string) ([]Project, error) {
	var ps []Project
	err := c.do(ctx, http.MethodGet, "/admin/tenants/"+tenantID+"/projects", nil, &ps)
	return ps, err
}

// Lock is the server representation of a mutex lock returned by the API.
type Lock struct {
	Key        string    `json:"key"`
	OwnerToken string    `json:"owner_token"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// LockRequest is the request body for [Client.AcquireLock].
// OwnerToken is mandatory — generate it once per critical section and reuse
// it for release/refresh. If left empty, [Client.AcquireLock] generates a
// fresh cryptographically random token and returns it via the returned Lock.
type LockRequest struct {
	Key         string
	OwnerToken  string
	TTL         time.Duration // 0 is invalid; AcquireLock returns an error.
	WaitTimeout time.Duration // 0 = try-once (returns ErrLockBusy on conflict).
}

// AcquireLock acquires the named lock by calling POST /locks/acquire.
// The lock is held for ttl; when ttl elapses without a Refresh the server
// considers it released and another caller may take it.
//
// If req.OwnerToken is empty a fresh 128-bit random token is generated and
// returned in the resulting Lock.OwnerToken — capture it for the matching
// ReleaseLock/RefreshLock call.
//
// Errors:
//   - [ErrLockBusy] — req.WaitTimeout == 0 and another owner currently holds the lock.
//   - [ErrLockWaitTimeout] — req.WaitTimeout > 0 and the deadline fired before the lock freed.
func (c *Client) AcquireLock(ctx context.Context, req LockRequest) (Lock, error) {
	if req.Key == "" {
		return Lock{}, errors.New("lock: key is required")
	}
	if req.TTL <= 0 {
		return Lock{}, errors.New("lock: ttl must be > 0")
	}
	token := req.OwnerToken
	if token == "" {
		var err error
		token, err = newOwnerToken()
		if err != nil {
			return Lock{}, fmt.Errorf("generate owner token: %w", err)
		}
	}
	body := map[string]any{
		"key":                 req.Key,
		"owner_token":         token,
		"ttl_seconds":         int(req.TTL / time.Second),
		"wait_timeout_millis": int64(req.WaitTimeout / time.Millisecond),
	}
	var out Lock
	err := c.do(ctx, http.MethodPost, "/locks/acquire", body, &out)
	if err != nil {
		return Lock{}, mapLockAcquireError(err)
	}
	return out, nil
}

// TryAcquireLock is a convenience wrapper that calls AcquireLock with
// WaitTimeout = 0. Returns ErrLockBusy instead of waiting when the lock
// is currently held.
func (c *Client) TryAcquireLock(ctx context.Context, key, ownerToken string, ttl time.Duration) (Lock, error) {
	return c.AcquireLock(ctx, LockRequest{
		Key:        key,
		OwnerToken: ownerToken,
		TTL:        ttl,
	})
}

// ReleaseLock releases the lock identified by key if ownerToken matches the
// current holder. Returns [ErrLockNotHolder] if a different owner holds it.
// The server treats release of an already-expired lock as success.
func (c *Client) ReleaseLock(ctx context.Context, key, ownerToken string) error {
	body := map[string]any{"key": key, "owner_token": ownerToken}
	err := c.do(ctx, http.MethodPost, "/locks/release", body, nil)
	if err != nil {
		return mapLockOwnerError(err)
	}
	return nil
}

// RefreshLock extends the lock's TTL by ttl if ownerToken matches the
// current holder. Returns [ErrLockNotHolder] when the lock is held by a
// different owner or has already expired.
func (c *Client) RefreshLock(ctx context.Context, key, ownerToken string, ttl time.Duration) (Lock, error) {
	if ttl <= 0 {
		return Lock{}, errors.New("lock: ttl must be > 0")
	}
	body := map[string]any{
		"key":         key,
		"owner_token": ownerToken,
		"ttl_seconds": int(ttl / time.Second),
	}
	var out Lock
	err := c.do(ctx, http.MethodPost, "/locks/refresh", body, &out)
	if err != nil {
		return Lock{}, mapLockOwnerError(err)
	}
	return out, nil
}

// GetLock inspects the lock for key without requiring ownership. Returns
// the current record and a held flag (false when the row is missing or
// expired). Errors only on transport / DB failures.
func (c *Client) GetLock(ctx context.Context, key string) (Lock, bool, error) {
	var out struct {
		Key        string    `json:"key"`
		OwnerToken string    `json:"owner_token"`
		ExpiresAt  time.Time `json:"expires_at"`
		Held       bool      `json:"held"`
	}
	err := c.do(ctx, http.MethodGet, "/locks/"+url.PathEscape(key), nil, &out)
	if err != nil {
		return Lock{}, false, err
	}
	return Lock{Key: out.Key, OwnerToken: out.OwnerToken, ExpiresAt: out.ExpiresAt}, out.Held, nil
}

// WithLock acquires the lock, runs fn, and releases the lock — even if fn
// returns an error. The owner token is generated locally so callers don't
// need to mint UUIDs themselves. WaitTimeout defaults to ttl so the call
// gives up if the lock remains busy longer than the caller is willing to
// wait; the fn will not run in that case.
func (c *Client) WithLock(ctx context.Context, key string, ttl time.Duration, fn func(context.Context) error) error {
	if fn == nil {
		return errors.New("lock: fn must not be nil")
	}
	token, err := newOwnerToken()
	if err != nil {
		return fmt.Errorf("generate owner token: %w", err)
	}
	lock, err := c.AcquireLock(ctx, LockRequest{
		Key:         key,
		OwnerToken:  token,
		TTL:         ttl,
		WaitTimeout: ttl,
	})
	if err != nil {
		return err
	}
	if err := fn(ctx); err != nil {
		// best-effort release — a TTL expiry will eventually clean up.
		_ = c.ReleaseLock(context.Background(), lock.Key, lock.OwnerToken)
		return err
	}
	return c.ReleaseLock(context.Background(), lock.Key, lock.OwnerToken)
}

// newOwnerToken returns 16 random bytes hex-encoded (32 chars). The server
// treats it as opaque; uniqueness and unpredictability are all we need.
func newOwnerToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// mapLockAcquireError translates HTTPError status codes from the
// /locks/acquire endpoint. 409 here means ErrLockBusy (different holder),
// 408 means ErrLockWaitTimeout.
func mapLockAcquireError(err error) error {
	var he *HTTPError
	if !errors.As(err, &he) {
		return err
	}
	switch he.StatusCode {
	case http.StatusConflict:
		return ErrLockBusy
	case http.StatusRequestTimeout:
		return ErrLockWaitTimeout
	default:
		return err
	}
}

// mapLockOwnerError translates HTTPError status codes from /locks/release
// and /locks/refresh. 409 here means ErrLockNotHolder (wrong owner or
// already expired — the spec treats the two cases identically for the
// caller: re-acquire).
func mapLockOwnerError(err error) error {
	var he *HTTPError
	if !errors.As(err, &he) {
		return err
	}
	switch he.StatusCode {
	case http.StatusConflict:
		return ErrLockNotHolder
	default:
		return err
	}
}

// ── Secrets ──────────────────────────────────────────────────────────────

// Secret is an application secret (a DSN, a private key, a token) stored
// encrypted in the vault of the client's project.
type Secret struct {
	Ref       string `json:"ref"`
	Value     []byte `json:"-"`
	Version   int64  `json:"version"` // also the ETag; pass it to PutSecret/DeleteSecret
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

var (
	// ErrSecretNotFound is returned by GetSecret for a missing ref.
	ErrSecretNotFound = errors.New("secret not found")
	// ErrSecretPrecondition is returned when a conditional write does not
	// hold: CreateSecret on an existing ref, or a stale version.
	ErrSecretPrecondition = errors.New("secret precondition failed")
)

type secretJSON struct {
	Secret
	Value string `json:"value"`
}

func (j secretJSON) decode() (Secret, error) {
	v, err := base64.StdEncoding.DecodeString(j.Value)
	if err != nil {
		return Secret{}, fmt.Errorf("decode secret %s: %w", j.Ref, err)
	}
	s := j.Secret
	s.Value = v
	return s, nil
}

// secretPath escapes each segment of a ref ("billing/db/dsn").
func secretPath(ref string) string {
	segs := strings.Split(ref, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return "/vault/secrets/" + strings.Join(segs, "/")
}

func mapSecretError(err error) error {
	var he *HTTPError
	if errors.As(err, &he) {
		switch he.StatusCode {
		case http.StatusNotFound:
			return ErrSecretNotFound
		case http.StatusPreconditionFailed:
			return ErrSecretPrecondition
		}
	}
	return err
}

// CreateSecret stores value under ref only if ref does not exist yet
// (If-None-Match: *). An existing ref returns [ErrSecretPrecondition].
func (c *Client) CreateSecret(ctx context.Context, ref string, value []byte) (Secret, error) {
	return c.putSecret(ctx, ref, value, map[string]string{"If-None-Match": "*"})
}

// PutSecret stores value under ref, replacing any previous value; nothing
// of the old value is kept. With ifVersion > 0 it replaces only that
// version (If-Match) and returns [ErrSecretPrecondition] otherwise.
func (c *Client) PutSecret(ctx context.Context, ref string, value []byte, ifVersion int64) (Secret, error) {
	var headers map[string]string
	if ifVersion > 0 {
		headers = map[string]string{"If-Match": `"` + strconv.FormatInt(ifVersion, 10) + `"`}
	}
	return c.putSecret(ctx, ref, value, headers)
}

func (c *Client) putSecret(ctx context.Context, ref string, value []byte, headers map[string]string) (Secret, error) {
	var out Secret
	body := map[string]string{"value": base64.StdEncoding.EncodeToString(value)}
	_, err := c.doWithHeaders(ctx, http.MethodPut, secretPath(ref), body, headers, &out)
	if err != nil {
		return Secret{}, mapSecretError(err)
	}
	out.Value = value
	return out, nil
}

// GetSecret returns the secret, or [ErrSecretNotFound].
func (c *Client) GetSecret(ctx context.Context, ref string) (Secret, error) {
	var out secretJSON
	if err := c.do(ctx, http.MethodGet, secretPath(ref), nil, &out); err != nil {
		return Secret{}, mapSecretError(err)
	}
	return out.decode()
}

// GetSecrets fetches up to 100 refs in one call. It returns the secrets
// found, keyed by ref, and the refs that do not exist.
func (c *Client) GetSecrets(ctx context.Context, refs ...string) (map[string]Secret, []string, error) {
	var out struct {
		Secrets map[string]secretJSON `json:"secrets"`
		Missing []string              `json:"missing"`
	}
	if err := c.do(ctx, http.MethodPost, "/vault/secrets:batchGet", map[string]any{"refs": refs}, &out); err != nil {
		return nil, nil, err
	}
	found := make(map[string]Secret, len(out.Secrets))
	for ref, j := range out.Secrets {
		s, err := j.decode()
		if err != nil {
			return nil, nil, err
		}
		found[ref] = s
	}
	return found, out.Missing, nil
}

// DeleteSecret removes ref; no earlier version survives. Deleting a missing
// ref succeeds. With ifVersion > 0 it deletes only that version and returns
// [ErrSecretPrecondition] otherwise.
func (c *Client) DeleteSecret(ctx context.Context, ref string, ifVersion int64) error {
	var headers map[string]string
	if ifVersion > 0 {
		headers = map[string]string{"If-Match": `"` + strconv.FormatInt(ifVersion, 10) + `"`}
	}
	_, err := c.doWithHeaders(ctx, http.MethodDelete, secretPath(ref), nil, headers, nil)
	return mapSecretError(err)
}

// SecretAuditEntry is one recorded access to a secret ref.
type SecretAuditEntry struct {
	ID         uint   `json:"id"`
	Ref        string `json:"ref"`
	Action     string `json:"action"` // get | put | delete
	Result     string `json:"result"` // ok | created | not_found | precondition_failed | error
	KeyID      string `json:"key_id"` // registry ID of the public key the caller used
	TimeMillis int64  `json:"time_millis"`
}

// SecretAudit lists a project's secret audit, newest first (admin client
// only). ref filters when non-empty; pass the returned nextBeforeID as
// beforeID to page back, until it is 0.
func (c *Client) SecretAudit(ctx context.Context, tenantID, projectID, ref string, beforeID uint, limit int) (entries []SecretAuditEntry, nextBeforeID uint, err error) {
	q := url.Values{}
	if ref != "" {
		q.Set("ref", ref)
	}
	if beforeID > 0 {
		q.Set("before_id", strconv.FormatUint(uint64(beforeID), 10))
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out struct {
		Entries      []SecretAuditEntry `json:"entries"`
		NextBeforeID uint               `json:"next_before_id"`
	}
	path := "/admin/tenants/" + url.PathEscape(tenantID) + "/projects/" + url.PathEscape(projectID) + "/vault/audit?" + q.Encode()
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, 0, err
	}
	return out.Entries, out.NextBeforeID, nil
}
