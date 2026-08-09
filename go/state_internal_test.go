package velox

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestRefreshByteIdenticalStateLeavesCacheUntouched(t *testing.T) {
	initial := []byte(`{"name":"unchanged","nested":{"value":1}}`)
	s := directRefreshState(append([]byte(nil), initial...))
	s.data.bytes = append([]byte(nil), initial...)
	s.data.delta = []byte(`{"existing":true}`)
	s.data.version = 7
	if _, err := s.data.patcher.patch(initial); err != nil {
		t.Fatal(err)
	}

	bytesPointer := reflect.ValueOf(s.data.bytes).Pointer()
	deltaPointer := reflect.ValueOf(s.data.delta).Pointer()
	prevCache := s.data.patcher.prev
	prevSnapshot := patcherSnapshot(t, prevCache)

	if _, err := s.refresh(); err != nil {
		t.Fatal(err)
	}

	if got := reflect.ValueOf(s.data.bytes).Pointer(); got != bytesPointer {
		t.Fatalf("cached bytes were replaced: got pointer %x, want %x", got, bytesPointer)
	}
	if got := reflect.ValueOf(s.data.delta).Pointer(); got != deltaPointer {
		t.Fatalf("delta was replaced: got pointer %x, want %x", got, deltaPointer)
	}
	if s.data.version != 7 {
		t.Fatalf("version = %d, want 7", s.data.version)
	}
	prevPointer := reflect.ValueOf(prevCache).Pointer()
	if got := reflect.ValueOf(s.data.patcher.prev).Pointer(); got != prevPointer {
		t.Fatalf("patcher cache was replaced: got pointer %x, want %x", got, prevPointer)
	}
	if got := patcherSnapshot(t, s.data.patcher.prev); !bytes.Equal(got, prevSnapshot) {
		t.Fatalf("patcher cache changed: got %s, want %s", got, prevSnapshot)
	}
}

func TestRefreshChangedStateUpdatesDataAndCache(t *testing.T) {
	initial := []byte(`{"keep":1,"value":"before"}`)
	changed := []byte(`{"keep":1,"value":"after"}`)
	s := directRefreshState(changed)
	s.data.bytes = append([]byte(nil), initial...)
	s.data.delta = []byte(`{"stale":true}`)
	s.data.version = 7
	if _, err := s.data.patcher.patch(initial); err != nil {
		t.Fatal(err)
	}
	prevCache := s.data.patcher.prev

	if _, err := s.refresh(); err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(s.data.bytes, changed) {
		t.Fatalf("cached bytes = %s, want %s", s.data.bytes, changed)
	}
	if !bytes.Equal(s.data.delta, []byte(`{"value":"after"}`)) {
		t.Fatalf("delta = %s, want changed value", s.data.delta)
	}
	if s.data.version != 8 {
		t.Fatalf("version = %d, want 8", s.data.version)
	}
	if got := reflect.ValueOf(s.data.patcher.prev).Pointer(); got == reflect.ValueOf(prevCache).Pointer() {
		t.Fatalf("patcher cache pointer = %x, want a replacement", got)
	}
	wantPrev := map[string]interface{}{"keep": float64(1), "value": "after"}
	if !reflect.DeepEqual(s.data.patcher.prev, wantPrev) {
		t.Fatalf("patcher cache = %#v, want %#v", s.data.patcher.prev, wantPrev)
	}
}

func TestRefreshOwnsMarshalBufferSnapshots(t *testing.T) {
	buffer := json.RawMessage(`{"value":"A"}`)
	s := New(func() (json.RawMessage, error) {
		return buffer, nil
	})
	s.Throttle = 0

	if reflect.ValueOf(s.data.bytes).Pointer() == reflect.ValueOf(buffer).Pointer() {
		t.Fatal("initial cached bytes alias marshal buffer")
	}
	copy(buffer, `{"value":"B"}`)

	if _, err := s.refresh(); err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(s.data.bytes, []byte(`{"value":"B"}`)) {
		t.Fatalf("cached bytes = %s, want value B", s.data.bytes)
	}
	if !bytes.Equal(s.data.delta, []byte(`{"value":"B"}`)) {
		t.Fatalf("delta = %s, want value B", s.data.delta)
	}
	if s.data.version != 2 {
		t.Fatalf("version = %d, want 2", s.data.version)
	}
	wantPrev := map[string]interface{}{"value": "B"}
	if !reflect.DeepEqual(s.data.patcher.prev, wantPrev) {
		t.Fatalf("patcher cache = %#v, want %#v", s.data.patcher.prev, wantPrev)
	}
	if reflect.ValueOf(s.data.bytes).Pointer() == reflect.ValueOf(buffer).Pointer() {
		t.Fatal("updated cached bytes alias marshal buffer")
	}

	copy(buffer, `{"value":"C"}`)
	if !bytes.Equal(s.data.bytes, []byte(`{"value":"B"}`)) {
		t.Fatalf("cached bytes changed after marshal buffer mutation: got %s, want value B", s.data.bytes)
	}
	if !reflect.DeepEqual(s.data.patcher.prev, wantPrev) {
		t.Fatalf("patcher cache changed after marshal buffer mutation: got %#v, want %#v", s.data.patcher.prev, wantPrev)
	}
}

