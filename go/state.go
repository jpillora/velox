package velox

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Pusher implements a push method,
// similar to Flush
type Pusher interface {
	Push() bool
}

var (
	//MinThrottle is the minimum manual State.Throttle value.
	//15ms is approximately highest resolution on the JS eventloop.
	MinThrottle = 15 * time.Millisecond
	//DefaultThrottle is the default State.Throttle value.
	DefaultThrottle = 200 * time.Millisecond
	//DefaultWriteTimeout is the default State.Throttle value.
	DefaultWriteTimeout = 30 * time.Second
	//DefaultPingInterval is the default State.PingInterval value.
	DefaultPingInterval = 25 * time.Second
)

// State must be embedded into a struct to make it syncable.
type State struct {
	//configuration
	Locker       sync.Locker   `json:"-"` // Locker optionally overrides the lock used during marshal/unmarshal.
	Data         MarshalFunc   `json:"-"` // Data is called each Push to get the current state of the object.
	Throttle     time.Duration `json:"-"` // Throttle is the minimum time between pushes.
	WriteTimeout time.Duration `json:"-"` // WriteTimeout is the maximum time to wait for a write to complete.
	PingInterval time.Duration `json:"-"` // PingInterval is the time between pings to the client.
	Debug        bool          `json:"-"` // Debug is used to enable debug logging.
	//internal state
	initMut sync.Mutex
	initd   bool
	connMut sync.Mutex
	conns   map[int64]*conn
	data struct {
		mut     sync.RWMutex
		id      string //data id != conn id
		bytes   []byte
		delta   []byte
		version int64
		patcher mergePatcher // caches unmarshaled prev state
	}
	push struct {
		mut    sync.Mutex
		ing    uint32
		queued uint32
	}
}

func (s *State) init() error {
	s.initMut.Lock()
	defer s.initMut.Unlock()
	if s.initd {
		return nil
	}
	s.initd = true
	if s.Throttle < MinThrottle {
		s.Throttle = DefaultThrottle
	}
	if s.WriteTimeout == 0 {
		s.WriteTimeout = DefaultWriteTimeout
	}
	if s.PingInterval == 0 {
		s.PingInterval = DefaultPingInterval
	}
	if s.Data == nil {
		return fmt.Errorf("no data function provided")
	}
	//get initial JSON bytes and confirm gostruct is marshallable
	b, _ := s.Data()
	// set data fields
	s.data.mut.Lock()
	s.data.bytes = b
	// seed the merge patcher cache with the initial state
	s.data.patcher.patch(b)
	id := make([]byte, 4)
	if n, _ := rand.Read(id); n > 0 {
		s.data.id = hex.EncodeToString(id)
	}
	s.data.version = 1
	s.data.mut.Unlock()
	// set connection fields
	s.connMut.Lock()
	s.conns = map[int64]*conn{}
	s.connMut.Unlock()
	return nil
}

func (s *State) self() *State {
	return s
}

