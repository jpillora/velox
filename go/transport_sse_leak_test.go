package velox

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// gateWriter blocks every Write until the gate is closed
type gateWriter struct {
	gate chan struct{}
	h    http.Header
}

type switchWriter struct {
	mut      sync.Mutex
	h        http.Header
	gate     chan struct{}
	blocked  bool
	code     int
	returned bool
	misuse   int
}

func newSwitchWriter() *switchWriter {
	return &switchWriter{h: http.Header{}, gate: make(chan struct{})}
}

func (w *switchWriter) Header() http.Header { return w.h }
func (w *switchWriter) WriteHeader(c int) {
	w.mut.Lock()
	w.code = c
	w.mut.Unlock()
	w.touch()
}
func (w *switchWriter) Flush() { w.touch() }
func (w *switchWriter) Write(p []byte) (int, error) {
	w.mut.Lock()
	blocked := w.blocked
	gate := w.gate
	w.mut.Unlock()
	if blocked {
		<-gate
	}
	w.touch()
	return len(p), nil
}

// handlerReturned marks the point net/http reclaims the connection's buffers.
// Every use of this writer after it is the bug that panics a real server on a
// nil *bufio.Writer.
func (w *switchWriter) handlerReturned() {
	w.mut.Lock()
	w.returned = true
	w.mut.Unlock()
}

func (w *switchWriter) touch() {
	w.mut.Lock()
	defer w.mut.Unlock()
	if w.returned {
		w.misuse++
	}
}

func (w *switchWriter) misuses() int {
	w.mut.Lock()
	defer w.mut.Unlock()
	return w.misuse
}

func (w *switchWriter) status() int {
	w.mut.Lock()
	defer w.mut.Unlock()
	return w.code
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

// deadlineWriter is a switchWriter that also carries net/http's optional
// deadline control, the way *http.response does
type deadlineWriter struct {
	*switchWriter
	dmut      sync.Mutex
	deadlines []time.Time
}

func (w *deadlineWriter) SetWriteDeadline(t time.Time) error {
	w.dmut.Lock()
	w.deadlines = append(w.deadlines, t)
	w.dmut.Unlock()
	return nil
}

func (w *deadlineWriter) written() []time.Time {
	w.dmut.Lock()
	defer w.dmut.Unlock()
	return append([]time.Time(nil), w.deadlines...)
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

// a timed-out writer goroutine keeps compressing into gzw after send() gives up
// on it, so close() must leave it attached and drain() — which the HTTP handler
// blocks on — is what releases it
func TestSSEGzipTimedOutWriterIsReleasedOnlyByDrain(t *testing.T) {
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
	if !es.writerAbandoned || es.w != nil {
		t.Fatalf("timed-out writer was not detached: abandoned=%v w=%T", es.writerAbandoned, es.w)
	}
	if es.gzw == nil {
		t.Fatal("close released the compressor while a writer was still using it")
	}
	candidate := gzipWriterPool.Get().(*gzip.Writer)
	if candidate == gz {
		t.Fatal("in-use compressor was returned to the pool")
	}
	gzipWriterPool.Put(candidate)

	drained := make(chan struct{})
	go func() {
		es.drain()
		close(drained)
	}()
	select {
	case <-drained:
		t.Fatal("drain returned while the writer still held the ResponseWriter")
	case <-time.After(50 * time.Millisecond):
	}

	w.release()
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("drain did not return once the writer completed")
	}
	if es.gzw != nil {
		t.Fatal("drain did not release the compressor")
	}
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

// nothing calls conn.Wait() when the initial ping times out, so connect() has
// to drain there: the handler must still outlive the writer, and must not try
// to append an HTTP error to a response the transport already committed
func TestSSEGzipInitialPingTimeoutHoldsHandlerUntilWriterCompletes(t *testing.T) {
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
		w.handlerReturned()
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("ServeHTTP returned while the timed-out writer still held the ResponseWriter")
	case <-time.After(100 * time.Millisecond):
	}
	if es.IsConnected() {
		t.Fatal("SSE transport remained connected after initial ping timeout")
	}
	if !es.writerAbandoned || es.w != nil {
		t.Fatalf("timed-out initial ping was not detached: abandoned=%v w=%T", es.writerAbandoned, es.w)
	}

	w.release()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ServeHTTP did not unwind once the writer completed")
	}
	if code := w.status(); code != 0 {
		t.Fatalf("initial ping timeout wrote HTTP status %d over the committed response", code)
	}
	if n := w.misuses(); n != 0 {
		t.Fatalf("ResponseWriter used %d times after ServeHTTP returned", n)
	}
	deadline := time.Now().Add(time.Second)
	for countSendGoroutines() != before && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := countSendGoroutines(); got != before {
		t.Fatalf("send goroutines = %d, want baseline %d", got, before)
	}
}