func TestRefreshInvalidStateReturnsErrorAndLeavesCacheRecoverable(t *testing.T) {
	tests := []struct {
		name    string
		payload json.RawMessage
	}{
		{name: "nil", payload: nil},
		{name: "empty", payload: json.RawMessage{}},
		{name: "invalid", payload: json.RawMessage(`{`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New(func() (json.RawMessage, error) {
				return json.RawMessage(`{"value":"before"}`), nil
			})
			s.data.delta = []byte(`{"existing":true}`)
			bytesPointer := reflect.ValueOf(s.data.bytes).Pointer()
			deltaPointer := reflect.ValueOf(s.data.delta).Pointer()
			prevCache := s.data.patcher.prev
			prevPointer := reflect.ValueOf(prevCache).Pointer()
			prevSnapshot := patcherSnapshot(t, prevCache)
			version := s.data.version
			s.Data = func() (json.RawMessage, error) {
				return tt.payload, nil
			}

			_, err := s.refresh()
			if err == nil || !strings.HasPrefix(err.Error(), "create-patch: ") {
				t.Fatalf("refresh error = %v, want create-patch context", err)
			}
			if got := reflect.ValueOf(s.data.bytes).Pointer(); got != bytesPointer {
				t.Fatalf("cached bytes were replaced: got pointer %x, want %x", got, bytesPointer)
			}
			if got := reflect.ValueOf(s.data.delta).Pointer(); got != deltaPointer {
				t.Fatalf("delta was replaced: got pointer %x, want %x", got, deltaPointer)
			}
			if s.data.version != version {
				t.Fatalf("version = %d, want %d", s.data.version, version)
			}
			if got := reflect.ValueOf(s.data.patcher.prev).Pointer(); got != prevPointer {
				t.Fatalf("patcher cache was replaced: got pointer %x, want %x", got, prevPointer)
			}
			if got := patcherSnapshot(t, s.data.patcher.prev); !bytes.Equal(got, prevSnapshot) {
				t.Fatalf("patcher cache changed: got %s, want %s", got, prevSnapshot)
			}
			if !s.data.mut.TryLock() {
				t.Fatal("refresh left data mutex locked")
			}
			s.data.mut.Unlock()

			s.Data = func() (json.RawMessage, error) {
				return json.RawMessage(`{"value":"after"}`), nil
			}
			if _, err := s.refresh(); err != nil {
				t.Fatalf("refresh after invalid state: %v", err)
			}
			if !bytes.Equal(s.data.bytes, []byte(`{"value":"after"}`)) {
				t.Fatalf("recovered bytes = %s, want value after", s.data.bytes)
			}
		})
	}
}

func TestRefreshNilBytesStillSeedsAndPatches(t *testing.T) {
	initial := []byte(`{"old":true}`)
	s := directRefreshState([]byte(`{}`))
	s.data.version = 7
	if _, err := s.data.patcher.patch(initial); err != nil {
		t.Fatal(err)
	}

	if _, err := s.refresh(); err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(s.data.bytes, []byte(`{}`)) {
		t.Fatalf("cached bytes = %s, want {}", s.data.bytes)
	}
	if !bytes.Equal(s.data.delta, []byte(`{"old":null}`)) {
		t.Fatalf("delta = %s, want key deletion", s.data.delta)
	}
	if s.data.version != 8 {
		t.Fatalf("version = %d, want 8", s.data.version)
	}
	if s.data.patcher.prev == nil || len(s.data.patcher.prev) != 0 {
		t.Fatalf("patcher cache = %#v, want non-nil empty map", s.data.patcher.prev)
	}
}

