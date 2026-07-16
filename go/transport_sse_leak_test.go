package velox

import (
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"
)

// gateWriter blocks every Write until the gate is closed
type gateWriter struct {
	gate chan struct{}
	h    http.Header
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
	es := &eventSourceTransport{
		writeTimeout: 5 * time.Millisecond,
		isConnected:  true,
		w:            gw,
	}
	before := countSendGoroutines()
	const n = 10
	for i := range n {
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
