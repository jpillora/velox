package velox_test

import (
	"strings"
	"sync"
	"testing"

	velox "github.com/jpillora/velox/go"
)

func TestBindAllSimpleStruct(t *testing.T) {
	pusher := &mockPusher{}
	locker := &mockLocker{}

	type Simple struct {
		Items *velox.VMap[string, int]
	}

	s := &Simple{
		Items: &velox.VMap[string, int]{},
	}

	velox.BindAll(s, locker, pusher)

	// VMap should be bound
	s.Items.Set("test", 42)

	if pusher.count.Load() != 1 {
		t.Errorf("After Set, push count = %d, want 1", pusher.count.Load())
	}

	if locker.lockCount.Load() < 1 {
		t.Errorf("After Set, lock count = %d, want >= 1", locker.lockCount.Load())
	}
}

func TestBindAllNestedStruct(t *testing.T) {
	pusher := &mockPusher{}
	locker := &mockLocker{}

	type Inner struct {
		Data *velox.VSlice[string]
	}

	type Outer struct {
		Inner Inner
		Map   *velox.VMap[int, string]
	}

	o := &Outer{
		Inner: Inner{
			Data: &velox.VSlice[string]{},
		},
		Map: &velox.VMap[int, string]{},
	}

	velox.BindAll(o, locker, pusher)

	// Both should be bound
	o.Inner.Data.Append("item")
	o.Map.Set(1, "one")

	if pusher.count.Load() != 2 {
		t.Errorf("After operations, push count = %d, want 2", pusher.count.Load())
	}
}

func TestBindAllPointerToStruct(t *testing.T) {
	pusher := &mockPusher{}

	type Data struct {
		Items *velox.VMap[string, int]
	}

	d := &Data{
		Items: &velox.VMap[string, int]{},
	}

	velox.BindAll(d, nil, pusher)

	d.Items.Set("key", 100)

	if pusher.count.Load() != 1 {
		t.Errorf("After Set, push count = %d, want 1", pusher.count.Load())
	}
}

func TestBindAllWithNilPointer(t *testing.T) {
	pusher := &mockPusher{}

	type Data struct {
		Items *velox.VMap[string, int]
	}

	d := &Data{
		Items: nil, // nil pointer
	}

	// Should not panic
	velox.BindAll(d, nil, pusher)

	// Items is still nil, nothing to bind
	if d.Items != nil {
		t.Error("BindAll should not create nil pointers")
	}
}

func TestBindAllWithEmbeddedVMap(t *testing.T) {
	pusher := &mockPusher{}
	locker := &mockRWLocker{}

	type Container struct {
		Map1 velox.VMap[string, int]
		Map2 *velox.VMap[string, int]
	}

	c := &Container{
		Map2: &velox.VMap[string, int]{},
	}

	velox.BindAll(c, locker, pusher)

	// Both should work
	c.Map1.Set("a", 1)
	c.Map2.Set("b", 2)

	if pusher.count.Load() != 2 {
		t.Errorf("After operations, push count = %d, want 2", pusher.count.Load())
	}
}

func TestBindAllWithMixedTypes(t *testing.T) {
	pusher := &mockPusher{}

	type Mixed struct {
		Name   string
		Count  int
		Map    *velox.VMap[string, string]
		Slice  *velox.VSlice[int]
		nested struct {
			Data *velox.VMap[int, int]
		}
	}

	m := &Mixed{
		Name:  "test",
		Count: 42,
		Map:   &velox.VMap[string, string]{},
		Slice: &velox.VSlice[int]{},
	}
	m.nested.Data = &velox.VMap[int, int]{}

	velox.BindAll(m, nil, pusher)

	m.Map.Set("key", "value")
	m.Slice.Append(1, 2, 3)

	if pusher.count.Load() != 2 {
		t.Errorf("After operations, push count = %d, want 2", pusher.count.Load())
	}
}

func TestBindAllNilValue(t *testing.T) {
	// Should not panic
	velox.BindAll(nil, nil, nil)
}

func TestBindAllWithRLocker(t *testing.T) {
	locker := &mockRWLocker{}
	pusher := &mockPusher{}

	type Data struct {
		Items *velox.VMap[string, int]
	}

	d := &Data{
		Items: &velox.VMap[string, int]{},
	}

	velox.BindAll(d, locker, pusher)

	// Write should use Lock
	d.Items.Set("key", 42)
	lockCount := locker.lockCount.Load()
	if lockCount < 1 {
		t.Errorf("Set() did not use Lock, count = %d", lockCount)
	}

	// Read should use RLock
	d.Items.Get("key")
	rlockCount := locker.rlockCount.Load()
	if rlockCount < 1 {
		t.Errorf("Get() did not use RLock, count = %d", rlockCount)
	}
}

