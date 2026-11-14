package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ariefsam/esb/client"
)

// stubPubSub speaks the server's /pubsub protocol: SSE frames on
// /pubsub/stream, {messages, cursor, gap} on /pubsub/poll. Cursors are
// "e-<n>"; any other prefix is a cursor from before a restart (gap).
type stubPubSub struct {
	mu       sync.Mutex
	msgs     []client.Message
	notify   chan struct{}
	resumes  []string // Last-Event-ID of each stream request
	polls    int
	perConn  int  // end a stream after this many messages (0 = never)
	jsonOnly bool // answer the stream like a proxy that does not stream
}

func newStubPubSub() *stubPubSub { return &stubPubSub{notify: make(chan struct{})} }

func (s *stubPubSub) publish(topic string, data string) {
	s.mu.Lock()
	s.msgs = append(s.msgs, client.Message{ID: fmt.Sprintf("e-%d", len(s.msgs)+1), Topic: topic, Data: json.RawMessage(data)})
	close(s.notify)
	s.notify = make(chan struct{})
	s.mu.Unlock()
}

// after returns the messages after cursor and whether cursor is unknown.
func (s *stubPubSub) after(cursor string, topics string) ([]client.Message, string, bool, chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, gap := len(s.msgs), false
	if cursor != "" {
		if _, err := fmt.Sscanf(cursor, "e-%d", &n); err != nil {
			n, gap = 0, true
		}
	}
	var out []client.Message
	for _, m := range s.msgs[n:] {
		if topics == "" || strings.Contains(","+topics+",", ","+m.Topic+",") {
			out = append(out, m)
		}
	}
	return out, fmt.Sprintf("e-%d", len(s.msgs)), gap, s.notify
}

