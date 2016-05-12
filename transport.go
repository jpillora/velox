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

type wsTransport struct {
	conn *websocket.Conn
}

func (ws *wsTransport) connect(w http.ResponseWriter, r *http.Request) error {
	conn, err := defaultUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return fmt.Errorf("cannot upgrade connection: %s", err)
	}
	ws.conn = conn
	return nil
}

func (ws *wsTransport) send(upd *update) error {
	ws.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	return ws.conn.WriteJSON(upd)
}

func (ws *wsTransport) wait() error {
	//block on connection
	for {
		//msgType, msgBytes, err
		if _, _, err := ws.conn.ReadMessage(); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}
func (ws *wsTransport) close() error {
	return ws.conn.Close()
}

//=========================

type evtSrcTransport struct {
	enc         *eventsource.Encoder
	isConnected bool
	connected   chan struct{}
}

func (es *evtSrcTransport) connect(w http.ResponseWriter, r *http.Request) error {
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
	w.WriteHeader(http.StatusOK)
	//connection is now expecting a stream of events
	es.enc = eventsource.NewEncoder(w)
	return nil
}

func (es *evtSrcTransport) send(upd *update) error {
	b, err := json.Marshal(upd)
	if err != nil {
		return err
	}
	return es.enc.Encode(eventsource.Event{
		ID:   strconv.FormatInt(upd.Version, 10),
		Data: b,
	})
}

func (es *evtSrcTransport) wait() error {
	<-es.connected
	return nil
}

func (es *evtSrcTransport) close() error {
	if es.isConnected {
		//unblocking the wait, causes the http handler to return
		close(es.connected)
		es.isConnected = false
	}
	return nil
}
