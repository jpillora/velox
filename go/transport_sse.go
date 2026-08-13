package velox

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/jpillora/eventsource"
)

var encodePool = sync.Pool{
	New: func() any { return new(bytes.Buffer) },
}

type eventSourceTransport struct {
	mut             sync.Mutex
	writeTimeout    time.Duration
	w               http.ResponseWriter
	rc              *http.ResponseController // write deadlines on the raw writer
	gzw             *gzipResponseWriter      // non-nil if gzip is active
	writers         sync.WaitGroup           // writer goroutines still holding w
	writerAbandoned bool                     // a timed-out child may still be using w
	isConnected     bool
	connected       chan struct{}
}

func (es *eventSourceTransport) connect(w http.ResponseWriter, r *http.Request) error {
	//deadlines must be set on the writer net/http handed us, not on a wrapper
	es.rc = http.NewResponseController(w)
	//eventsource headers
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Vary", "Accept")
	w.Header().Set("Content-Type", "text/event-stream")
	//negotiate gzip compression (skip if outer middleware already set it)
	if acceptsGzip(r) && w.Header().Get("Content-Encoding") == "" {
		if flusher, ok := w.(http.Flusher); ok {
			w.Header().Set("Content-Encoding", "gzip")
			gz := gzipWriterPool.Get().(*gzip.Writer)
			gz.Reset(w)
			gzw := &gzipResponseWriter{
				ResponseWriter: w,
				gz:             gz,
				flusher:        flusher,
			}
			es.gzw = gzw
			es.w = gzw
		}
	}
	//connection is now expecting a stream of events
	if es.w == nil {
		es.w = w
	}
	es.mut.Lock()
	es.isConnected = true
	es.connected = make(chan struct{})
	connected := es.connected
	es.mut.Unlock()
	go func() {
		select {
		case <-connected:
		case <-r.Context().Done(): //client disconnected early
			es.close()
		}
	}()
	return nil
}

// http.ResponseWriter.Write is not thread safe, so we need to lock
func (es *eventSourceTransport) send(upd *Update) error {
	es.mut.Lock()
	defer es.mut.Unlock()
	if !es.isConnected || es.writerAbandoned {
		return errors.New("not connected")
	}
	writer := es.w

	buf := encodePool.Get().(*bytes.Buffer)
	buf.Reset()
	if err := json.NewEncoder(buf).Encode(upd); err != nil {
		encodePool.Put(buf)
		return err
	}
	// json.Encoder.Encode appends a trailing newline; strip it
	b := buf.Bytes()
	if len(b) > 0 && b[len(b)-1] == '\n' {
		b = b[:len(b)-1]
	}
	// bound the write where the ResponseWriter chain exposes the connection.
	// net/http does; wrappers only do if they implement Unwrap. without a
	// deadline a stalled client holds the writer until it disconnects, and
	// drain — so the HTTP handler — waits exactly that long.
	if es.rc != nil {
		es.rc.SetWriteDeadline(time.Now().Add(es.writeTimeout))
	}
	// the write gets its own goroutine so a stalled client cannot block the
	// pusher. past the timeout that goroutine still holds writer, so drain
	// keeps the handler open until it lets go — touching a ResponseWriter
	// after ServeHTTP returns dereferences a freed *bufio.Writer.
	sent := make(chan error, 1)
	es.writers.Add(1)
	go func() {
		defer es.writers.Done()
		sent <- eventsource.WriteEvent(writer, eventsource.Event{
			ID:   strconv.FormatInt(upd.Version, 10),
			Data: b,
		})
	}()
	select {
	case <-time.After(es.writeTimeout):
		es.writerAbandoned = true
		// leave the deadline in place — it is what will release the abandoned
		// writer, and no further send can reuse this transport anyway
		// don't return buf to pool; goroutine may still be writing
		return errors.New("timeout")
	case err := <-sent:
		// clear it again while the stream is idle. HTTP/2 does not treat a
		// deadline the way a net.Conn does: it arms a timer that RSTs the
		// stream when it fires, write in flight or not, so an idle stream with
		// a shorter WriteTimeout than PingInterval would be killed mid-life.
		es.clearWriteDeadline()
		encodePool.Put(buf)
		return err
	}
}

func (es *eventSourceTransport) wait() error {
	<-es.connected
	return nil
}

// clearWriteDeadline lifts the bound set for a completed write, so the deadline
// only ever covers a write actually in flight.
func (es *eventSourceTransport) clearWriteDeadline() {
	if es.rc != nil {
		es.rc.SetWriteDeadline(time.Time{})
	}
}

// drain blocks until no writer goroutine is using the http.ResponseWriter, then
// releases the gzip writer now that nothing can still be compressing into it.
// The HTTP handler must not return before this completes: net/http reclaims the
// connection's buffers once ServeHTTP returns, so a late write or flush panics
// on a nil *bufio.Writer and takes the process down with it.
func (es *eventSourceTransport) drain() {
	es.writers.Wait()
	es.mut.Lock()
	defer es.mut.Unlock()
	if es.gzw != nil {
		es.gzw.abort()
		es.gzw = nil
	}
}

func (es *eventSourceTransport) IsConnected() bool {
	es.mut.Lock()
	defer es.mut.Unlock()
	return es.isConnected
}

func (es *eventSourceTransport) close() error {
	es.mut.Lock()
	defer es.mut.Unlock()
	if !es.isConnected {
		return nil
	}
	es.isConnected = false
	es.w = nil
	//the gzip writer is released by drain instead, once no writer goroutine
	//can still be compressing into it
	//unblocking the wait causes the HTTP handler to return
	close(es.connected)
	return nil
}
