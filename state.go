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
	initMut  sync.Mutex
	initd    bool
	gostruct interface{}
	connMut  sync.Mutex
	conns    map[string]*conn
	data     struct {
		mut     sync.RWMutex
		id      string
		bytes   []byte
		delta   []byte
		version int64
	}
	push struct {
		mut    sync.Mutex
		ing    uint32
		queued uint32
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
		s.data.bytes = b
	}
	id := make([]byte, 4)
	if n, _ := rand.Read(id); n > 0 {
		s.data.id = hex.EncodeToString(id)
	}
	s.gostruct = gostruct
	s.data.version = 1
	s.conns = map[string]*conn{}
	s.initd = true
	return nil
}

//ID uniquely identifies this state object
func (s *State) ID() string {
	return s.data.id
}

//Version of this state object (when the underlying struct is
//and a Push is performed, this version number is incremented).
func (s *State) Version() int64 {
	return s.data.version
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
//with abandon. Returns false if a Push in progress.
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
	t0 := time.Now()
	//queue cleanup
	defer func() {
		//measure time passed, ensure we wait at least Throttle time
		tdelta := time.Now().Sub(t0)
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
	if !bytes.Equal(s.data.bytes, newBytes) {
		//calculate change set from last version
		ops, _ := jsonpatch.CreatePatch(s.data.bytes, newBytes)
		if len(s.data.bytes) > 0 && len(ops) > 0 {
			//changes! bump version
			s.data.mut.Lock()
			s.data.delta, _ = json.Marshal(ops)
			s.data.bytes = newBytes
			s.data.version++
			s.data.mut.Unlock()
		}
	}
	//send this new change to each subscriber
	s.connMut.Lock()
	for _, c := range s.conns {
		if c.version != s.data.version {
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
	if c.version == s.data.version {
		return
	}
	s.data.mut.RLock()
	update := &update{Version: s.data.version}
	//first push? include id
	if atomic.CompareAndSwapUint32(&c.first, 0, 1) {
		update.ID = s.data.id
	}
	//choose optimal update (send the smallest)
	if s.data.delta != nil &&
		c.version == (s.data.version-1) &&
		len(s.data.bytes) > 0 &&
		len(s.data.delta) < len(s.data.bytes) {
		update.Delta = true
		update.Body = s.data.delta
	} else {
		update.Delta = false
		update.Body = s.data.bytes
	}
	s.data.mut.RUnlock()
	//send!
	sent := make(chan error)
	go func() {
		sent <- c.send(update)
	}()
	//wait for timeout or sent
	select {
	case err := <-sent:
		//success?
		if err == nil {
			//on success, update client version
			s.data.mut.RLock()
			c.version = s.data.version
			s.data.mut.RUnlock()
		} else {
			c.Close()
		}
	case <-time.After(30 * time.Second):
		//timeout
		c.Close()
	}
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
