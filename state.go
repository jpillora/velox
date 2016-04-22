package velox

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mattbaird/jsonpatch"
)

var (
	//15ms is approximately highest resolution on the JS eventloop
	MinThrottle     = 15 * time.Millisecond
	DefaultThrottle = 200 * time.Millisecond
)

//State must be embedded into a struct to make it syncable.
type State struct {
	//configuration
	Throttle time.Duration `json:"-"`
	//internal state
	id       string
	initMut  sync.Mutex
	initd    bool
	gostruct interface{}
	dataMut  sync.RWMutex //protects bytes/delta/version
	bytes    []byte
	delta    []byte
	version  int64
	connMut  sync.Mutex
	conns    map[string]*conn
	push     struct {
		mut    sync.Mutex
		ing    uint32
		queued uint32
		start  time.Time
		wg     sync.WaitGroup
	}
}

func (s *State) init(gostruct interface{}) error {
	if s.Throttle < MinThrottle {
		s.Throttle = DefaultThrottle
	}
	//get initial JSON bytes and confirm gostruct is marshallable
	if b, err := json.Marshal(gostruct); err != nil {
		return fmt.Errorf("JSON marshalling failed: %s", err)
	} else {
		s.bytes = b
	}
	id := make([]byte, 4)
	if n, _ := rand.Read(id); n > 0 {
		s.id = hex.EncodeToString(id)
	}
	s.gostruct = gostruct
	s.version = 1
	s.conns = map[string]*conn{}
	s.initd = true
	return nil
}

//ID uniquely identifies this state object
func (s *State) ID() string {
	return s.id
}

//Version of this state object (when the underlying struct is
//and a Push is performed, this version number is incremented).
func (s *State) Version() int64 {
	return s.version
}

func (s *State) sync(gostruct interface{}) (*State, error) {
	s.initMut.Lock()
	defer s.initMut.Unlock()
	if !s.initd {
		if err := s.init(gostruct); err != nil {
			return nil, err
		}
	} else if s.gostruct != gostruct {
		return nil, errors.New("A different struct is already synced")
	}
	return s, nil
}

func (s *State) subscribe(conn *conn) {
	//subscribe
	s.connMut.Lock()
	s.conns[conn.id] = conn
	s.connMut.Unlock()
	//and then unsubscribe on close
	go func() {
		conn.Wait()
		s.connMut.Lock()
		delete(s.conns, conn.id)
		s.connMut.Unlock()
	}()
}

func (s *State) NumConnections() int {
	s.connMut.Lock()
	n := len(s.conns)
	s.connMut.Unlock()
	return n
}

//Send the changes from this object to all connected clients.
//Push is thread-safe and is throttled so it can be called
//with abandon. Returns false if a Push has already been queued.
func (s *State) Push() bool {
	//attempt to mark state as 'pushing'
	if atomic.CompareAndSwapUint32(&s.push.ing, 0, 1) {
		go s.gopush()
		return true
	}
	//if already pushing, mark queued
	atomic.StoreUint32(&s.push.queued, 1)
	return false
}

//non-blocking push
func (s *State) gopush() {
	s.push.mut.Lock()
	s.push.start = time.Now()
	//queue cleanup
	defer func() {
		//measure time passed, ensure we wait at least Throttle time
		tdelta := time.Now().Sub(s.push.start)
		if t := s.Throttle - tdelta; t > 0 {
			time.Sleep(t)
		}
		//mark not 'pushing'
		atomic.StoreUint32(&s.push.ing, 0)
		//cleanup
		if atomic.CompareAndSwapUint32(&s.push.queued, 1, 0) {
			s.push.mut.Unlock()
			s.Push() //auto-push when queued
		} else {
			s.push.mut.Unlock()
		}
	}()
	//calculate new json state
	l, hasLock := s.gostruct.(sync.Locker)
	if hasLock {
		l.Lock()
	}
	newBytes, err := json.Marshal(s.gostruct)
	if hasLock {
		l.Unlock()
	}
	if err != nil {
		log.Printf("velox: marshal failed: %s", err)
		return
	}
	//if changed, then calculate change set
	if !bytes.Equal(s.bytes, newBytes) {
		//calculate change set from last version
		ops, _ := jsonpatch.CreatePatch(s.bytes, newBytes)
		if len(s.bytes) > 0 && len(ops) > 0 {
			//changes! bump version
			s.dataMut.Lock()
			s.delta, _ = json.Marshal(ops)
			s.bytes = newBytes
			s.version++
			s.dataMut.Unlock()
		}
	}

	//send this new change to each subscriber
	s.connMut.Lock()
	for _, c := range s.conns {
		if c.version != s.version {
			s.push.wg.Add(1)
			go func(c *conn) {
				s.pushTo(c)
				s.push.wg.Done()
			}(c)
		}
	}
	s.connMut.Unlock()
	//wait for all connection pushes
	s.push.wg.Wait()
	//cleanup()
}

func (s *State) pushTo(c *conn) {
	if c.version == s.version {
		return
	}
	update := &update{
		Version: s.version,
	}
	s.dataMut.RLock()
	//first push? include id
	if atomic.CompareAndSwapUint32(&c.first, 0, 1) {
		update.ID = s.id
	}
	//choose optimal update (send the smallest)
	if s.delta != nil && c.version == (s.version-1) && len(s.bytes) > 0 && len(s.delta) < len(s.bytes) {
		update.Delta = true
		update.Body = s.delta
	} else {
		update.Delta = false
		update.Body = s.bytes
	}
	//send update
	if err := c.send(update); err == nil {
		c.version = s.version //sent! mark this version
	}
	s.dataMut.RUnlock()
}

//A single update. May contain compression flags in future.
type update struct {
	ID      string          `json:"id,omitempty"`
	Ping    bool            `json:"ping,omitempty"`
	Delta   bool            `json:"delta,omitempty"`
	Version int64           `json:"version,omitempty"` //53 usable bits
	Body    json.RawMessage `json:"body,omitempty"`
}

//implement eventsource.Event interface
func (u *update) Id() string    { return strconv.FormatInt(u.Version, 10) }
func (u *update) Event() string { return "" }
func (u *update) Data() string {
	b, _ := json.Marshal(u)
	return string(b)
}
