package velox

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/donovanhide/eventsource"
	"github.com/gorilla/websocket"
)

//Conn represents a single live connection being synchronised.
//Its ID is the the connection's remote address.
type Conn interface {
	ID() string
	Connected() bool
	Wait()
	Close() error
}

type transport interface {
	connect(w http.ResponseWriter, r *http.Request, isConnected chan bool) error
	send(upd *update) error
	close() error
}

type conn struct {
	transport
	connected bool
	id        string
	uptime    time.Time
	version   int64
	waiter    sync.WaitGroup
}

func (c *conn) ID() string {
	return c.id
}

func (c *conn) Connected() bool {
	return c.connected
}

//Wait will block until the connection is closed.
func (c *conn) Wait() {
	c.waiter.Wait()
}

//Force close the connection.
func (c *conn) Close() error {
	return c.transport.close()
}

//=========================

var defaultUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type wsTrans struct {
	conn *websocket.Conn
}

func (ws *wsTrans) connect(w http.ResponseWriter, r *http.Request, isConnected chan bool) error {
	conn, err := defaultUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return fmt.Errorf("cannot upgrade connection: %s", err)
	}
	ws.conn = conn
	isConnected <- true
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

func (ws *wsTrans) send(upd *update) error {
	return ws.conn.WriteJSON(upd)
}

func (ws *wsTrans) close() error {
	return ws.conn.Close()
}

//=========================

type evtSrcTrans struct {
	s *eventsource.Server
}

func (es *evtSrcTrans) connect(w http.ResponseWriter, r *http.Request, isConnected chan bool) error {
	es.s = eventsource.NewServer()
	if !strings.Contains(w.Header().Get("Content-Encoding"), "gzip") {
		es.s.Gzip = true
	}
	isConnected <- true
	es.s.Handler("events").ServeHTTP(w, r)
	return nil
}

func (es *evtSrcTrans) send(upd *update) error {
	es.s.Publish([]string{"events"}, upd)
	return nil
}

func (es *evtSrcTrans) close() error {
	es.s.Close()
	return nil
}
