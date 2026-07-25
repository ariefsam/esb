// Package eventstore provides a typed Go client for the event-sourcing-builder HTTP API.
//
// Each request is authenticated with a short-lived ES256 JWT signed by the
// caller's ECDSA P-256 private key. The matching public key must be registered
// on the server via the PUBLIC_KEYS environment variable.
package eventstore

import (
	"bytes"
	"context"
	"crypto/ecdsa"
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
// It is safe for concurrent use.
type Client struct {
	baseURL    string
	tenantID   string
	projectID  string
	issuer     string
	privateKey *ecdsa.PrivateKey
	httpClient *http.Client
}

// New creates a Client targeting baseURL, signing every request with privateKey.
func New(baseURL, tenantID, projectID, issuer string, privateKey *ecdsa.PrivateKey) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		tenantID:   tenantID,
		projectID:  projectID,
		issuer:     issuer,
		privateKey: privateKey,
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
}

// StoreRequest is the request body for Store.
type StoreRequest struct {
	AggregateName   string          `json:"aggregate_name"`
	AggregateID     string          `json:"aggregate_id"`
	EventName       string          `json:"event_name"`
	Data            json.RawMessage `json:"data,omitempty"`
	ExpectedVersion int64           `json:"expected_version,omitempty"`
	IdempotencyKey  string          `json:"idempotency_key,omitempty"`
	CorrelationID   string          `json:"correlation_id,omitempty"`
	CausationID     string          `json:"causation_id,omitempty"`
}

// Event is the server representation of a stored event.
type Event struct {
	ID             uint            `json:"ID"`
	EventID        string          `json:"EventID"`
	AggregateName  string          `json:"AggregateName"`
	AggregateID    string          `json:"AggregateID"`
	EventName      string          `json:"EventName"`
	Version        int64           `json:"Version"`
	Data           json.RawMessage `json:"Data"`
	TimeMillis     int64           `json:"TimeMillis"`
	CorrelationID  string          `json:"CorrelationID"`
	CausationID    string          `json:"CausationID"`
	IdempotencyKey string          `json:"IdempotencyKey"`
}

// Snapshot is the server representation of an aggregate snapshot.
type Snapshot struct {
	ID            uint            `json:"ID"`
	AggregateName string          `json:"AggregateName"`
	AggregateID   string          `json:"AggregateID"`
	Version       int64           `json:"Version"`
	State         json.RawMessage `json:"State"`
	TimeMillis    int64           `json:"TimeMillis"`
}

// Store appends a new event to the event store.
func (c *Client) Store(ctx context.Context, req StoreRequest) (Event, error) {
	var e Event
	err := c.do(ctx, http.MethodPost, "/events/store", req, &e)
	return e, err
}

// Events retrieves all events for a single aggregate instance.
func (c *Client) Events(ctx context.Context, aggregateID, aggregateName string, afterVersion int64) ([]Event, error) {
	q := url.Values{}
	q.Set("aggregate_id", aggregateID)
	q.Set("aggregate_name", aggregateName)
	q.Set("after_version", strconv.FormatInt(afterVersion, 10))
	var events []Event
	err := c.do(ctx, http.MethodGet, "/events?"+q.Encode(), nil, &events)
	return events, err
}

// EventsAll fetches events across one or more aggregate types with optional long polling.
func (c *Client) EventsAll(ctx context.Context, aggregateNames []string, afterID uint, limit int) ([]Event, error) {
	q := url.Values{}
	if len(aggregateNames) > 0 {
		q.Set("aggregate_names", strings.Join(aggregateNames, ","))
	}
	q.Set("after_id", strconv.FormatUint(uint64(afterID), 10))
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	q.Set("wait_time_millis_if_empty", "30000")
	var events []Event
	err := c.do(ctx, http.MethodGet, "/events/all?"+q.Encode(), nil, &events)
	return events, err
}

// StoreSnapshot persists an aggregate snapshot.
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

// LatestSnapshot retrieves the most recently stored snapshot for an aggregate instance.
func (c *Client) LatestSnapshot(ctx context.Context, aggregateID, aggregateName string) (Snapshot, error) {
	q := url.Values{}
	q.Set("aggregate_id", aggregateID)
	q.Set("aggregate_name", aggregateName)
	var snap Snapshot
	err := c.do(ctx, http.MethodGet, "/snapshots?"+q.Encode(), nil, &snap)
	return snap, err
}

// HTTPError is returned when the server responds with a 4xx or 5xx status.
type HTTPError struct {
	StatusCode int
	Message    string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("server error %d: %s", e.StatusCode, e.Message)
}

// ErrKVNotFound is returned by KVGet when the key does not exist or has expired.
var ErrKVNotFound = errors.New("kv: key not found or expired")

// KVSet stores value under key with the given TTL in seconds.
func (c *Client) KVSet(ctx context.Context, key, value string, ttlSeconds int) error {
	body := map[string]any{"value": value, "ttl_seconds": ttlSeconds}
	return c.do(ctx, http.MethodPut, "/kv/"+url.PathEscape(key), body, nil)
}

// KVGet retrieves the value stored under key.
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

// KVDel deletes the entry stored under key.
func (c *Client) KVDel(ctx context.Context, key string) error {
	return c.do(ctx, http.MethodDelete, "/kv/"+url.PathEscape(key), nil, nil)
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	token, err := c.signedToken()
	if err != nil {
		return fmt.Errorf("sign token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		var errBody map[string]string
		json.NewDecoder(resp.Body).Decode(&errBody) //nolint:errcheck
		return &HTTPError{StatusCode: resp.StatusCode, Message: errBody["error"]}
	}

	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (c *Client) signedToken() (string, error) {
	claims := jwt.MapClaims{
		"sub":        "client",
		"exp":        time.Now().Add(5 * time.Minute).Unix(),
		"iat":        time.Now().Unix(),
		"realm":      "tenant",
		"tenant_id":  c.tenantID,
		"project_id": c.projectID,
	}
	if c.issuer != "" {
		claims["iss"] = c.issuer
	}
	return jwt.NewWithClaims(jwt.SigningMethodES256, claims).SignedString(c.privateKey)
}

// EventRepository is the storage interface for domain events.
type EventRepository interface {
	StoreAtomic(ctx context.Context, e Event, expectedVersion int64) (Event, error)
	Retrieve(ctx context.Context, aggregateID, aggregateName string, afterVersion int64) ([]Event, error)
	FetchAll(ctx context.Context, aggregateNames []string, afterID uint, limit int) ([]Event, error)
}