func TestRefreshNullStillClearsByteIdenticalState(t *testing.T) {
	s := directRefreshState([]byte("null"))
	s.data.bytes = []byte("null")
	s.data.delta = []byte(`{"existing":true}`)
	s.data.version = 7
	if _, err := s.data.patcher.patch([]byte(`{"old":true}`)); err != nil {
		t.Fatal(err)
	}
	prevCache := s.data.patcher.prev
	prevSnapshot := patcherSnapshot(t, prevCache)

	if _, err := s.refresh(); err != nil {
		t.Fatal(err)
	}

	if s.data.bytes != nil {
		t.Fatalf("cached bytes = %s, want nil", s.data.bytes)
	}
	if s.data.delta != nil {
		t.Fatalf("delta = %s, want nil", s.data.delta)
	}
	if s.data.version != 8 {
		t.Fatalf("version = %d, want 8", s.data.version)
	}
	prevPointer := reflect.ValueOf(prevCache).Pointer()
	if got := reflect.ValueOf(s.data.patcher.prev).Pointer(); got != prevPointer {
		t.Fatalf("patcher cache was replaced: got pointer %x, want %x", got, prevPointer)
	}
	if got := patcherSnapshot(t, s.data.patcher.prev); !bytes.Equal(got, prevSnapshot) {
		t.Fatalf("patcher cache changed: got %s, want %s", got, prevSnapshot)
	}

	if _, err := s.refresh(); err != nil {
		t.Fatal(err)
	}
	if s.data.bytes != nil || s.data.delta != nil {
		t.Fatalf("repeated null did not keep data cleared: bytes=%s delta=%s", s.data.bytes, s.data.delta)
	}
	if s.data.version != 9 {
		t.Fatalf("version after repeated null = %d, want 9", s.data.version)
	}
	if got := reflect.ValueOf(s.data.patcher.prev).Pointer(); got != prevPointer {
		t.Fatalf("repeated null replaced patcher cache: got pointer %x, want %x", got, prevPointer)
	}
	if got := patcherSnapshot(t, s.data.patcher.prev); !bytes.Equal(got, prevSnapshot) {
		t.Fatalf("repeated null changed patcher cache: got %s, want %s", got, prevSnapshot)
	}
}

func TestGopushNoSubscribersLeavesDataCacheUntouched(t *testing.T) {
	var calls atomic.Int32
	s := &State{
		Throttle: 5 * time.Second,
		Data: func() (json.RawMessage, error) {
			calls.Add(1)
			return json.RawMessage(`{"value":"new"}`), nil
		},
	}
	s.initd.Store(true)
	s.conns = map[int64]*conn{}
	s.data.bytes = []byte(`{"value":"old"}`)
	s.data.delta = []byte(`{"existing":true}`)
	s.data.version = 7
	if _, err := s.data.patcher.patch(s.data.bytes); err != nil {
		t.Fatal(err)
	}

	bytesPointer := reflect.ValueOf(s.data.bytes).Pointer()
	deltaPointer := reflect.ValueOf(s.data.delta).Pointer()
	prevCache := s.data.patcher.prev
	prevPointer := reflect.ValueOf(prevCache).Pointer()
	prevSnapshot := patcherSnapshot(t, prevCache)
	atomic.StoreUint32(&s.push.ing, 1)

	done := make(chan struct{})
	go func() {
		s.gopush()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("zero-subscriber gopush incurred the throttle delay")
	}

	if got := calls.Load(); got != 0 {
		t.Fatalf("Data calls = %d, want 0", got)
	}
	if got := atomic.LoadUint32(&s.push.ing); got != 0 {
		t.Fatalf("push.ing = %d, want 0", got)
	}
	if got := reflect.ValueOf(s.data.bytes).Pointer(); got != bytesPointer {
		t.Fatalf("cached bytes were replaced: got pointer %x, want %x", got, bytesPointer)
	}
	if got := reflect.ValueOf(s.data.delta).Pointer(); got != deltaPointer {
		t.Fatalf("delta was replaced: got pointer %x, want %x", got, deltaPointer)
	}
	if s.data.version != 7 {
		t.Fatalf("version = %d, want 7", s.data.version)
	}
	if got := reflect.ValueOf(s.data.patcher.prev).Pointer(); got != prevPointer {
		t.Fatalf("patcher cache was replaced: got pointer %x, want %x", got, prevPointer)
	}
	if got := patcherSnapshot(t, s.data.patcher.prev); !bytes.Equal(got, prevSnapshot) {
		t.Fatalf("patcher cache changed: got %s, want %s", got, prevSnapshot)
	}
}

