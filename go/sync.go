package velox

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
)

// NewAny creates a new State object with the given
// json-serializable data. If data is a sync.Locker,
// it will be locked during the marshalling process.
func NewAny(data any) *State {
	return New(Marshal(data))
}

// New creates a new State object with the given data function.
// Data must not return an error otherwise New will panic.
func New(data MarshalFunc) *State {
	s := &State{
		Data:         data,
		Throttle:     DefaultThrottle,
		WriteTimeout: DefaultWriteTimeout,
		PingInterval: DefaultPingInterval,
		Debug:        false,
	}
	if err := s.init(); err != nil {
		panic("velox: " + err.Error())
	}
	return s
}

type stateEmbedded interface {
	self() *State
}

// SyncHandler is a small wrapper around Sync which simply synchronises
// all incoming connections. Use Sync if you wish to implement user authentication
// or any other request-time checks.
func SyncHandler(gostruct interface{}) http.Handler {
	var s *State
	// struct embeds velox.State?
	if tmp, ok := gostruct.(stateEmbedded); ok {
		s = tmp.self()
		s.Data = Marshal(gostruct)
		var locker sync.Locker
		if s.Locker != nil {
			locker = s.Locker
		} else if l, ok := gostruct.(sync.Locker); ok {
			locker = l
		}
		bindAll(gostruct, locker, s)
		if err := s.init(); err != nil {
			panic("velox: " + err.Error())
		}
	}
	// otherwise, check if the struct is a pointer to a struct
	if s == nil {
		s = New(Marshal(gostruct))
	}
	return s
}

var connectionID int64

type MarshalFunc func() (json.RawMessage, error)

// Sync upgrades a given HTTP connection into a velox connection and synchronises
// the provided struct with the client. velox takes responsibility for writing the response
// in the event of failure. Default handlers close the TCP connection on return so when
// manually using this method, you'll most likely want to block using Conn.Wait().
func Sync(gostruct interface{}, w http.ResponseWriter, r *http.Request) (Conn, error) {
	state := New(Marshal(gostruct))
	return state.Handle(w, r)
}

func Marshal(gostruct interface{}) MarshalFunc {
	rlock := func() {}
	runlock := func() {}
	if se, ok := gostruct.(stateEmbedded); ok && se.self().Locker != nil {
		l := se.self().Locker
		if rl, ok2 := l.(RLocker); ok2 {
			rlock = rl.RLock
			runlock = rl.RUnlock
		} else {
			rlock = l.Lock
			runlock = l.Unlock
		}
	} else if rl, ok := gostruct.(RLocker); ok {
		rlock = rl.RLock
		runlock = rl.RUnlock
	} else if l, ok := gostruct.(sync.Locker); ok {
		rlock = l.Lock
		runlock = l.Unlock
	}

	return func() (outBytes json.RawMessage, outErr error) {
		rlock()
		defer runlock()
		defer func() {
			if r := recover(); r != nil {
				outBytes = nil
				outErr = fmt.Errorf("velox sync panic during marshal: %v", r)
			}
		}()
		b, err := json.Marshal(gostruct)
		if err != nil {
			return nil, fmt.Errorf("velox sync failed: %s", err)
		}
		return b, nil
	}
}
