package velox_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	velox "github.com/jpillora/velox/go"
	"google.golang.org/grpc/test/bufconn"
)

// v3State is a document with one large, stable subtree and one small, churning
// one — the shape the whole design is aimed at.
type v3State struct {
	velox.State
	sync.Mutex
	Counter int               `json:"counter"`
	Stable  map[string]string `json:"stable"`
	Log     []string          `json:"log"`
}

func newV3State() *v3State {
	stable := map[string]string{}
	for i := range 200 {
		stable[fmt.Sprintf("key-%03d", i)] = strings.Repeat("payload", 12)
	}
	s := &v3State{Stable: stable, Log: []string{"a", "b", "c"}}
	s.State.Throttle = velox.MinThrottle
	return s
}

// readUpdates opens a raw SSE stream with the given query and returns up to
// count updates, so the wire format itself can be asserted on. It is bounded by
// a deadline: an SSE stream never ends on its own, so a test that expects more
// updates than the server sends must fail rather than hang.
func readUpdates(t *testing.T, url string, count int) []velox.Update {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })

	updates := make([]velox.Update, 0, count)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() && len(updates) < count {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var update velox.Update
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &update); err != nil {
			t.Fatalf("decode update: %v", err)
		}
		if update.Ping {
			continue
		}
		updates = append(updates, update)
	}
	return updates
}

func TestV3SendsOperationsInsteadOfSnapshots(t *testing.T) {
	state := newV3State()
	server := httptest.NewServer(velox.SyncHandler(state))
	defer server.Close()

	done := make(chan struct{})
	defer close(done)
	go func() {
		// Keep publishing while the reader is attached. Pushes are coalesced, so
		// producing more versions than are read is the only reliable way to be
		// sure the reader sees the number it asks for.
		for i := 1; ; i++ {
			select {
			case <-done:
				return
			case <-time.After(40 * time.Millisecond):
			}
			state.Lock()
			state.Counter = i
			state.Unlock()
			state.Push()
		}
	}()

	updates := readUpdates(t, server.URL+"?p=3", 3)
	if len(updates) < 3 {
		t.Fatalf("received %d updates, want 3", len(updates))
	}

	first := updates[0]
	if first.Proto != 3 {
		t.Fatalf("first update advertised proto %d, want 3", first.Proto)
	}
	if first.Root == "" {
		t.Fatal("first update carried no resume token")
	}
	if len(first.Body) == 0 {
		t.Fatal("first update was not a full snapshot")
	}

	// Every later update must be operations against the previous root, and must
	// be dramatically smaller than the snapshot it replaces.
	for i, update := range updates[1:] {
		if len(update.Ops) == 0 {
			t.Fatalf("update %d sent a full snapshot instead of operations", i+1)
		}
		if update.Base != updates[i].Root {
			t.Fatalf("update %d applies to base %q, want the previous root %q", i+1, update.Base, updates[i].Root)
		}
		if len(update.Ops) > len(first.Body)/10 {
			t.Fatalf("operations were %d bytes against a %d byte snapshot", len(update.Ops), len(first.Body))
		}
	}
}

func TestV3ResumeFromStoredRootAfterReconnect(t *testing.T) {
	state := newV3State()
	server := httptest.NewServer(velox.SyncHandler(state))
	defer server.Close()

	initial := readUpdates(t, server.URL+"?p=3", 1)
	if len(initial) != 1 {
		t.Fatal("no initial update")
	}
	snapshot := initial[0]

	// Advance several versions while nobody is listening, as a reloading page
	// would miss.
	for i := 1; i <= 5; i++ {
		state.Lock()
		state.Counter = i
		state.Unlock()
		state.Push()
		time.Sleep(20 * time.Millisecond)
	}

	// Reconnect the way a client restored from localStorage does: same state id,
	// last known version, and the resume token.
	resumeURL := fmt.Sprintf("%s?p=3&id=%s&v=%d&h=%s", server.URL, snapshot.ID, snapshot.Version, snapshot.Root)
	resumed := readUpdates(t, resumeURL, 1)
	if len(resumed) != 1 {
		t.Fatal("no update after resume")
	}
	if len(resumed[0].Ops) == 0 {
		t.Fatalf("resume sent a %d byte snapshot instead of operations", len(resumed[0].Body))
	}
	if resumed[0].Base != snapshot.Root {
		t.Fatalf("resume based on %q, want the stored root %q", resumed[0].Base, snapshot.Root)
	}
	if len(resumed[0].Ops) > len(snapshot.Body)/10 {
		t.Fatalf("resume operations were %d bytes against a %d byte snapshot", len(resumed[0].Ops), len(snapshot.Body))
	}
}