func TestGopushNoSubscribersDrainsPublicQueuedPushWithoutData(t *testing.T) {
	var calls atomic.Int32
	s := New(func() (json.RawMessage, error) {
		calls.Add(1)
		return json.RawMessage(`{}`), nil
	})
	initialCalls := calls.Load()
	s.push.mut.Lock()
	if !s.Push() {
		t.Fatal("first Push did not start")
	}
	if s.Push() {
		t.Fatal("second Push unexpectedly started")
	}
	if got := atomic.LoadUint32(&s.push.queued); got != 1 {
		t.Fatalf("push.queued = %d, want 1", got)
	}
	s.push.mut.Unlock()

	waitForPushIdle(t, s)

	if got := calls.Load(); got != initialCalls {
		t.Fatalf("Data calls = %d, want %d", got, initialCalls)
	}
	if got := atomic.LoadUint32(&s.push.queued); got != 0 {
		t.Fatalf("push.queued = %d, want 0", got)
	}
}

func TestGopushInitializesDirectStateBeforeIdleReturn(t *testing.T) {
	var calls atomic.Int32
	s := &State{Data: func() (json.RawMessage, error) {
		calls.Add(1)
		return json.RawMessage(`{"value":1}`), nil
	}}

	if !s.Push() {
		t.Fatal("Push did not start")
	}
	waitForPushIdle(t, s)

	if got := calls.Load(); got != 1 {
		t.Fatalf("Data calls = %d, want 1 initialization call", got)
	}
	if !s.initd.Load() {
		t.Fatal("State was not initialized")
	}
	if s.Throttle != DefaultThrottle || s.WriteTimeout != DefaultWriteTimeout || s.PingInterval != DefaultPingInterval {
		t.Fatalf("defaults not initialized: throttle=%s writeTimeout=%s pingInterval=%s", s.Throttle, s.WriteTimeout, s.PingInterval)
	}
	if s.data.id == "" || s.data.version != 1 || !bytes.Equal(s.data.bytes, []byte(`{"value":1}`)) {
		t.Fatalf("data not initialized: id=%q version=%d bytes=%s", s.data.id, s.data.version, s.data.bytes)
	}
	if s.conns == nil || s.data.patcher.prev == nil {
		t.Fatal("connection or patcher cache was not initialized")
	}
}

func TestGopushActiveConnectionRefreshesAndBroadcasts(t *testing.T) {
	var value atomic.Int64
	var calls atomic.Int32
	s := New(func() (json.RawMessage, error) {
		calls.Add(1)
		return json.RawMessage(fmt.Sprintf(`{"value":%d}`, value.Load())), nil
	})
	s.Throttle = 0
	updates := make(chan Update, 1)
	c := newConn(1, "active", s, s.Version())
	c.transport = &recordingTransport{updates: updates}
	defer func() {
		close(c.connectedCh)
		waitForConnections(t, s, 0)
	}()
	if err := s.subscribe(c); err != nil {
		t.Fatal(err)
	}

	value.Store(1)
	if !s.Push() {
		t.Fatal("Push did not start")
	}
	select {
	case update := <-updates:
		assertStateValue(t, update, 1)
		if update.Version != 2 {
			t.Fatalf("update version = %d, want 2", update.Version)
		}
	case <-time.After(time.Second):
		t.Fatal("active connection did not receive broadcast")
	}
	waitForPushIdle(t, s)
	if got := calls.Load(); got != 3 {
		t.Fatalf("Data calls = %d, want 3 (init, subscribe, gopush)", got)
	}
	if got := atomic.LoadUint32(&s.push.ing); got != 0 {
		t.Fatalf("push.ing = %d, want 0", got)
	}
}

func TestSubscribeRefreshesEverySubscriber(t *testing.T) {
	var calls atomic.Int32
	s := New(func() (json.RawMessage, error) {
		calls.Add(1)
		return json.RawMessage(`{"value":1}`), nil
	})
	first := newConn(1, "first", s, 0)
	second := newConn(2, "second", s, 0)
	defer func() {
		close(first.connectedCh)
		close(second.connectedCh)
		waitForConnections(t, s, 0)
	}()

	if err := s.subscribe(first); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("Data calls after first subscriber = %d, want 2", got)
	}
	if err := s.subscribe(second); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("Data calls after second subscriber = %d, want 3", got)
	}
	if got := s.NumConnections(); got != 2 {
		t.Fatalf("connections = %d, want 2", got)
	}
}

