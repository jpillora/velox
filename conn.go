package velox

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

//Conn represents a single live connection being synchronised.
//ID is current set to the connection's remote address.
type Conn interface {
	ID() string
	Connected() bool
	Wait()
	Push()
	Close() error
}

type conn struct {
	transport   transport
	state       *State
	connected   bool
	connectedCh chan struct{}
	id          string
	first       uint32
	uptime      time.Time
	version     int64
	sendingMut  sync.Mutex
}

func newConn(id string, state *State, version int64) *conn {
	return &conn{
		connectedCh: make(chan struct{}),
		id:          id,
		state:       state,
		version:     version,
	}
}

//ID of this connection
func (c *conn) ID() string {
	return c.id
}

//Status of this connection, should be true initially, then false after Wait().
func (c *conn) Connected() bool {
	return c.connected
}

//Wait will block until the connection is closed.
func (c *conn) Wait() {
	<-c.connectedCh
}

//Push will the current state only to this client.
//Blocks until push is complete.
func (c *conn) Push() {
	c.state.pushTo(c)
}

//Force close the connection.
func (c *conn) Close() error {
	return c.transport.close()
}

//connect using the provided transport
//and block until successfully connected
func (c *conn) connect(w http.ResponseWriter, r *http.Request) error {
	//choose transport
	if r.Header.Get("Accept") == "text/event-stream" {
		c.transport = &evtSrcTransport{}
	} else if r.Header.Get("Upgrade") == "websocket" {
		c.transport = &wsTransport{}
	} else {
		return fmt.Errorf("Invalid sync request")
	}
	//connect to client over set transport
	connectingCh := make(chan error)
	go func() {
		//connect, and then:
		// * send on channel success/failed connection
		// * return when disconnected
		if err := c.transport.connect(w, r, connectingCh); err != nil {
			//transport error after connection TODO(jpillora): log optionally
		}
		//disconnected
		close(c.connectedCh)
	}()
	//wait here until transport completes connection
	if err := <-connectingCh; err != nil {
		return err
	}
	//successfully connected
	c.connected = true
	//while connected, ping loop (every 25s, browser timesout after 30s)
	go func() {
		for {
			select {
			case <-time.After(25 * time.Second):
				c.send(&update{Ping: true})
			case <-c.connectedCh:
				c.connected = false
				c.Close()
				return
			}
		}
	}()
	//now connected, consumer can connection.Wait()
	return nil
}

//send to connection, ensure only 1 concurrent sender
func (c *conn) send(upd *update) error {
	c.sendingMut.Lock()
	defer c.sendingMut.Unlock()
	return c.transport.send(upd)
}