func TestBindAllWithRegularLocker(t *testing.T) {
	locker := &mockLocker{}
	pusher := &mockPusher{}

	type Data struct {
		Items *velox.VMap[string, int]
	}

	d := &Data{
		Items: &velox.VMap[string, int]{},
	}

	velox.BindAll(d, locker, pusher)

	// Both read and write should use Lock (fallback)
	d.Items.Set("key", 42)
	d.Items.Get("key")

	// Should have used Lock for both operations
	if locker.lockCount.Load() < 2 {
		t.Errorf("Lock count = %d, want >= 2", locker.lockCount.Load())
	}
}

func TestBindAllNilPusher(t *testing.T) {
	locker := &mockLocker{}

	type Data struct {
		Items *velox.VMap[string, int]
	}

	d := &Data{
		Items: &velox.VMap[string, int]{},
	}

	// Bind with nil pusher (like on client side)
	velox.BindAll(d, locker, nil)

	// Should not panic, just skip push
	d.Items.Set("key", 42)
	d.Items.Delete("key")

	// Locker should still be used
	if locker.lockCount.Load() < 2 {
		t.Errorf("Lock count = %d, want >= 2", locker.lockCount.Load())
	}
}

func TestBindAllNilLocker(t *testing.T) {
	pusher := &mockPusher{}

	type Data struct {
		Items *velox.VMap[string, int]
	}

	d := &Data{
		Items: &velox.VMap[string, int]{},
	}

	// Bind with nil locker
	velox.BindAll(d, nil, pusher)

	// Should not panic, just skip locking
	d.Items.Set("key", 42)
	d.Items.Get("key")

	// Pusher should still be used
	if pusher.count.Load() != 1 {
		t.Errorf("Push count = %d, want 1", pusher.count.Load())
	}
}

func TestBindAllMultipleVMaps(t *testing.T) {
	pusher := &mockPusher{}

	type MultiMap struct {
		Users    *velox.VMap[string, string]
		Sessions *velox.VMap[int, bool]
		Config   *velox.VMap[string, int]
	}

	m := &MultiMap{
		Users:    &velox.VMap[string, string]{},
		Sessions: &velox.VMap[int, bool]{},
		Config:   &velox.VMap[string, int]{},
	}

	velox.BindAll(m, nil, pusher)

	m.Users.Set("alice", "admin")
	m.Sessions.Set(123, true)
	m.Config.Set("timeout", 30)

	if pusher.count.Load() != 3 {
		t.Errorf("After 3 operations, push count = %d, want 3", pusher.count.Load())
	}
}

func TestBindAllDoesNotPanic(t *testing.T) {
	// Test various edge cases that should not panic
	tests := []struct {
		name string
		fn   func()
	}{
		{"nil interface", func() { velox.BindAll(nil, nil, nil) }},
		{"zero value struct", func() { velox.BindAll(&struct{}{}, nil, nil) }},
		{"unexported fields", func() {
			type s struct {
				hidden *velox.VMap[string, int]
			}
			velox.BindAll(&s{}, nil, nil)
		}},
		{"interface field", func() {
			type s struct {
				I interface{}
			}
			velox.BindAll(&s{I: &velox.VMap[string, int]{}}, nil, nil)
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("BindAll panicked: %v", r)
				}
			}()
			tt.fn()
		})
	}
}

func TestBindAllInterfaceField(t *testing.T) {
	pusher := &mockPusher{}

	type Container struct {
		Data interface{}
	}

	vm := &velox.VMap[string, int]{}
	c := &Container{Data: vm}

	velox.BindAll(c, nil, pusher)

	// The VMap should be bound through the interface
	vm.Set("key", 42)

	if pusher.count.Load() != 1 {
		t.Errorf("Push count = %d, want 1", pusher.count.Load())
	}
}