func TestSubscriberRefreshChangePushesExistingConnections(t *testing.T) {
	s := New(func() (json.RawMessage, error) {
		return json.RawMessage("null"), nil
	})
	updates := make(chan Update, 1)
	existing := newConn(1, "existing", s, 0)
	existing.transport = &recordingTransport{updates: updates}
	newSubscriber := newConn(2, "new", s, 0)
	defer func() {
		close(existing.connectedCh)
		close(newSubscriber.connectedCh)
		waitForConnections(t, s, 0)
	}()

	if err := s.subscribe(existing); err != nil {
		t.Fatal(err)
	}
	existing.sendVerMut.Lock()
	existing.version = s.Version()
	existing.sendVerMut.Unlock()
	if err := s.subscribe(newSubscriber); err != nil {
		t.Fatal(err)
	}

	select {
	case update := <-updates:
		if update.Version != s.Version() {
			t.Fatalf("existing subscriber version = %d, want %d", update.Version, s.Version())
		}
		if update.Body != nil {
			t.Fatalf("existing subscriber body = %s, want nil null snapshot", update.Body)
		}
	case <-time.After(time.Second):
		t.Fatal("existing subscriber did not receive subscriber-triggered change")
	}
}

func TestReplacementSubscriberFirstFrameCurrentWithPendingPush(t *testing.T) {
	for i := 0; i < 25; i++ {
		var value atomic.Int64
		s := New(func() (json.RawMessage, error) {
			return json.RawMessage(fmt.Sprintf(`{"value":%d}`, value.Load())), nil
		})
		s.Throttle = 0
		old := newConn(1, "closing", s, s.Version())
		old.transport = &recordingTransport{updates: make(chan Update, 1)}
		if err := s.subscribe(old); err != nil {
			t.Fatal(err)
		}

		value.Store(int64(i + 1))
		s.push.mut.Lock()
		if !s.Push() {
			t.Fatal("pending Push did not start")
		}
		updates := make(chan Update, 1)
		replacement := newConn(2, "replacement", s, 0)
		replacement.transport = &recordingTransport{updates: updates}
		subscribed := make(chan error, 1)
		go func() {
			if err := s.subscribe(replacement); err != nil {
				subscribed <- err
				return
			}
			replacement.Push()
			subscribed <- nil
		}()
		s.push.mut.Unlock()

		select {
		case update := <-updates:
			assertStateValue(t, update, int64(i+1))
		case <-time.After(time.Second):
			t.Fatalf("iteration %d: replacement did not receive first frame", i)
		}
		if err := <-subscribed; err != nil {
			t.Fatalf("iteration %d: subscribe: %v", i, err)
		}
		waitForPushIdle(t, s)
		close(old.connectedCh)
		close(replacement.connectedCh)
		waitForConnections(t, s, 0)
	}
}

func TestSubscribeRefreshErrorDoesNotSubscribe(t *testing.T) {
	wantErr := errors.New("marshal unavailable")
	s := New(func() (json.RawMessage, error) {
		return json.RawMessage(`{"value":1}`), nil
	})
	s.Data = func() (json.RawMessage, error) { return nil, wantErr }
	c := newConn(1, "failed", s, 0)

	err := s.subscribe(c)
	if !errors.Is(err, wantErr) {
		t.Fatalf("subscribe error = %v, want %v", err, wantErr)
	}
	if got := s.NumConnections(); got != 0 {
		t.Fatalf("connections = %d, want 0", got)
	}
}

func TestHandleRefreshErrorClosesConnectedTransport(t *testing.T) {
	wantErr := errors.New("marshal unavailable")
	s := New(func() (json.RawMessage, error) {
		return json.RawMessage(`{"value":1}`), nil
	})
	s.Data = func() (json.RawMessage, error) { return nil, wantErr }
	handleErr := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := s.Handle(w, r)
		handleErr <- err
	}))
	defer server.Close()

	ws := dialStateWebsocket(t, server.URL)
	defer ws.Close()
	readUpdate(t, ws, true)

	select {
	case err := <-handleErr:
		if !errors.Is(err, wantErr) {
			t.Fatalf("Handle error = %v, want %v", err, wantErr)
		}
	case <-time.After(time.Second):
		t.Fatal("Handle did not return after refresh failure")
	}
	if got := s.NumConnections(); got != 0 {
		t.Fatalf("connections = %d, want 0", got)
	}
	var update Update
	if err := ws.ReadJSON(&update); err == nil {
		t.Fatalf("transport remained open after refresh failure: update = %+v", update)
	}
}

