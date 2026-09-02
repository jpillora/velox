package velox

import (
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

// DefaultMaxWebSocketMessageSize is the maximum inbound WebSocket data frame
// accepted by a State unless State.MaxWebSocketMessageSize overrides it. The
// protocol has no client-to-server data messages beyond a tiny keepalive.
const DefaultMaxWebSocketMessageSize int64 = 1024

var defaultUpgrader = websocket.Upgrader{
	ReadBufferSize:    1024,
	WriteBufferSize:   1024,
	EnableCompression: true,
}

type websocketsTransport struct {
	writeTimeout   time.Duration
	maxMessageSize int64
	checkOrigin    func(*http.Request) bool
	conn           *websocket.Conn
}

func (ws *websocketsTransport) connect(w http.ResponseWriter, r *http.Request) error {
	// Copy before applying a State-specific policy: the package-level upgrader
	// is shared by concurrent connections and must retain its safe default.
	upgrader := defaultUpgrader
	if ws.checkOrigin != nil {
		upgrader.CheckOrigin = ws.checkOrigin
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrader writes its own HTTP status for every failed handshake (most
		// importantly a rejected Origin). Mark that response as committed so
		// State.ServeHTTP does not append a misleading 500 afterwards.
		return &responseCommittedError{err: fmt.Errorf("[velox] cannot upgrade connection: %w", err)}
	}
	conn.EnableWriteCompression(true)
	conn.SetCompressionLevel(gzip.BestSpeed)
	ws.conn = conn
	return nil
}

func (ws *websocketsTransport) send(upd *Update) error {
	ws.conn.SetWriteDeadline(time.Now().Add(ws.writeTimeout))
	return ws.conn.WriteJSON(upd)
}

func (ws *websocketsTransport) wait() error {
	// Inbound payloads are ignored; without a limit an unauthenticated peer can
	// make ReadMessage allocate its chosen size merely to keep the connection
	// alive. Gorilla applies this limit before handing the payload to us.
	limit := ws.maxMessageSize
	if limit <= 0 {
		limit = DefaultMaxWebSocketMessageSize
	}
	ws.conn.SetReadLimit(limit)
	//block on connection
	for {
		//ws is bi-directional, so we can rely on pings
		//from clients. currently hardcoded to 25s so timeout
		//after 30s.
		ws.conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		if _, _, err := ws.conn.ReadMessage(); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// drain is a no-op: the upgrade hands this transport its own net.Conn, so it
// never touches the http.ResponseWriter after connect returns.
func (ws *websocketsTransport) drain() {}

func (ws *websocketsTransport) close() error {
	return ws.conn.Close()
}
