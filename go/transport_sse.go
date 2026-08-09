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
	gzw             *gzipResponseWriter // non-nil if gzip is active
	writerAbandoned bool                // a timed-out child may still be using w
	isConnected     bool
	connected       chan struct{}
}

func (es *eventSourceTransport) connect(w http.ResponseWriter, r *http.Request) error {
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
	// TODO: improve this to not use a goroutine
	// instead it should hijack and use a tcp write-timeout
	// buffered so an abandoned (timed-out) writer goroutine can
	// deposit its result and exit instead of blocking forever
	sent := make(chan error, 1)
	go func() {
		err := eventsource.WriteEvent(writer, eventsource.Event{
			ID:   strconv.FormatInt(upd.Version, 10),
			Data: b,
		})
		sent <- err
	}()
	select {
	case <-time.After(es.writeTimeout):
		es.writerAbandoned = true
		// don't return buf to pool; goroutine may still be writing
		return errors.New("timeout")
	case err := <-sent:
		encodePool.Put(buf)
		return err
	}
}

func (es *eventSourceTransport) wait() error {
	<-es.connected
	return nil
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
	if es.gzw != nil {
		if !es.writerAbandoned {
			es.gzw.abort()
		}
		es.gzw = nil
	}
	es.w = nil
	//unblocking the wait causes the HTTP handler to return
	close(es.connected)
	return nil
}