// the incident this guards: a push whose write stalls past writeTimeout is
// abandoned by send(), conn.Push() closes the connection, and the handler
// returns — net/http then reclaims the connection's buffers while the abandoned
// goroutine is still inside Write/Flush, and the process dies on a nil
// *bufio.Writer somewhere under chunkWriter.flush
func TestSSEStalledPushDoesNotOutliveTheHandler(t *testing.T) {
	w := newSwitchWriter()
	var value atomic.Int64
	s := New(func() (json.RawMessage, error) {
		return json.RawMessage(fmt.Sprintf(`{"value":%d}`, value.Load())), nil
	})
	s.WriteTimeout = 20 * time.Millisecond
	s.Throttle = MinThrottle
	req := httptest.NewRequest(http.MethodGet, "http://example.test/sync", nil)
	req.Header.Set("Accept", "text/event-stream")
	done := make(chan struct{})
	go func() {
		s.ServeHTTP(w, req)
		w.handlerReturned()
		close(done)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for s.NumConnections() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if s.NumConnections() != 1 {
		t.Fatal("client never connected")
	}

	//stall the client mid-stream, then push a new version at it
	w.block()
	value.Add(1)
	s.Push()

	select {
	case <-done:
		t.Fatal("handler returned while the stalled writer still held the ResponseWriter")
	case <-time.After(200 * time.Millisecond):
	}

	w.release()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not unwind once the writer completed")
	}
	if n := w.misuses(); n != 0 {
		t.Fatalf("ResponseWriter used %d times after ServeHTTP returned", n)
	}
}

// every send must put a real deadline on the connection. without one an
// abandoned writer is only released when the client's TCP session dies, and
// drain — so the HTTP handler — waits exactly that long.
func TestSSESendSetsWriteDeadline(t *testing.T) {
	w := &deadlineWriter{switchWriter: newSwitchWriter()}
	req := httptest.NewRequest(http.MethodGet, "http://example.test/sync", nil)
	es := &eventSourceTransport{writeTimeout: 250 * time.Millisecond}
	if err := es.connect(w, req); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := es.send(&Update{Ping: true}); err != nil {
		t.Fatalf("send: %v", err)
	}
	set := w.written()
	if len(set) != 1 {
		t.Fatalf("write deadlines set = %d, want 1", len(set))
	}
	if d := set[0].Sub(started); d < 200*time.Millisecond || d > 2*time.Second {
		t.Fatalf("deadline %s away, want ~%s", d, es.writeTimeout)
	}
}

// http.ResponseController only reaches the connection through wrappers that
// implement Unwrap — ours must, or a caller wrapping us silently loses
// deadline control
func TestGzipResponseWriterUnwrapsForResponseController(t *testing.T) {
	w := &deadlineWriter{switchWriter: newSwitchWriter()}
	gzw := &gzipResponseWriter{ResponseWriter: w}
	if err := http.NewResponseController(gzw).SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("SetWriteDeadline through the gzip wrapper: %v", err)
	}
	if n := len(w.written()); n != 1 {
		t.Fatalf("deadlines set = %d, want 1", n)
	}
}