func (s *stubPubSub) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c := claims(t, r); c["realm"] != "tenant" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		q := r.URL.Query()
		switch r.URL.Path {
		case "/pubsub/publish":
			var body struct {
				Topic string          `json:"topic"`
				Data  json.RawMessage `json:"data"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			s.publish(body.Topic, string(body.Data))
			s.mu.Lock()
			m := s.msgs[len(s.msgs)-1]
			s.mu.Unlock()
			writeJSON(w, http.StatusAccepted, m)
		case "/pubsub/poll":
			s.mu.Lock()
			s.polls++
			s.mu.Unlock()
			msgs, next, gap, wait := s.after(q.Get("cursor"), q.Get("topics"))
			if len(msgs) == 0 && !gap {
				select {
				case <-wait:
				case <-time.After(time.Second):
				case <-r.Context().Done():
				}
				msgs, next, gap, _ = s.after(next, q.Get("topics"))
			}
			if msgs == nil {
				msgs = []client.Message{}
			}
			writeJSON(w, http.StatusOK, map[string]any{"messages": msgs, "cursor": next, "gap": gap})
		case "/pubsub/stream":
			if s.jsonOnly {
				writeJSON(w, http.StatusOK, map[string]string{})
				return
			}
			cursor := q.Get("cursor")
			if id := r.Header.Get("Last-Event-ID"); id != "" {
				cursor = id
			}
			s.mu.Lock()
			s.resumes = append(s.resumes, r.Header.Get("Last-Event-ID"))
			s.mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flush := func() { http.NewResponseController(w).Flush() }
			if cursor == "" {
				_, cursor, _, _ = s.after("", "")
			}
			fmt.Fprintf(w, "id: %s\nevent: ready\ndata: {}\n\n", cursor)
			flush()
			sent := 0
			for {
				msgs, next, gap, wait := s.after(cursor, q.Get("topics"))
				if gap {
					fmt.Fprintf(w, "event: gap\ndata: {}\n\n")
				}
				for _, m := range msgs {
					b, _ := json.Marshal(m)
					fmt.Fprintf(w, "id: %s\nevent: message\ndata: %s\n\n", m.ID, b)
					if sent++; s.perConn > 0 && sent >= s.perConn {
						flush()
						return // like StreamMaxAge: the client reconnects
					}
				}
				flush()
				cursor = next
				select {
				case <-wait:
				case <-time.After(50 * time.Millisecond):
					fmt.Fprint(w, ": ping\n\n")
					flush()
				case <-r.Context().Done():
					return
				}
			}
		default:
			http.NotFound(w, r)
		}
	}
}

type received struct {
	mu   sync.Mutex
	data []string
}

func (r *received) add(m client.Message) error {
	r.mu.Lock()
	r.data = append(r.data, string(m.Data))
	r.mu.Unlock()
	return nil
}

func (r *received) waitFor(t *testing.T, n int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		got := append([]string(nil), r.data...)
		r.mu.Unlock()
		if len(got) >= n {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("received %d messages, want %d", len(r.data), n)
	return nil
}

func TestClient_Publish(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		expect(t, r, http.MethodPost, "/pubsub/publish")
		var body map[string]json.RawMessage
		json.NewDecoder(r.Body).Decode(&body)
		if string(body["topic"]) != `"cache/invalidate"` || string(body["data"]) != `{"key":"k1"}` {
			t.Errorf("body = %s / %s", body["topic"], body["data"])
		}
		writeJSON(w, http.StatusAccepted, client.Message{ID: "e-1", Topic: "cache/invalidate", Data: body["data"]})
	})
	c := client.NewProjectClient(srv.URL, "t1", "p1", generateKey(t), "")
	m, err := c.Publish(context.Background(), "cache/invalidate", map[string]string{"key": "k1"})
	if err != nil || m.ID != "e-1" {
		t.Fatalf("Publish = %+v, %v", m, err)
	}
	// json.RawMessage is sent as is.
	if _, err := c.Publish(context.Background(), "cache/invalidate", json.RawMessage(`{"key":"k1"}`)); err != nil {
		t.Fatal(err)
	}
}

func TestClient_SubscribeSSEResumes(t *testing.T) {
	stub := newStubPubSub()
	stub.perConn = 2 // every stream ends after 2 messages
	srv := serve(t, stub.handler(t))
	c := client.NewProjectClient(srv.URL, "t1", "p1", generateKey(t), "")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var got received
	done := make(chan error, 1)
	go func() {
		done <- c.Subscribe(ctx, client.SubscribeOptions{Topics: []string{"a"}, Transport: client.TransportSSE}, got.add)
	}()
	time.Sleep(100 * time.Millisecond)
	for i := 0; i < 5; i++ {
		stub.publish("a", fmt.Sprint(i))
		stub.publish("b", `"other"`)
	}
	if data := got.waitFor(t, 5); strings.Join(data, ",") != "0,1,2,3,4" {
		t.Fatalf("received %v, want 0..4 once each in order", data)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Subscribe = %v", err)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.resumes) < 3 || stub.resumes[1] == "" {
		t.Fatalf("reconnects must send Last-Event-ID: %v", stub.resumes)
	}
	if stub.polls != 0 {
		t.Fatalf("SSE transport polled %d times", stub.polls)
	}
}

func TestClient_SubscribeAutoFallsBackToPoll(t *testing.T) {
	stub := newStubPubSub()
	stub.jsonOnly = true
	srv := serve(t, stub.handler(t))
	c := client.NewProjectClient(srv.URL, "t1", "p1", generateKey(t), "")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var got received
	go c.Subscribe(ctx, client.SubscribeOptions{}, got.add)
	time.Sleep(100 * time.Millisecond)
	stub.publish("a", `"x"`)
	stub.publish("a", `"y"`)
	if data := got.waitFor(t, 2); strings.Join(data, ",") != `"x","y"` {
		t.Fatalf("received %v", data)
	}
}

func TestClient_SubscribeGapAndClosed(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "id: e-9\nevent: ready\ndata: {}\n\n")
		fmt.Fprint(w, "event: gap\ndata: {}\n\n")
		fmt.Fprint(w, "id: e-10\nevent: message\ndata: {\"id\":\"e-10\",\"topic\":\"a\",\"data\":1}\n\n")
		fmt.Fprint(w, "event: closed\ndata: {\"reason\":\"project closed\"}\n\n")
	})
	c := client.NewProjectClient(srv.URL, "t1", "p1", generateKey(t), "")
	gaps := 0
	var got received
	err := c.Subscribe(context.Background(), client.SubscribeOptions{Cursor: "old-3", OnGap: func() { gaps++ }}, got.add)
	if !errors.Is(err, client.ErrSubscriptionClosed) {
		t.Fatalf("Subscribe = %v, want ErrSubscriptionClosed", err)
	}
	if gaps != 1 || len(got.data) != 1 {
		t.Fatalf("gaps = %d, messages = %v", gaps, got.data)
	}
}

func TestClient_SubscribeStops(t *testing.T) {
	t.Run("forbidden", func(t *testing.T) {
		srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "forbidden: project is archived", http.StatusForbidden)
		})
		c := client.NewProjectClient(srv.URL, "t1", "p1", generateKey(t), "")
		err := c.Subscribe(context.Background(), client.SubscribeOptions{}, func(client.Message) error { return nil })
		var he *client.HTTPError
		if !errors.As(err, &he) || he.StatusCode != http.StatusForbidden || he.Message != "forbidden: project is archived" {
			t.Fatalf("Subscribe = %v", err)
		}
	})
	t.Run("handler error", func(t *testing.T) {
		stub := newStubPubSub()
		srv := serve(t, stub.handler(t))
		c := client.NewProjectClient(srv.URL, "t1", "p1", generateKey(t), "")
		boom := errors.New("boom")
		done := make(chan error, 1)
		go func() {
			done <- c.Subscribe(context.Background(), client.SubscribeOptions{}, func(client.Message) error { return boom })
		}()
		time.Sleep(100 * time.Millisecond)
		stub.publish("a", "1")
		select {
		case err := <-done:
			if !errors.Is(err, boom) {
				t.Fatalf("Subscribe = %v, want boom", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Subscribe did not return the handler's error")
		}
	})
}