func TestHandleInitialPingFailureClosesOnceAndPreservesCommittedError(t *testing.T) {
	wantErr := errors.New("initial send failed")
	controlled := newControlledTransport()
	controlled.sendErr = wantErr
	s := New(func() (json.RawMessage, error) { return json.RawMessage(`{}`), nil })
	s.transportFactory = func(*http.Request) transport { return controlled }
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://example.test/sync", nil)

	_, err := s.Handle(recorder, req)
	var committed *responseCommittedError
	if !errors.As(err, &committed) {
		t.Fatalf("Handle error = %v, want committed response error", err)
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("Handle error = %v, want wrapped %v", err, wantErr)
	}
	if got := controlled.closeCalls.Load(); got != 1 {
		t.Fatalf("transport close calls = %d, want 1", got)
	}
	if got := s.NumConnections(); got != 0 {
		t.Fatalf("connections = %d, want 0", got)
	}
}

func TestHandleDataPanicUnwindsAndStateRemainsUsable(t *testing.T) {
	s := New(func() (json.RawMessage, error) { return json.RawMessage(`{"value":1}`), nil })
	var transportsMu sync.Mutex
	var transports []*controlledTransport
	s.transportFactory = func(*http.Request) transport {
		transport := newControlledTransport()
		transportsMu.Lock()
		transports = append(transports, transport)
		transportsMu.Unlock()
		return transport
	}
	s.Data = func() (json.RawMessage, error) { panic("custom marshal panic") }

	_, err := s.Handle(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://example.test/sync", nil))
	var committed *responseCommittedError
	if !errors.As(err, &committed) || !strings.Contains(err.Error(), "data panic: custom marshal panic") {
		t.Fatalf("Handle error = %v, want committed data panic", err)
	}
	if got := s.NumConnections(); got != 0 {
		t.Fatalf("connections after panic = %d, want 0", got)
	}
	transportsMu.Lock()
	first := transports[0]
	transportsMu.Unlock()
	if got := first.closeCalls.Load(); got < 1 {
		t.Fatalf("panic transport close calls = %d, want at least 1", got)
	}

	s.Data = func() (json.RawMessage, error) { return json.RawMessage(`{"value":2}`), nil }
	type handleResult struct {
		conn Conn
		err  error
	}
	handled := make(chan handleResult, 1)
	go func() {
		conn, err := s.Handle(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://example.test/sync", nil))
		handled <- handleResult{conn: conn, err: err}
	}()
	var result handleResult
	select {
	case result = <-handled:
	case <-time.After(time.Second):
		t.Fatal("Handle after Data panic remained blocked")
	}
	if result.err != nil {
		t.Fatalf("Handle after panic: %v", result.err)
	}
	if !s.Push() {
		t.Fatal("Push after recovered panic did not start")
	}
	waitForPushIdle(t, s)
	if err := result.conn.Close(); err != nil {
		t.Fatal(err)
	}
	result.conn.Wait()
	waitForConnections(t, s, 0)
}

func TestHandleInvalidJSONClosesConnectedTransportAndStateRecovers(t *testing.T) {
	s := New(func() (json.RawMessage, error) {
		return json.RawMessage(`{"value":1}`), nil
	})
	s.Data = func() (json.RawMessage, error) { return json.RawMessage(`{`), nil }
	handleErr := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := s.Handle(w, r)
		handleErr <- err
	}))
	defer server.Close()

	ws := dialStateWebsocket(t, server.URL)
	defer ws.Close()
	readUpdate(t, ws, true)
	select {
	case err := <-handleErr:
		if err == nil || !strings.Contains(err.Error(), "create-patch:") {
			t.Fatalf("Handle error = %v, want create-patch context", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Handle did not unwind after invalid JSON")
	}
	if got := s.NumConnections(); got != 0 {
		t.Fatalf("connections = %d, want 0", got)
	}
	var update Update
	if err := ws.ReadJSON(&update); err == nil {
		t.Fatalf("transport remained open after invalid JSON: update = %+v", update)
	}
	if !s.data.mut.TryLock() {
		t.Fatal("invalid JSON left data mutex locked")
	}
	s.data.mut.Unlock()
	s.Data = func() (json.RawMessage, error) { return json.RawMessage(`{"value":2}`), nil }
	if _, err := s.refresh(); err != nil {
		t.Fatalf("refresh after invalid JSON: %v", err)
	}
}

func TestServeHTTPCommittedRefreshErrorDoesNotAppendHTTPError(t *testing.T) {
	for _, gzipEnabled := range []bool{false, true} {
		name := "plain"
		if gzipEnabled {
			name = "gzip"
		}
		t.Run(name, func(t *testing.T) {
			s := New(func() (json.RawMessage, error) {
				return json.RawMessage(`{"value":1}`), nil
			})
			s.Data = func() (json.RawMessage, error) { return json.RawMessage(`{`), nil }
			req := httptest.NewRequest(http.MethodGet, "http://example.test/sync", nil)
			req.Header.Set("Accept", "text/event-stream")
			if gzipEnabled {
				req.Header.Set("Accept-Encoding", "gzip")
			}
			recorder := httptest.NewRecorder()

			s.ServeHTTP(recorder, req)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", recorder.Code)
			}
			if body := recorder.Body.String(); strings.Contains(body, "velox initial refresh failed") {
				t.Fatalf("committed SSE response contains appended HTTP error: %q", body)
			}
		})
	}
}