func (s *State) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := s.Handle(w, r)
	if err != nil {
		log.Printf("velox: serve: %s", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	conn.Wait()
}

func (state *State) Handle(w http.ResponseWriter, r *http.Request) (Conn, error) {
	if err := state.init(); err != nil {
		return nil, fmt.Errorf("init: %w", err)
	}
	version := int64(0)
	//matching id, allow user to pick version
	if id := r.URL.Query().Get("id"); id != "" && id == state.data.id {
		if v, err := strconv.ParseInt(r.URL.Query().Get("v"), 10, 64); err == nil && v > 0 {
			version = v
		}
	}
	//set initial connection state
	conn := newConn(atomic.AddInt64(&connectionID, 1), r.RemoteAddr, state, version)
	//attempt connection over transport
	//(negotiate websockets / start eventsource emitter)
	//return when connected
	if err := conn.connect(w, r); err != nil {
		return nil, fmt.Errorf("velox connection failed: %s", err)
	}
	//hand over to state to keep in sync
	state.subscribe(conn)
	//do an initial push only to this client
	conn.Push()
	//pass connection to user
	return conn, nil
}

// ID uniquely identifies this state object
func (s *State) ID() string {
	return s.data.id
}

// Version of this state object (when the underlying struct is
// and a Push is performed, this version number is incremented).
func (s *State) Version() int64 {
	return s.data.version
}

func (s *State) subscribe(conn *conn) {
	//subscribe
	conn.waiter.Add(1)
	s.connMut.Lock()
	s.conns[conn.id] = conn
	s.connMut.Unlock()
	//and then unsubscribe on close
	go func() {
		<-conn.connectedCh //this unblocks before wait
		s.connMut.Lock()
		delete(s.conns, conn.id)
		s.connMut.Unlock()
		conn.waiter.Done()
	}()
}

// NumConnections currently active
func (s *State) NumConnections() int {
	s.connMut.Lock()
	n := len(s.conns)
	s.connMut.Unlock()
	return n
}

// Push the changes from this object to all connected clients.
// Push is thread-safe and is throttled so it can be called
// with abandon. Returns false if a Push is already in progress.
func (s *State) Push() bool {
	if s.Data == nil {
		return false
	}
	//attempt to mark state as 'pushing'
	if atomic.CompareAndSwapUint32(&s.push.ing, 0, 1) {
		if s.Debug {
			log.Printf("velox: Push() starting new push")
		}
		go s.gopush()
		return true
	}
	//if already pushing, mark queued
	if s.Debug {
		log.Printf("velox: Push() already pushing, marking queued")
	}
	atomic.StoreUint32(&s.push.queued, 1)
	return false
}

// non-blocking push
func (s *State) gopush() {
	s.push.mut.Lock()
	t0 := time.Now()
	//queue cleanup
	defer func() {
		//measure time passed, ensure we wait at least Throttle time
		tdelta := time.Since(t0)
		if t := s.Throttle - tdelta; t > 0 {
			time.Sleep(t)
		}
		//push complete
		s.push.mut.Unlock()
		atomic.StoreUint32(&s.push.ing, 0)
		//if queued, auto-push again
		if atomic.CompareAndSwapUint32(&s.push.queued, 1, 0) {
			s.Push()
		}
	}()
	//calculate new json state
	newBytes, err := s.Data()
	if err != nil {
		log.Printf("velox: marshal failed: %s", err)
		return
	}
	if s.Debug {
		log.Printf("velox: gopush marshaled %d bytes", len(newBytes))
	}
	s.data.mut.Lock()
	changed := false
	if bytes.Equal(newBytes, []byte("null")) {
		// special case, clear data
		s.data.bytes = nil
		s.data.delta = nil
		changed = true
	} else {
		// ensure non-nil
		if s.data.bytes == nil {
			s.data.bytes = []byte(`{}`)
		}
		// steps to go from local to remote, capture changes
		delta, err := s.data.patcher.patch(newBytes)
		if err != nil {
			panic(fmt.Errorf("create-patch: %w", err))
		}
		// if changed,
		if !bytes.Equal(delta, []byte(`{}`)) && len(delta) > 0 {
			// then calculate change set from last version
			// NOTE: patch may contain references to localStruct
			s.data.delta = delta
			s.data.bytes = newBytes
			changed = true
			if s.Debug {
				log.Printf("velox: gopush changed, delta=%s", string(delta))
			}
		} else if s.Debug {
			log.Printf("velox: gopush no change detected")
		}
	}
	// bump if changed
	if changed {
		s.data.version++
	}
	dversion := s.data.version
	s.data.mut.Unlock()
	//send this new change to each subscriber
	s.connMut.Lock()
	for _, c := range s.conns {
		if c.Version() != dversion {
			go c.Push()
		}
	}
	s.connMut.Unlock()
	//defered cleanup()
}
