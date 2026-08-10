package velox

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
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
	initd   atomic.Bool
	connMut sync.Mutex
	conns   map[int64]*conn
	// transportFactory is a test seam for deterministic transport failures.
	transportFactory func(*http.Request) transport
	data             struct {
		mut     sync.RWMutex
		id      string //data id != conn id
		bytes   []byte
		delta   []byte
		version int64
		cleared bool         // clients hold no document: state was null or never published
		patcher mergePatcher // owns the raw previous state for merge patches
	}
	push struct {
		mut        sync.Mutex
		ing        uint32
		queued     uint32
		generation atomic.Uint64
		refreshed  atomic.Uint64
	}
}

func (s *State) init() error {
	if s.initd.Load() {
		return nil
	}
	s.initMut.Lock()
	defer s.initMut.Unlock()
	if s.initd.Load() {
		return nil
	}
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
	// Capture the generation before marshaling. A concurrent Push advances it,
	// leaving this initial snapshot stale for the push worker or subscriber.
	generation := s.push.generation.Load()
	//get initial JSON bytes and confirm gostruct is marshallable
	b, err := s.Data()
	// set data fields
	s.data.mut.Lock()
	// seed the merge patcher cache with the initial state
	if err == nil {
		b = bytes.Clone(b)
		_, err = s.data.patcher.patch(b)
	}
	if err != nil {
		// Nothing usable was published, so treat clients as holding no document.
		// The first successful refresh then sends a full snapshot: diffing a
		// valid state against an empty patcher cache yields {}, which reports no
		// change and would strand every client on the empty document forever.
		log.Printf("velox: initial marshal failed: %s", err)
		b = nil
		s.data.cleared = true
		// Ensure the first subscriber retries Data even if no explicit Push
		// occurs between this failure and the connection attempt.
		s.push.generation.Add(1)
	}
	s.data.bytes = b
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
	s.push.refreshed.Store(generation)
	s.initd.Store(true)
	return nil
}

func (s *State) self() *State {
	return s
}

func (s *State) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := s.Handle(w, r)
	if err != nil {
		log.Printf("velox: serve: %s", err)
		var committed *responseCommittedError
		if errors.As(err, &committed) {
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	conn.Wait()
}

// responseCommittedError marks failures after a transport has already
// committed the HTTP response or hijacked the connection.
type responseCommittedError struct {
	err error
}

func (e *responseCommittedError) Error() string { return e.err.Error() }
func (e *responseCommittedError) Unwrap() error { return e.err }

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
		return nil, fmt.Errorf("velox connection failed: %w", err)
	}
	//hand over to state to keep in sync
	if err := state.subscribe(conn); err != nil {
		conn.Close()
		conn.Wait()
		return nil, &responseCommittedError{err: fmt.Errorf("velox initial refresh failed: %w", err)}
	}
	//do an initial push only to this client
	conn.Push()
	//pass connection to user
	return conn, nil
}

// ID uniquely identifies this state object
func (s *State) ID() string {
	s.data.mut.RLock()
	defer s.data.mut.RUnlock()
	return s.data.id
}

// Version of this state object (when the underlying struct is
// and a Push is performed, this version number is incremented).
// refresh writes version under data.mut, so reads must be guarded.
func (s *State) Version() int64 {
	s.data.mut.RLock()
	defer s.data.mut.RUnlock()
	return s.data.version
}

func (s *State) subscribe(conn *conn) error {
	// Insert first, then repair an idle-stale cache if necessary. Push marks
	// the cache stale synchronously, so a push racing this insertion is either
	// observed here or treats this connection as active itself.
	conn.waiter.Add(1)
	s.connMut.Lock()
	s.conns[conn.id] = conn
	s.connMut.Unlock()

	var changed bool
	if s.cacheStale() {
		s.push.mut.Lock()
		var err error
		changed, err = s.refreshStale()
		s.push.mut.Unlock()
		if err != nil {
			s.connMut.Lock()
			delete(s.conns, conn.id)
			s.connMut.Unlock()
			conn.waiter.Done()
			return err
		}
	}
	if changed {
		s.pushConnections(conn)
	}
	//and then unsubscribe on close
	go func() {
		<-conn.connectedCh //this unblocks before wait
		s.connMut.Lock()
		delete(s.conns, conn.id)
		s.connMut.Unlock()
		conn.waiter.Done()
	}()
	return nil
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
	// Publish staleness before starting the worker so a subscriber racing an
	// idle push cannot send the previous cached snapshot.
	s.push.generation.Add(1)
	return s.startPush()
}