func TestServeHTTPWebsocketRefreshErrorDoesNotWriteAfterHijack(t *testing.T) {
	s := New(func() (json.RawMessage, error) {
		return json.RawMessage(`{"value":1}`), nil
	})
	s.Data = func() (json.RawMessage, error) { return json.RawMessage(`{`), nil }
	var serverLogs bytes.Buffer
	server := httptest.NewUnstartedServer(s)
	server.Config.ErrorLog = log.New(&serverLogs, "", 0)
	server.Start()

	ws := dialStateWebsocket(t, server.URL)
	readUpdate(t, ws, true)
	var update Update
	if err := ws.ReadJSON(&update); err == nil {
		t.Fatalf("websocket remained open after refresh error: update = %+v", update)
	}
	ws.Close()
	server.Close()
	if logs := serverLogs.String(); strings.Contains(logs, "hijacked") || strings.Contains(logs, "superfluous") {
		t.Fatalf("ServeHTTP attempted an HTTP response after websocket upgrade: %s", logs)
	}
}

func TestServeHTTPWebsocketInitialPingFailureDoesNotWriteAfterHijack(t *testing.T) {
	s := New(func() (json.RawMessage, error) { return json.RawMessage(`{}`), nil })
	failing := &failingSendTransport{
		transport: &websocketsTransport{writeTimeout: time.Second},
		err:       errors.New("controlled initial ping failure"),
	}
	s.transportFactory = func(*http.Request) transport { return failing }
	var serverLogs bytes.Buffer
	server := httptest.NewUnstartedServer(s)
	server.Config.ErrorLog = log.New(&serverLogs, "", 0)
	server.Start()

	ws, _, err := websocket.DefaultDialer.Dial(websocketURL(server.URL), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var update Update
	if err := ws.ReadJSON(&update); err == nil {
		t.Fatalf("websocket remained open after initial ping failure: update = %+v", update)
	}
	ws.Close()
	server.Close()
	if got := failing.closeCalls.Load(); got != 1 {
		t.Fatalf("websocket close calls = %d, want 1", got)
	}
	if logs := serverLogs.String(); strings.Contains(logs, "hijacked") || strings.Contains(logs, "superfluous") {
		t.Fatalf("ServeHTTP attempted HTTP output after failed websocket ping: %s", logs)
	}
}

func TestServeHTTPPreCommitFailureStillWritesHTTPError(t *testing.T) {
	s := New(func() (json.RawMessage, error) { return json.RawMessage(`{}`), nil })
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://example.test/sync", nil)

	s.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "invalid sync request") {
		t.Fatalf("HTTP error body = %q, want invalid sync request", recorder.Body.String())
	}
}

