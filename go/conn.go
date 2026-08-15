package velox

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Conn represents a single live connection being synchronised.
// ID is current set to the connection's remote address.
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
	connectedAt time.Time
	connectedCh chan struct{}
	waiter      sync.WaitGroup
	id          int64
	addr        string
	first       uint32
	pushing     uint32
	queued      uint32
	sendVerMut  sync.Mutex // serialises send, protects version and baseHash
	version     int64
	// proto is the protocol the client asked for. Anything below 3 — including
	// a client that asked for nothing — is served v2 unchanged.
	proto int
	// baseHash is the opaque root the client currently holds. It survives a
	// reconnect because the client sends it back as the "h" query parameter,
	// which is what turns a page reload into a patch instead of a snapshot.
	baseHash string
}

func newConn(id int64, addr string, state *State, version int64, proto int, baseHash string) *conn {
	return &conn{
		connectedCh: make(chan struct{}),
		id:          id,
		addr:        addr,
		state:       state,
		version:     version,
		proto:       proto,
		baseHash:    baseHash,
	}
}

// ID of this connection
func (c *conn) ID() string {
	return strconv.FormatInt(c.id, 10)
}

// Status of this connection, should be true initially, then false after Wait().
func (c *conn) Connected() bool {
	return c.connected
}

// Read the current version
func (c *conn) Version() int64 {
	c.sendVerMut.Lock()
	defer c.sendVerMut.Unlock()
	return c.version
}

// Wait will block until the connection is closed and nothing is still writing
// to it. The HTTP handler must not return until this returns.
func (c *conn) Wait() {
	c.waiter.Wait()
	c.transport.drain()
}

// Force close the connection.
func (c *conn) Close() error {
	return c.transport.close()
}

// connect using the provided transport
// and block until connection is ready
func (c *conn) connect(w http.ResponseWriter, r *http.Request) error {
	//choose transport
	if c.state.transportFactory != nil {
		c.transport = c.state.transportFactory(r)
	} else if r.Header.Get("Accept") == "text/event-stream" {
		c.transport = &eventSourceTransport{writeTimeout: c.state.WriteTimeout}
	} else if r.Header.Get("Upgrade") == "websocket" {
		c.transport = &websocketsTransport{writeTimeout: c.state.WriteTimeout}
	} else {
		return fmt.Errorf("invalid sync request")
	}
	//non-blocking connect to client over set transport
	if err := c.transport.connect(w, r); err != nil {
		return err
	}
	//initial ping
	if err := c.send(&Update{Ping: true}); err != nil {
		c.transport.close()
		//nobody reaches conn.Wait() on this path, so drain here instead
		c.transport.drain()
		return &responseCommittedError{err: fmt.Errorf("failed to send initial event: %w", err)}
	}
	//successfully connected
	c.connected = true
	c.connectedAt = time.Now()
	c.waiter.Add(1)
	//while connected, ping loop (every 25s, browser timesout after 30s)
	go func() {
		for {
			select {
			case <-time.After(c.state.PingInterval):
				if err := c.send(&Update{Ping: true}); err != nil {
					goto disconnected
				}
			case <-c.connectedCh:
				goto disconnected
			}
		}
	disconnected:
		c.connected = false
		c.Close()
		//unblock waiters
		c.waiter.Done()
	}()
	//non-blocking wait on connection
	go func() {
		//log error?
		c.transport.wait()
		close(c.connectedCh)
	}()
	//now connected, consumer can connection.Wait()
	return nil
}

// Push will the current state only to this client.
// Blocks until push is complete.
func (c *conn) Push() {
	if c.state.Debug {
		log.Printf("velox: conn[%d] Push() called, connVersion=%d", c.id, c.Version())
	}
	//attempt to mark state as 'pushing'
	if !atomic.CompareAndSwapUint32(&c.pushing, 0, 1) {
		//if already pushing, mark queued
		if c.state.Debug {
			log.Printf("velox: conn[%d] Push() already pushing, marking queued", c.id)
		}
		atomic.StoreUint32(&c.queued, 1)
		return
	}
	defer func() {
		//no longer pushing
		atomic.StoreUint32(&c.pushing, 0)
		//queued? dequeue and push again
		if atomic.CompareAndSwapUint32(&c.queued, 1, 0) {
			c.Push() //within same goroutine
		}
	}()
	//current state data
	d := &c.state.data
	d.mut.RLock()
	if c.Version() == d.version {
		d.mut.RUnlock()
		if c.state.Debug {
			log.Printf("velox: conn[%d] already at version %d, skipping", c.id, d.version)
		}
		return
	}
	update := &Update{Version: d.version}
	//first push? include id
	if atomic.CompareAndSwapUint32(&c.first, 0, 1) {
		update.ID = d.id
		if c.proto >= 3 {
			update.Proto = ProtoVersion
		}
	}
	//choose optimal update (send the smallest)
	if c.proto >= 3 {
		update.Root = d.rootHash
		//under v3 the root hash, not the version, says where a client is. A state
		//that changes and changes back returns to a root some client already
		//holds, and that client needs nothing but the version to catch up —
		//comparing versions instead would call it stale and resend the document.
		if c.baseHash != "" && c.baseHash == d.rootHash {
			update.Base = c.baseHash
			update.Ops = json.RawMessage(`[]`)
		} else if ops, ok := c.state.opsFor(c.baseHash); ok && len(ops) < len(d.bytes) {
			//a client whose base is still retained gets operations against it,
			//however many versions behind it has fallen; anything else, including
			//a base evicted from the history, falls back to the whole document
			update.Base = c.baseHash
			update.Ops = ops
		} else {
			update.Body = d.bytes
		}
	} else if d.delta != nil &&
		c.Version() == (d.version-1) &&
		len(d.bytes) > 0 &&
		len(d.delta) < len(d.bytes) {
		update.Delta = true
		update.Body = d.delta
	} else {
		update.Delta = false
		update.Body = d.bytes
	}
	d.mut.RUnlock()
	//unlock data and send!
	if c.state.Debug {
		log.Printf("velox: conn[%d] sending version=%d delta=%v bodyLen=%d", c.id, update.Version, update.Delta, len(update.Body))
	}
	if err := c.send(update); err != nil {
		log.Printf("velox: send failed: %s", err)
		c.Close()
		return
	}
}

// send to connection, ensure only 1 concurrent sender
func (c *conn) send(upd *Update) error {
	c.sendVerMut.Lock()
	defer c.sendVerMut.Unlock()
	//send (transports responsiblity to enforce timeouts)
	if err := c.transport.send(upd); err != nil {
		return err
	}
	// mark new current version
	c.version = upd.Version
	if upd.Root != "" {
		c.baseHash = upd.Root
	}
	return nil
}
