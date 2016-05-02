package velox

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/donovanhide/eventsource"
	"github.com/gorilla/websocket"
)

type transport interface {
	connect(w http.ResponseWriter, r *http.Request, connectingCh chan error) error
	send(upd *update) error
	close() error
}

//=========================

var defaultUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type wsTransport struct {
	conn *websocket.Conn
}

func (ws *wsTransport) connect(w http.ResponseWriter, r *http.Request, connectingCh chan error) error {
	conn, err := defaultUpgrader.Upgrade(w, r, nil)
	if err != nil {
		connectingCh <- err
		return fmt.Errorf("cannot upgrade connection: %s", err)
	}
	ws.conn = conn
	connectingCh <- nil
	//block on connection
	for {
		//msgType, msgBytes, err
		if _, _, err := conn.ReadMessage(); err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
	}
	return nil
}

func (ws *wsTransport) send(upd *update) error {
	ws.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	return ws.conn.WriteJSON(upd)
}

func (ws *wsTransport) close() error {
	return ws.conn.Close()
}

//=========================

type evtSrcTransport struct {
	s *eventsource.Server
}

//evtSrcWriter is a hack to
//watch when connection is ready
type evtSrcWriter struct {
	isFlushed    bool
	connectingCh chan error
	http.ResponseWriter
}

func (ew *evtSrcWriter) CloseNotify() <-chan bool {
	return ew.ResponseWriter.(http.CloseNotifier).CloseNotify()
}

//HACK: uses eventsource's call to flush as a signal that its connected
func (ew *evtSrcWriter) Flush() {
	if !ew.isFlushed {
		ew.connectingCh <- nil
	}
	ew.isFlushed = true
	f, ok := ew.ResponseWriter.(http.Flusher)
	if ok {
		f.Flush()
	}
}

func (es *evtSrcTransport) connect(w http.ResponseWriter, r *http.Request, connectingCh chan error) error {
	es.s = eventsource.NewServer()
	if !strings.Contains(w.Header().Get("Content-Encoding"), "gzip") {
		es.s.Gzip = true
	}
	ew := &evtSrcWriter{false, connectingCh, w}
	es.s.Handler("events").ServeHTTP(ew, r)
	return nil
}

func (es *evtSrcTransport) send(upd *update) error {
	es.s.Publish([]string{"events"}, upd)
	return nil
}

func (es *evtSrcTransport) close() error {
	if es.s != nil {
		es.s.Close()
	}
	return nil
}
