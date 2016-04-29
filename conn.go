package velox

import (
	"errors"
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
	Push()
	Close() error
}

//NOTE transport should only be used in this file!
type transport interface {
	connect(w http.ResponseWriter, r *http.Request, isConnected chan error) error
	send(upd *update) error
	close() error
}

type conn struct {
	transport  transport
	state      *State
	connected  bool
	id         string
	first      uint32
	uptime     time.Time
	version    int64
	sendingMut sync.Mutex
	waiter     sync.WaitGroup
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

//Push will the current state only to this client.
//Blocks until push is complete.
func (c *conn) Push() {
	c.state.pushTo(c)
}

//Force close the connection.
func (c *conn) Close() error {
	if c.transport != nil {
		err := c.transport.close()
		c.connected = false
		c.transport = nil
		return err
	}
	return nil
}

//connect using the provided transport
//and block until successfully connected
func (c *conn) connect(w http.ResponseWriter, r *http.Request) error {
	//choose transport
	if r.Header.Get("Accept") == "text/event-stream" {
		c.transport = &evtSrcTrans{}
	} else if r.Header.Get("Upgrade") == "websocket" {
		c.transport = &wsTrans{}
	} else {
		return fmt.Errorf("Invalid sync request")
	}
	//connect to client over set transport
	c.waiter.Add(1)
	isConnected := make(chan error)
	go func() {
		//connect, and then:
		// * send on channel success/failed connection
		// * return when disconnected
		if err := c.transport.connect(w, r, isConnected); err != nil {
			//transport error after connection TODO(jpillora): log optionally
		}
		//disconnected, done waiting
		c.Close()
		c.waiter.Done()
	}()
	//wait here until transport completes connection
	if err := <-isConnected; err != nil {
		return err
	}
	//once successfully connected, ping loop (every 25s, browser timesout after 30s)
	c.connected = true
	go func() {
		for {
			time.Sleep(25 * time.Second)
			if !c.connected {
				return
			}
			c.send(&update{Ping: true})
		}
	}()
	//now connected, consumer should connection.Wait()
	return nil
}

//send to connection, ensure only 1 concurrent sender
func (c *conn) send(upd *update) error {
	if c.transport == nil {
		return errors.New("not connected")
	}
	c.sendingMut.Lock()
	defer c.sendingMut.Unlock()
	return c.transport.send(upd)
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

func (ws *wsTrans) connect(w http.ResponseWriter, r *http.Request, isConnected chan error) error {
	conn, err := defaultUpgrader.Upgrade(w, r, nil)
	if err != nil {
		isConnected <- err
		return fmt.Errorf("cannot upgrade connection: %s", err)
	}
	ws.conn = conn
	isConnected <- nil
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

//evtSrcWriter is a hack to
//watch when connection is ready
type evtSrcWriter struct {
	isFlushed   bool
	isConnected chan error
	http.ResponseWriter
}

func (ew *evtSrcWriter) CloseNotify() <-chan bool {
	return ew.ResponseWriter.(http.CloseNotifier).CloseNotify()
}

//HACK: uses eventsource's call to flush as a signal that its connected
func (ew *evtSrcWriter) Flush() {
	if !ew.isFlushed {
		ew.isConnected <- nil
	}
	ew.isFlushed = true
	f, ok := ew.ResponseWriter.(http.Flusher)
	if ok {
		f.Flush()
	}
}

func (es *evtSrcTrans) connect(w http.ResponseWriter, r *http.Request, isConnected chan error) error {
	es.s = eventsource.NewServer()
	if !strings.Contains(w.Header().Get("Content-Encoding"), "gzip") {
		es.s.Gzip = true
	}
	ew := &evtSrcWriter{false, isConnected, w}
	es.s.Handler("events").ServeHTTP(ew, r)
	return nil
}

func (es *evtSrcTrans) send(upd *update) error {
	es.s.Publish([]string{"events"}, upd)
	return nil
}

func (es *evtSrcTrans) close() error {
	if es.s != nil {
		es.s.Close()
	}
	return nil
}
