package velox

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/bernerdschaefer/eventsource"
	"github.com/gorilla/websocket"
)

//a single update
type update struct {
	ID      string          `json:"id,omitempty"`
	Ping    bool            `json:"ping,omitempty"`
	Delta   bool            `json:"delta,omitempty"`
	Version int64           `json:"version,omitempty"` //53 usable bits
	Body    json.RawMessage `json:"body,omitempty"`
}

type transport interface {
	connect(w http.ResponseWriter, r *http.Request) error
	send(upd *update) error
	wait() error
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

type websocketsTransport struct {
	writeTimeout time.Duration
	conn         *websocket.Conn
}

func (ws *websocketsTransport) connect(w http.ResponseWriter, r *http.Request) error {
	conn, err := defaultUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return fmt.Errorf("[velox] cannot upgrade connection: %s", err)
	}
	ws.conn = conn
	return nil
}

func (ws *websocketsTransport) send(upd *update) error {
	ws.conn.SetWriteDeadline(time.Now().Add(ws.writeTimeout))
	return ws.conn.WriteJSON(upd)
}

func (ws *websocketsTransport) wait() error {
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
func (ws *websocketsTransport) close() error {
	return ws.conn.Close()
}

//=========================

type eventSourceTransport struct {
	writeTimeout time.Duration
	enc          *eventsource.Encoder
	flusher      http.Flusher
	isConnected  bool
	connected    chan struct{}
}

func (es *eventSourceTransport) connect(w http.ResponseWriter, r *http.Request) error {
	notifier, ok := w.(http.CloseNotifier)
	if !ok {
		return errors.New("underlying writer must be an http.CloseNotifier")
	}
	//connection controls
	es.isConnected = true
	es.connected = make(chan struct{})
	go func() {
		select {
		case <-es.connected:
		case <-notifier.CloseNotify(): //client disconnected early
			es.close()
		}
	}()
	//eventsource headers
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Vary", "Accept")
	w.Header().Set("Content-Type", "text/event-stream")
	//connection is now expecting a stream of events
	es.enc = eventsource.NewEncoder(w)
	return nil
}

func (es *eventSourceTransport) send(upd *update) error {
	b, err := json.Marshal(upd)
	if err != nil {
		return err
	}
	sent := make(chan error)
	go func() {
		sent <- es.enc.Encode(eventsource.Event{
			ID:   strconv.FormatInt(upd.Version, 10),
			Data: b,
		})
	}()
	select {
	case <-time.After(es.writeTimeout):
		return errors.New("timeout")
	case err := <-sent:
		return err
	}
}

func (es *eventSourceTransport) wait() error {
	<-es.connected
	return nil
}

func (es *eventSourceTransport) close() error {
	if es.isConnected {
		//unblocking the wait, causes the http handler to return
		close(es.connected)
		es.isConnected = false
	}
	return nil
}