func TestV3UnknownResumeTokenFallsBackToSnapshot(t *testing.T) {
	state := newV3State()
	server := httptest.NewServer(velox.SyncHandler(state))
	defer server.Close()

	initial := readUpdates(t, server.URL+"?p=3", 1)
	id := initial[0].ID

	url := fmt.Sprintf("%s?p=3&id=%s&v=1&h=%s", server.URL, id, strings.Repeat("ab", 16))
	updates := readUpdates(t, url, 1)
	if len(updates[0].Ops) != 0 {
		t.Fatal("an unknown resume token produced operations")
	}
	if len(updates[0].Body) == 0 {
		t.Fatal("an unknown resume token produced no snapshot")
	}
}

func TestV2ClientStillServedMergePatches(t *testing.T) {
	state := newV3State()
	server := httptest.NewServer(velox.SyncHandler(state))
	defer server.Close()

	done := make(chan struct{})
	defer close(done)
	go func() {
		for i := 1; ; i++ {
			select {
			case <-done:
				return
			case <-time.After(40 * time.Millisecond):
			}
			state.Lock()
			state.Counter = i
			state.Unlock()
			state.Push()
		}
	}()

	// No "p" parameter at all: exactly what an existing client sends.
	updates := readUpdates(t, server.URL, 2)
	if len(updates) < 2 {
		t.Fatalf("received %d updates, want 2", len(updates))
	}
	for i, update := range updates {
		if update.Proto != 0 || update.Root != "" || len(update.Ops) != 0 {
			t.Fatalf("update %d leaked v3 fields to a v2 client: %+v", i, update)
		}
	}
	if !updates[1].Delta {
		t.Fatal("second update to a v2 client was not a merge patch")
	}
	var patch map[string]any
	if err := json.Unmarshal(updates[1].Body, &patch); err != nil {
		t.Fatal(err)
	}
	if _, ok := patch["counter"]; !ok {
		t.Fatalf("merge patch did not carry the changed field: %s", updates[1].Body)
	}
}

// TestV3GoClientStaysInSync drives the real Go client, which now negotiates v3,
// against a server and checks the document it ends up holding.
func TestV3GoClientStaysInSync(t *testing.T) {
	state := newV3State()
	l := bufconn.Listen(1 << 20)
	defer l.Close()
	server := &http.Server{Handler: velox.SyncHandler(state)}
	go server.Serve(l)
	defer server.Close()

	type clientState struct {
		sync.Mutex
		Counter int               `json:"counter"`
		Stable  map[string]string `json:"stable"`
		Log     []string          `json:"log"`
	}
	held := &clientState{}
	client, err := velox.NewClient("http://bufconn/sync", held)
	if err != nil {
		t.Fatal(err)
	}
	client.HTTPClient = bufconnClient(l)

	updated := make(chan struct{}, 64)
	client.OnUpdate = func() { updated <- struct{}{} }
	client.OnError = func(err error) { t.Errorf("client error: %v", err) }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go client.Connect(ctx)

	waitForUpdate := func() {
		select {
		case <-updated:
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for an update")
		}
	}
	waitForUpdate()

	for i := 1; i <= 4; i++ {
		state.Lock()
		state.Counter = i
		state.Log = append(state.Log, fmt.Sprintf("entry-%d", i))
		state.Unlock()
		state.Push()
		waitForUpdate()
	}

	// Drain any coalesced trailing updates before reading.
	time.Sleep(200 * time.Millisecond)
	held.Lock()
	defer held.Unlock()
	if held.Counter != 4 {
		t.Fatalf("client counter = %d, want 4", held.Counter)
	}
	if len(held.Log) != 7 {
		t.Fatalf("client log = %v, want 7 entries", held.Log)
	}
	if len(held.Stable) != 200 {
		t.Fatalf("client stable map has %d keys, want 200", len(held.Stable))
	}
	if client.Version() == 0 {
		t.Fatal("client never recorded a version")
	}
}
