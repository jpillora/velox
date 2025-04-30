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
	return func() (json.RawMessage, error) {
		l, ok := gostruct.(sync.Locker)
		if ok {
			l.Lock()
			defer l.Unlock()
		}
		b, err := json.Marshal(gostruct)
		if err != nil {
			return nil, fmt.Errorf("velox sync failed: %s", err)
		}
		return b, nil
	}
}