func TestBindAllRebinding(t *testing.T) {
	pusher1 := &mockPusher{}
	pusher2 := &mockPusher{}

	type Data struct {
		Items *velox.VMap[string, int]
	}

	d := &Data{
		Items: &velox.VMap[string, int]{},
	}

	// First binding
	velox.BindAll(d, nil, pusher1)
	d.Items.Set("key", 1)
	if pusher1.count.Load() != 1 {
		t.Errorf("First pusher count = %d, want 1", pusher1.count.Load())
	}

	// Rebind to new pusher
	velox.BindAll(d, nil, pusher2)
	d.Items.Set("key", 2)

	// Second pusher should now receive pushes
	if pusher2.count.Load() != 1 {
		t.Errorf("Second pusher count = %d, want 1", pusher2.count.Load())
	}

	// First pusher should not have changed
	if pusher1.count.Load() != 1 {
		t.Errorf("First pusher count changed to %d", pusher1.count.Load())
	}
}

func TestBindAllPanicsOnNestedLocker(t *testing.T) {
	tests := []struct {
		name string
		fn   func()
	}{
		{"pointer child with mutex", func() {
			type Child struct {
				sync.Mutex
				Items map[string]string
			}
			type Root struct {
				sync.Mutex
				Child *Child
			}
			velox.BindAll(&Root{Child: &Child{}}, nil, nil)
		}},
		{"value child with mutex", func() {
			type Child struct {
				sync.Mutex
				Name string
			}
			type Root struct {
				sync.Mutex
				Child Child
			}
			velox.BindAll(&Root{}, nil, nil)
		}},
		{"deep nested mutex", func() {
			type Project struct {
				sync.Mutex
				Data map[string]string
			}
			type Machine struct {
				Projects *Project
			}
			type Root struct {
				sync.Mutex
				Machine Machine
			}
			velox.BindAll(&Root{Machine: Machine{Projects: &Project{}}}, nil, nil)
		}},
		{"child with rwmutex", func() {
			type Child struct {
				sync.RWMutex
				Items map[string]string
			}
			type Root struct {
				sync.Mutex
				Child *Child
			}
			velox.BindAll(&Root{Child: &Child{}}, nil, nil)
		}},
		{"child via interface field", func() {
			type Child struct {
				sync.Mutex
				Items map[string]string
			}
			type Root struct {
				sync.Mutex
				Data interface{}
			}
			velox.BindAll(&Root{Data: &Child{}}, nil, nil)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("expected panic, got none")
				}
				msg, ok := r.(string)
				if !ok {
					t.Fatalf("expected string panic, got %T: %v", r, r)
				}
				if !strings.Contains(msg, "sync.Locker") {
					t.Fatalf("unexpected panic message: %s", msg)
				}
			}()
			tt.fn()
		})
	}
}

func TestBindAllAllowsRootMutex(t *testing.T) {
	// Root struct with its own mutex should NOT panic
	type Root struct {
		sync.Mutex
		Items *velox.VMap[string, int]
	}
	r := &Root{Items: &velox.VMap[string, int]{}}
	velox.BindAll(r, nil, nil) // should not panic
}

func TestBindAllAllowsRootRWMutex(t *testing.T) {
	// Root struct with RWMutex should NOT panic
	type Root struct {
		sync.RWMutex
		Items *velox.VMap[string, int]
	}
	r := &Root{Items: &velox.VMap[string, int]{}}
	velox.BindAll(r, nil, nil) // should not panic
}

// TestVMapAndVSliceTogether tests using both types in the same struct
func TestVMapAndVSliceTogether(t *testing.T) {
	pusher := &mockPusher{}
	locker := &mockRWLocker{}

	type State struct {
		mu       sync.RWMutex `json:"-"`
		Users    *velox.VMap[string, string]
		Messages *velox.VSlice[string]
		Counts   *velox.VMap[string, int]
	}

	s := &State{
		Users:    &velox.VMap[string, string]{},
		Messages: &velox.VSlice[string]{},
		Counts:   &velox.VMap[string, int]{},
	}

	velox.BindAll(s, locker, pusher)

	s.Users.Set("alice", "online")
	s.Messages.Append("Hello", "World")
	s.Counts.Set("views", 100)

	if pusher.count.Load() != 3 {
		t.Errorf("Push count = %d, want 3", pusher.count.Load())
	}

	// Verify all containers work
	if !s.Users.Has("alice") {
		t.Error("Users should have alice")
	}
	if s.Messages.Len() != 2 {
		t.Errorf("Messages len = %d, want 2", s.Messages.Len())
	}
	if v, _ := s.Counts.Get("views"); v != 100 {
		t.Errorf("Counts views = %d, want 100", v)
	}
}