func TestReconnectAfterIdlePushReceivesCurrentFirstState(t *testing.T) {
	var value atomic.Int64
	var calls atomic.Int32
	s := New(func() (json.RawMessage, error) {
		calls.Add(1)
		return json.RawMessage(fmt.Sprintf(`{"value":%d}`, value.Load())), nil
	})
	server := httptest.NewServer(s)
	defer server.Close()

	ws := dialStateWebsocket(t, server.URL)
	readUpdate(t, ws, true)
	assertStateValue(t, readUpdate(t, ws, false), 0)
	if err := ws.Close(); err != nil {
		t.Fatal(err)
	}
	waitForConnections(t, s, 0)

	value.Store(1)
	callsBeforeIdlePush := calls.Load()
	if !s.Push() {
		t.Fatal("idle Push did not start")
	}
	waitForPushIdle(t, s)
	if got := calls.Load(); got != callsBeforeIdlePush {
		t.Fatalf("idle Push called Data: calls = %d, want %d", got, callsBeforeIdlePush)
	}

	ws = dialStateWebsocket(t, server.URL)
	readUpdate(t, ws, true)
	assertStateValue(t, readUpdate(t, ws, false), 1)
	if err := ws.Close(); err != nil {
		t.Fatal(err)
	}

	// Reconnect without waiting for asynchronous removal of the old connection,
	// while also racing a public Push. The replacement's first state frame must
	// still reflect the latest value.
	value.Store(2)
	if !s.Push() {
		t.Fatal("racing idle Push did not start")
	}
	ws = dialStateWebsocket(t, server.URL)
	readUpdate(t, ws, true)
	assertStateValue(t, readUpdate(t, ws, false), 2)
	if err := ws.Close(); err != nil {
		t.Fatal(err)
	}
	waitForConnections(t, s, 0)
	waitForPushIdle(t, s)
}

func dialStateWebsocket(t *testing.T, serverURL string) *websocket.Conn {
	t.Helper()
	ws, _, err := websocket.DefaultDialer.Dial(websocketURL(serverURL), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	return ws
}

func websocketURL(serverURL string) string {
	return "ws" + strings.TrimPrefix(serverURL, "http")
}

func readUpdate(t *testing.T, ws *websocket.Conn, wantPing bool) Update {
	t.Helper()
	var update Update
	if err := ws.ReadJSON(&update); err != nil {
		t.Fatal(err)
	}
	if update.Ping != wantPing {
		t.Fatalf("update ping = %v, want %v; update = %+v", update.Ping, wantPing, update)
	}
	return update
}

func assertStateValue(t *testing.T, update Update, want int64) {
	t.Helper()
	var body struct {
		Value int64 `json:"value"`
	}
	if err := json.Unmarshal(update.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Value != want {
		t.Fatalf("first state value = %d, want %d; update = %+v", body.Value, want, update)
	}
}

func waitForConnections(t *testing.T, s *State, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for s.NumConnections() != want {
		if time.Now().After(deadline) {
			t.Fatalf("connections = %d, want %d", s.NumConnections(), want)
		}
		time.Sleep(time.Millisecond)
	}
}

func waitForPushIdle(t *testing.T, s *State) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadUint32(&s.push.ing) != 0 || atomic.LoadUint32(&s.push.queued) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("push did not become idle: ing=%d queued=%d", atomic.LoadUint32(&s.push.ing), atomic.LoadUint32(&s.push.queued))
		}
		time.Sleep(time.Millisecond)
	}
}

type recordingTransport struct {
	updates chan Update
}

func (r *recordingTransport) connect(http.ResponseWriter, *http.Request) error { return nil }
func (r *recordingTransport) wait() error                                      { return nil }
func (r *recordingTransport) close() error                                     { return nil }
func (r *recordingTransport) send(update *Update) error {
	copy := *update
	copy.Body = bytes.Clone(update.Body)
	r.updates <- copy
	return nil
}

type controlledTransport struct {
	closed     chan struct{}
	closeOnce  sync.Once
	closeCalls atomic.Int32
	sendCalls  atomic.Int32
	connectErr error
	sendErr    error
}

type failingSendTransport struct {
	transport
	err        error
	closeCalls atomic.Int32
}

func (f *failingSendTransport) send(*Update) error { return f.err }
func (f *failingSendTransport) close() error {
	f.closeCalls.Add(1)
	return f.transport.close()
}

func newControlledTransport() *controlledTransport {
	return &controlledTransport{closed: make(chan struct{})}
}

func (c *controlledTransport) connect(http.ResponseWriter, *http.Request) error {
	return c.connectErr
}
func (c *controlledTransport) send(*Update) error {
	c.sendCalls.Add(1)
	return c.sendErr
}
func (c *controlledTransport) wait() error {
	<-c.closed
	return nil
}
func (c *controlledTransport) close() error {
	c.closeCalls.Add(1)
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func directRefreshState(payload []byte) *State {
	s := &State{}
	s.initd.Store(true)
	s.Data = func() (json.RawMessage, error) {
		return payload, nil
	}
	return s
}

func patcherSnapshot(t *testing.T, cache map[string]interface{}) []byte {
	t.Helper()
	data, err := json.Marshal(cache)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
