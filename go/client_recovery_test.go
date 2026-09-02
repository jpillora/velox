package velox_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	velox "github.com/jpillora/velox/go"
)

func writeRecoveryUpdate(w http.ResponseWriter, update velox.Update) {
	encoded, _ := json.Marshal(update)
	fmt.Fprintf(w, "data: %s\n\n", encoded)
	w.(http.Flusher).Flush()
}

func waitRecoveryValue(t *testing.T, values <-chan int, want int) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case got := <-values:
			if got == want {
				return
			}
		case <-deadline:
			t.Fatalf("did not receive update value %d", want)
		}
	}
}

// A v2 peer uses only v and id to decide whether a reconnect is current. If a
// malformed delta advanced those fields before failing, the client could never
// repair itself: the peer would send no snapshot on every retry.
func TestClientInvalidDeltaResyncsWithoutResumeMetadata(t *testing.T) {
	var attempts atomic.Int32
	secondRequest := make(chan url.Values, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		attempt := attempts.Add(1)
		if attempt == 1 {
			writeRecoveryUpdate(w, velox.Update{
				ID:      "server-one",
				Version: 1,
				Body:    json.RawMessage(`{"counter":1}`),
			})
			// An array is valid JSON but not an RFC 7386 object patch.
			writeRecoveryUpdate(w, velox.Update{
				Version: 2,
				Delta:   true,
				Body:    json.RawMessage(`[]`),
			})
			return
		}
		secondRequest <- r.URL.Query()
		writeRecoveryUpdate(w, velox.Update{
			ID:      "server-one",
			Version: 3,
			Body:    json.RawMessage(`{"counter":7}`),
		})
		<-r.Context().Done()
	}))
	defer server.Close()

	type held struct {
		sync.Mutex
		Counter int `json:"counter"`
	}
	data := &held{}
	client, err := velox.NewClient(server.URL, data)
	if err != nil {
		t.Fatal(err)
	}
	client.MinRetryDelay = time.Millisecond
	client.MaxRetryDelay = 5 * time.Millisecond
	values := make(chan int, 4)
	client.OnUpdate = func() {
		data.Lock()
		values <- data.Counter
		data.Unlock()
	}
	client.OnError = func(error) {}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.Connect(ctx) }()
	defer func() {
		client.Disconnect()
		<-done
	}()

	select {
	case query := <-secondRequest:
		if query.Get("v") != "" || query.Get("id") != "" || query.Get("h") != "" {
			t.Fatalf("resync request retained failed update metadata: %q", query.Encode())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client never reconnected after invalid delta")
	}
	waitRecoveryValue(t, values, 7)
}

func TestClientClearsDataForEmptyStateUpdate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeRecoveryUpdate(w, velox.Update{
			ID:      "server-one",
			Version: 1,
			Body:    json.RawMessage(`{"counter":9,"name":"old"}`),
		})
		// State publishes JSON null as an omitted body. It is a state update,
		// not a no-op, and must clear caller-visible fields too.
		writeRecoveryUpdate(w, velox.Update{Version: 2})
		<-r.Context().Done()
	}))
	defer server.Close()

	type held struct {
		sync.Mutex
		Counter int    `json:"counter"`
		Name    string `json:"name"`
	}
	data := &held{}
	client, err := velox.NewClient(server.URL, data)
	if err != nil {
		t.Fatal(err)
	}
	values := make(chan int, 4)
	client.OnUpdate = func() {
		data.Lock()
		values <- data.Counter
		data.Unlock()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.Connect(ctx) }()
	defer func() {
		client.Disconnect()
		<-done
	}()

	waitRecoveryValue(t, values, 9)
	waitRecoveryValue(t, values, 0)
	data.Lock()
	defer data.Unlock()
	if data.Name != "" || data.Counter != 0 {
		t.Fatalf("empty update left stale data: %+v", data)
	}
	if got := client.Version(); got != 2 {
		t.Fatalf("client version = %d after clear, want 2", got)
	}
}

func TestClientIgnoresDuplicateV3Patch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeRecoveryUpdate(w, velox.Update{
			ID:      "server-one",
			Version: 1,
			Proto:   velox.ProtoVersion,
			Root:    "root-one",
			Body:    json.RawMessage(`{"counter":1}`),
		})
		patch := velox.Update{
			Version: 2,
			Root:    "root-two",
			Base:    "root-one",
			Ops:     json.RawMessage(`[["s",["counter"],2]]`),
		}
		writeRecoveryUpdate(w, patch)
		writeRecoveryUpdate(w, patch) // a replay after a transient SSE failure
		<-r.Context().Done()
	}))
	defer server.Close()

	type held struct {
		sync.Mutex
		Counter int `json:"counter"`
	}
	data := &held{}
	client, err := velox.NewClient(server.URL, data)
	if err != nil {
		t.Fatal(err)
	}
	values := make(chan int, 4)
	errors := make(chan error, 2)
	client.OnUpdate = func() {
		data.Lock()
		values <- data.Counter
		data.Unlock()
	}
	client.OnError = func(err error) { errors <- err }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.Connect(ctx) }()
	defer func() {
		client.Disconnect()
		<-done
	}()

	waitRecoveryValue(t, values, 1)
	waitRecoveryValue(t, values, 2)
	select {
	case err := <-errors:
		t.Fatalf("duplicate patch triggered recovery: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	data.Lock()
	defer data.Unlock()
	if data.Counter != 2 {
		t.Fatalf("duplicate patch changed counter to %d, want 2", data.Counter)
	}
}

func TestClientRejectsConcurrentConnect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	type held struct{ Counter int }
	client, err := velox.NewClient(server.URL, &held{})
	if err != nil {
		t.Fatal(err)
	}
	connected := make(chan struct{})
	client.OnConnect = func() { close(connected) }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	firstDone := make(chan error, 1)
	go func() { firstDone <- client.Connect(ctx) }()
	select {
	case <-connected:
	case <-time.After(3 * time.Second):
		t.Fatal("first connection was not established")
	}

	if err := client.Connect(context.Background()); err == nil {
		t.Fatal("concurrent Connect succeeded")
	}
	client.Disconnect()
	if err := <-firstDone; err != nil && err != context.Canceled {
		t.Fatalf("first Connect returned %v", err)
	}
}
