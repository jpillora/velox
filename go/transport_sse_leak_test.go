package velox

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// gateWriter blocks every Write until the gate is closed
type gateWriter struct {
	gate chan struct{}
	h    http.Header
}

type switchWriter struct {
	mut     sync.Mutex
	h       http.Header
	gate    chan struct{}
	blocked bool
}

func newSwitchWriter() *switchWriter {
	return &switchWriter{h: http.Header{}, gate: make(chan struct{})}
}

func (w *switchWriter) Header() http.Header { return w.h }
func (w *switchWriter) WriteHeader(int)     {}
func (w *switchWriter) Flush()              {}
func (w *switchWriter) Write(p []byte) (int, error) {
	w.mut.Lock()
	blocked := w.blocked
	gate := w.gate
	w.mut.Unlock()
	if blocked {
		<-gate
	}
	return len(p), nil
}
func (w *switchWriter) block() {
	w.mut.Lock()
	w.blocked = true
	w.mut.Unlock()
}
func (w *switchWriter) release() {
	w.mut.Lock()
	select {
	case <-w.gate:
	default:
		close(w.gate)
	}
	w.mut.Unlock()
}

func (g *gateWriter) Header() http.Header { return g.h }
func (g *gateWriter) WriteHeader(int)     {}
func (g *gateWriter) Write(p []byte) (int, error) {
	<-g.gate
	return len(p), nil
}

func countSendGoroutines() int {
	buf := make([]byte, 1<<22)
	n := runtime.Stack(buf, true)
	return strings.Count(string(buf[:n]), "eventSourceTransport).send.func")
}