// startPush schedules work for an already-recorded generation. It is also
// used to drain the coalesced queue without inventing another stale state.
func (s *State) startPush() bool {
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
	s.init()
	s.push.mut.Lock()
	var t0 time.Time
	//queue cleanup
	defer func() {
		var wait time.Duration
		// Active pushes are throttled. Idle pushes return immediately.
		if !t0.IsZero() {
			tdelta := time.Since(t0)
			wait = s.Throttle - tdelta
		}
		// Throttling is enforced by push.ing; push.mut only serialises cache
		// refreshes and must not block new subscribers during the sleep.
		s.push.mut.Unlock()
		if wait > 0 {
			time.Sleep(wait)
		}
		//push complete
		atomic.StoreUint32(&s.push.ing, 0)
		//if queued, auto-push again
		if atomic.CompareAndSwapUint32(&s.push.queued, 1, 0) {
			s.startPush()
		}
	}()
	if s.NumConnections() == 0 {
		return
	}
	if s.cacheStale() {
		t0 = time.Now()
	}
	changed, err := s.refreshStale()
	if err != nil {
		log.Printf("velox: refresh failed: %s", err)
		return
	}
	if changed {
		s.pushConnections(nil)
	}
	//defered cleanup()
}

// refreshStale refreshes at most once for all Push calls observed before it.
// A Push racing Data sets stale again and is handled by the queued worker.
// The caller must hold push.mut.
func (s *State) refreshStale() (changed bool, err error) {
	generation := s.push.generation.Load()
	if s.push.refreshed.Load() == generation {
		return false, nil
	}
	changed, err = s.refresh()
	if err == nil {
		// Publish freshness only after refresh has published the cache. If a
		// Push raced the marshal, generation has advanced and remains stale.
		s.push.refreshed.Store(generation)
	}
	return changed, err
}

func (s *State) cacheStale() bool {
	return s.push.refreshed.Load() != s.push.generation.Load()
}

// pushConnections schedules out-of-date connections without holding any lock
// across network I/O. except is used for a new subscriber whose initial Push
// is performed synchronously by Handle.
func (s *State) pushConnections(except *conn) {
	s.data.mut.RLock()
	dversion := s.data.version
	s.data.mut.RUnlock()
	s.connMut.Lock()
	conns := make([]*conn, 0, len(s.conns))
	for _, c := range s.conns {
		if c != except {
			conns = append(conns, c)
		}
	}
	s.connMut.Unlock()
	for _, c := range conns {
		go func() {
			if c.Version() != dversion {
				c.Push()
			}
		}()
	}
}

// refresh synchronously updates the marshaled state and merge-patch cache.
// The caller is responsible for serialising refreshes with push.mut.
func (s *State) refresh() (changed bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			changed = false
			err = fmt.Errorf("data panic: %v", recovered)
		}
	}()
	//calculate new json state
	newBytes, err := s.Data()
	if err != nil {
		return false, err
	}
	if s.Debug {
		log.Printf("velox: gopush marshaled %d bytes", len(newBytes))
	}
	s.data.mut.Lock()
	defer s.data.mut.Unlock()
	changed = false
	if isJSONNull(newBytes) {
		// special case, clear data
		s.data.bytes = nil
		s.data.delta = nil
		s.data.cleared = true
		changed = true
	} else if s.data.bytes != nil && s.data.patcher.prev != nil && bytes.Equal(newBytes, s.data.bytes) {
		if s.Debug {
			log.Printf("velox: gopush no change detected")
		}
	} else {
		// steps to go from local to remote, capture changes
		delta, err := s.data.patcher.patch(newBytes)
		if err != nil {
			return false, fmt.Errorf("create-patch: %w", err)
		}
		if s.data.cleared {
			// Clients have no document to patch after a null state. Publish the
			// restored object as a full snapshot, even if it matches the cache
			// from before the clear.
			s.data.bytes = bytes.Clone(newBytes)
			s.data.delta = nil
			s.data.cleared = false
			changed = true
		} else {
			// ensure non-nil after the patch has been validated
			if s.data.bytes == nil {
				s.data.bytes = []byte(`{}`)
			}
			// if changed,
			if !bytes.Equal(delta, []byte(`{}`)) && len(delta) > 0 {
				// then calculate change set from last version
				// NOTE: patch may contain references to localStruct
				s.data.delta = delta
				s.data.bytes = bytes.Clone(newBytes)
				changed = true
				if s.Debug {
					log.Printf("velox: gopush changed, delta=%s", string(delta))
				}
			} else if s.Debug {
				log.Printf("velox: gopush no change detected")
			}
		}
	}
	// bump if changed
	if changed {
		s.data.version++
	}
	return changed, nil
}

func isJSONNull(data []byte) bool {
	i := 0
	for i < len(data) && isJSONSpace(data[i]) {
		i++
	}
	if len(data)-i < len("null") || !bytes.Equal(data[i:i+len("null")], []byte("null")) {
		return false
	}
	i += len("null")
	for i < len(data) && isJSONSpace(data[i]) {
		i++
	}
	return i == len(data)
}

func isJSONSpace(c byte) bool {
	switch c {
	case ' ', '\t', '\r', '\n':
		return true
	default:
		return false
	}
}
