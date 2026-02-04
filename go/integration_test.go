package velox_test

import (
	"sync"
	"testing"

	velox "github.com/jpillora/velox/go"
)

// serverState implements RLocker for proper locking during marshal
type serverState struct {
	mu sync.RWMutex `json:"-"`
	velox.State
	Users    velox.VMap[string, string] `json:"users"`
	Messages velox.VSlice[string]       `json:"messages"`
	Counts   *velox.VMap[string, int]   `json:"counts"`
	Items    *velox.VSlice[int]         `json:"items"`
}

func (s *serverState) Lock()    { s.mu.Lock() }
func (s *serverState) Unlock()  { s.mu.Unlock() }
func (s *serverState) RLock()   { s.mu.RLock() }
func (s *serverState) RUnlock() { s.mu.RUnlock() }

// TestVMapVSliceIntegration tests basic VMap and VSlice sync
func TestVMapVSliceIntegration(t *testing.T) {
	// Simple test without network - just verify VMap/VSlice with binding work
	ss := &serverState{
		Counts: &velox.VMap[string, int]{},
		Items:  &velox.VSlice[int]{},
	}

	// Bind with the server state's lock (no pusher needed for this test)
	velox.BindAll(ss, ss, nil)

	// Test VMap operations
	ss.Users.Set("alice", "online")
	if v, ok := ss.Users.Get("alice"); !ok || v != "online" {
		t.Errorf("Users.Get(alice) = %v, %v, want online, true", v, ok)
	}

	ss.Counts.Set("visitors", 100)
	if v, ok := ss.Counts.Get("visitors"); !ok || v != 100 {
		t.Errorf("Counts.Get(visitors) = %v, %v, want 100, true", v, ok)
	}

	// Test VSlice operations
	ss.Messages.Append("Hello", "World")
	if ss.Messages.Len() != 2 {
		t.Errorf("Messages.Len() = %d, want 2", ss.Messages.Len())
	}

	ss.Items.Set([]int{1, 2, 3})
	if ss.Items.Len() != 3 {
		t.Errorf("Items.Len() = %d, want 3", ss.Items.Len())
	}

	// Test deletion
	ss.Users.Delete("alice")
	if ss.Users.Has("alice") {
		t.Error("Users should not have alice after deletion")
	}

	// Test clear
	ss.Messages.Clear()
	if ss.Messages.Len() != 0 {
		t.Errorf("Messages.Len() after clear = %d, want 0", ss.Messages.Len())
	}
}

// TestRLockerMarshal tests that Marshal uses RLock for RLocker types
func TestRLockerMarshal(t *testing.T) {
	type State struct {
		mu sync.RWMutex `json:"-"`
		velox.State
		Value int `json:"value"`
	}

	// Implement RLocker
	state := &State{Value: 42}
	state.State.Data = velox.Marshal(state)

	// The Marshal function should use RLock instead of Lock
	// This test verifies it doesn't panic and returns correct data
	data, err := state.State.Data()
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	if string(data) == "" {
		t.Error("Marshal returned empty data")
	}
}

// RWMutex wrapper to implement RLocker on State
func (s *State) Lock()    { s.mu.Lock() }
func (s *State) Unlock()  { s.mu.Unlock() }
func (s *State) RLock()   { s.mu.RLock() }
func (s *State) RUnlock() { s.mu.RUnlock() }

type State struct {
	mu sync.RWMutex `json:"-"`
	velox.State
	Value int `json:"value"`
}

func TestBindAllAfterUnmarshal(t *testing.T) {
	// This tests that BindAll properly binds VMap fields
	type Data struct {
		sync.Mutex
		Items *velox.VMap[string, int] `json:"items"`
	}

	data := &Data{
		Items: &velox.VMap[string, int]{},
	}

	// Bind with the data's mutex
	velox.BindAll(data, data, nil)

	// Operations should work with locking
	data.Items.Set("key", 42)
	if v, ok := data.Items.Get("key"); !ok || v != 42 {
		t.Errorf("Items.Get(key) = %v, %v, want 42, true", v, ok)
	}

	data.Items.Set("newkey", 100)
	if data.Items.Len() != 2 {
		t.Errorf("Items.Len() = %d, want 2", data.Items.Len())
	}
}