// guards against the goroutine leak where a slow client makes send() hit its
// writeTimeout, send() returns and abandons the writer goroutine, and once
// the write eventually completes the goroutine blocks forever on the
// `sent <- err` channel send — the channel must be buffered so the abandoned
// goroutine can deposit its result and exit
func TestSSESendTimeoutNoGoroutineLeak(t *testing.T) {
	gw := &gateWriter{gate: make(chan struct{}), h: http.Header{}}
	before := countSendGoroutines()
	const n = 10
	for i := range n {
		es := &eventSourceTransport{
			writeTimeout: 5 * time.Millisecond,
			isConnected:  true,
			w:            gw,
		}
		err := es.send(&Update{Version: int64(i)})
		if err == nil || err.Error() != "timeout" {
			t.Fatalf("send %d: expected timeout, got %v", i, err)
		}
	}
	// release the "slow client" — writes complete, writer goroutines must
	// deposit into the buffered channel and exit
	close(gw.gate)
	deadline := time.Now().Add(2 * time.Second)
	leaked := -1
	for time.Now().Before(deadline) {
		leaked = countSendGoroutines() - before
		if leaked == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	buf := make([]byte, 1<<22)
	stacks := string(buf[:runtime.Stack(buf, true)])
	t.Fatalf("%d writer goroutines still alive after 2s\n%s", leaked, stacks)
}

func TestSSEConcurrentCloseIsIdempotent(t *testing.T) {
	es := &eventSourceTransport{
		isConnected: true,
		connected:   make(chan struct{}),
	}
	const closers = 25
	var wg sync.WaitGroup
	wg.Add(closers)
	for i := 0; i < closers; i++ {
		go func() {
			defer wg.Done()
			if err := es.close(); err != nil {
				t.Errorf("close: %v", err)
			}
		}()
	}
	wg.Wait()

	select {
	case <-es.connected:
	default:
		t.Fatal("close did not unblock waiters")
	}
	if es.IsConnected() {
		t.Fatal("transport remained connected")
	}
	if err := es.close(); err != nil {
		t.Fatalf("repeated close: %v", err)
	}
}

func TestSSEInvalidRefreshAndRequestCancelFullyUnwind(t *testing.T) {
	s := New(func() (json.RawMessage, error) {
		return json.RawMessage(`{"value":1}`), nil
	})
	entered := make(chan struct{})
	release := make(chan struct{})
	s.Data = func() (json.RawMessage, error) {
		close(entered)
		<-release
		return json.RawMessage(`{`), nil
	}
	s.push.generation.Add(1)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "http://example.test/sync", nil).WithContext(ctx)
	req.Header.Set("Accept", "text/event-stream")
	recorder := httptest.NewRecorder()
	handleErr := make(chan error, 1)
	go func() {
		_, err := s.Handle(recorder, req)
		handleErr <- err
	}()

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start after SSE connect")
	}
	cancel()
	close(release)

	select {
	case err := <-handleErr:
		if err == nil || !strings.Contains(err.Error(), "create-patch:") {
			t.Fatalf("Handle error = %v, want create-patch context", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SSE Handle did not fully unwind")
	}
	if got := s.NumConnections(); got != 0 {
		t.Fatalf("connections = %d, want 0", got)
	}
}

func TestSSEGzipTimedOutWriterIsAbandonedNotReused(t *testing.T) {
	w := newSwitchWriter()
	w.block()
	req := httptest.NewRequest(http.MethodGet, "http://example.test/sync", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	es := &eventSourceTransport{writeTimeout: 5 * time.Millisecond}
	if err := es.connect(w, req); err != nil {
		t.Fatal(err)
	}
	gzw := es.gzw
	if gzw == nil {
		t.Fatal("gzip was not enabled")
	}
	gz := gzw.gz

	if err := es.send(&Update{Ping: true}); err == nil || err.Error() != "timeout" {
		t.Fatalf("send error = %v, want timeout", err)
	}
	if err := es.send(&Update{Ping: true}); err == nil || err.Error() != "not connected" {
		t.Fatalf("send after abandoned writer = %v, want not connected", err)
	}
	started := time.Now()
	if err := es.close(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("close took %s after send timeout", elapsed)
	}
	if !es.writerAbandoned || es.gzw != nil || es.w != nil {
		t.Fatalf("gzip was not safely detached: abandoned=%v gzw=%p w=%T", es.writerAbandoned, es.gzw, es.w)
	}
	candidate := gzipWriterPool.Get().(*gzip.Writer)
	if candidate == gz {
		t.Fatal("abandoned compressor was returned to the pool")
	}
	gzipWriterPool.Put(candidate)

	w.release()
	deadline := time.Now().Add(time.Second)
	for countSendGoroutines() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := countSendGoroutines(); got != 0 {
		t.Fatalf("%d timed-out gzip writer goroutines remain", got)
	}
}

func TestSSEGzipRefreshErrorCloseDoesNotWriteFooter(t *testing.T) {
	w := newSwitchWriter()
	s := New(func() (json.RawMessage, error) {
		return json.RawMessage(`{"value":1}`), nil
	})
	s.Data = func() (json.RawMessage, error) {
		w.block()
		return json.RawMessage(`{`), nil
	}
	s.push.generation.Add(1)
	req := httptest.NewRequest(http.MethodGet, "http://example.test/sync", nil)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Accept-Encoding", "gzip")
	handleErr := make(chan error, 1)
	go func() {
		_, err := s.Handle(w, req)
		handleErr <- err
	}()

	select {
	case err := <-handleErr:
		if err == nil || !strings.Contains(err.Error(), "create-patch:") {
			t.Fatalf("Handle error = %v, want create-patch context", err)
		}
	case <-time.After(100 * time.Millisecond):
		w.release()
		t.Fatal("gzip refresh-error cleanup blocked writing a footer")
	}
	if got := s.NumConnections(); got != 0 {
		t.Fatalf("connections = %d, want 0", got)
	}
}

func TestSSEGzipInitialPingTimeoutDoesNotAppendHTTPError(t *testing.T) {
	w := newSwitchWriter()
	w.block()
	es := &eventSourceTransport{writeTimeout: 5 * time.Millisecond}
	s := New(func() (json.RawMessage, error) { return json.RawMessage(`{}`), nil })
	s.transportFactory = func(*http.Request) transport { return es }
	req := httptest.NewRequest(http.MethodGet, "http://example.test/sync", nil)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Accept-Encoding", "gzip")
	before := countSendGoroutines()
	done := make(chan struct{})
	go func() {
		s.ServeHTTP(w, req)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		w.release()
		t.Fatal("initial ping timeout attempted blocking HTTP error output")
	}
	if es.IsConnected() {
		t.Fatal("SSE transport remained connected after initial ping timeout")
	}
	if !es.writerAbandoned || es.gzw != nil || es.w != nil {
		t.Fatalf("timed-out initial ping was not detached: abandoned=%v gzw=%p w=%T", es.writerAbandoned, es.gzw, es.w)
	}
	w.release()
	deadline := time.Now().Add(time.Second)
	for countSendGoroutines() != before && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := countSendGoroutines(); got != before {
		t.Fatalf("send goroutines = %d, want baseline %d", got, before)
	}
}
