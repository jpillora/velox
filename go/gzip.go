package velox

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"sync"
)

// gzipWriterPool pools gzip.Writer instances to reduce allocations.
var gzipWriterPool = sync.Pool{
	New: func() any {
		w, _ := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed)
		return w
	},
}

// gzipResponseWriter wraps an http.ResponseWriter, compressing all written
// data. It implements http.Flusher so that eventsource.WriteEvent's Flush()
// call propagates through the gzip layer to the underlying transport.
type gzipResponseWriter struct {
	http.ResponseWriter
	gz      *gzip.Writer
	flusher http.Flusher
}

func (g *gzipResponseWriter) Write(p []byte) (int, error) {
	return g.gz.Write(p)
}

// Flush flushes the gzip compressor and then the underlying transport.
// Without this, compressed bytes would be buffered and SSE events would
// not be delivered in real-time.
func (g *gzipResponseWriter) Flush() {
	g.gz.Flush()
	if g.flusher != nil {
		g.flusher.Flush()
	}
}

// close finalizes the gzip stream and returns the writer to the pool.
func (g *gzipResponseWriter) close() {
	g.gz.Close()
	gzipWriterPool.Put(g.gz)
}

// gzipReadCloser wraps a gzip.Reader and the underlying body so that
// closing the wrapper closes both.
type gzipReadCloser struct {
	gzReader *gzip.Reader
	body     io.ReadCloser
}

func (g *gzipReadCloser) Read(p []byte) (int, error) {
	return g.gzReader.Read(p)
}

func (g *gzipReadCloser) Close() error {
	err1 := g.gzReader.Close()
	err2 := g.body.Close()
	if err1 != nil {
		return err1
	}
	return err2
}

// acceptsGzip returns true if the request includes gzip in Accept-Encoding.
func acceptsGzip(r *http.Request) bool {
	for _, enc := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		if strings.TrimSpace(enc) == "gzip" {
			return true
		}
	}
	return false
}
