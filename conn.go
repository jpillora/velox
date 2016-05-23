package velox

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
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
	id          int64
	addr        string
	first       uint32
	uptime      time.Time
	version     int64
	sendingMut  sync.Mutex
}

func newConn(id int64, addr string, state *State, version int64) *conn {
	return &conn{
		connectedCh: make(chan struct{}),
		id:          id,
		addr:        addr,
		state:       state,
		version:     version,
	}
}

//ID of this connection
func (c *conn) ID() string {
	return strconv.FormatInt(c.id, 10)
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
	//non-blocking connect to client over set transport
	if err := c.transport.connect(w, r); err != nil {
		return err
	}
	//successfully connected
	c.connected = true
	//while connected, ping loop (every 25s, browser timesout after 30s)
	go func() {
		for {
			select {
			case <-time.After(25 * time.Second):
				if err := c.send(&update{Ping: true}, c.state.SendTimeout); err != nil {
					goto disconnected
				}
			case <-c.connectedCh:
				goto disconnected
			}
		}
	disconnected:
		c.connected = false
		c.Close()
	}()
	//non-blocking wait on connection
	go func() {
		if err := c.transport.wait(); err != nil {
			//log error?
		}
		close(c.connectedCh)
	}()
	//now connected, consumer can connection.Wait()
	return nil
}

//send to connection, ensure only 1 concurrent sender
func (c *conn) send(upd *update, timeout time.Duration) error {
	c.sendingMut.Lock()
	defer c.sendingMut.Unlock()
	//send
	sent := make(chan error)
	go func() {
		sent <- c.transport.send(upd)
	}()
	//wait for timeout or sent
	select {
	case err := <-sent:
		if err != nil {
			return err
		}
	case <-time.After(timeout):
		//timeout
		return errors.New("send timed out")
	}
	return nil
}
