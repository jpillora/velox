package velox

import (
	"encoding/json"
	"reflect"
	"slices"
	"sync"
)

// VSlice is a generic slice container that provides automatic locking and push support.
// Binding to a locker and pusher happens automatically via SyncHandler (server)
// and Client (after unmarshal).
type VSlice[V any] struct {
	locker sync.Locker
	pusher Pusher
	data   []V
	cache  jsonCache // memoised encoding, engaged only when State.Incremental is set
}

func (s *VSlice[V]) bind(locker sync.Locker, pusher Pusher) {
	s.locker = locker
	s.pusher = pusher
	s.cache.markDirty()
	if incrementalRequested(pusher) {
		s.cache.enable(reflect.TypeFor[V]())
	}
}

func (s *VSlice[V]) rlock() {
	if s.locker == nil {
		return
	}
	if rl, ok := s.locker.(RLocker); ok {
		rl.RLock()
	} else {
		s.locker.Lock()
	}
}

func (s *VSlice[V]) runlock() {
	if s.locker == nil {
		return
	}
	if rl, ok := s.locker.(RLocker); ok {
		rl.RUnlock()
	} else {
		s.locker.Unlock()
	}
}

func (s *VSlice[V]) lock() {
	if s.locker != nil {
		s.locker.Lock()
	}
}

func (s *VSlice[V]) unlock() {
	if s.locker != nil {
		s.locker.Unlock()
	}
}

// push notifies the state that this container changed. Marking the cache dirty
// here rather than in each mutator means a mutator added later cannot forget to
// do it, and it runs even on the client, where there is no pusher.
func (s *VSlice[V]) push() {
	s.cache.markDirty()
	if s.pusher != nil {
		s.pusher.Push()
	}
}

// Get returns a copy of the slice.
func (s *VSlice[V]) Get() []V {
	s.rlock()
	defer s.runlock()
	if s.data == nil {
		return nil
	}
	cp := make([]V, len(s.data))
	copy(cp, s.data)
	return cp
}

// Len returns the length of the slice.
func (s *VSlice[V]) Len() int {
	s.rlock()
	defer s.runlock()
	return len(s.data)
}

// At returns the element at the given index.
// Returns zero value and false if index is out of bounds.
func (s *VSlice[V]) At(index int) (V, bool) {
	s.rlock()
	defer s.runlock()
	if index < 0 || index >= len(s.data) {
		var zero V
		return zero, false
	}
	return s.data[index], true
}

// Range calls the given function for each element in the slice.
// If the function returns false, iteration stops.
// Note: The function is called with the lock held.
func (s *VSlice[V]) Range(fn func(index int, value V) bool) {
	s.rlock()
	defer s.runlock()
	for i, v := range s.data {
		if !fn(i, v) {
			return
		}
	}
}

// Set replaces the entire slice and triggers a push.
func (s *VSlice[V]) Set(data []V) {
	s.lock()
	defer s.unlock()
	s.data = data
	s.push()
}

// Append adds values to the end of the slice and triggers a push.
func (s *VSlice[V]) Append(values ...V) {
	s.lock()
	defer s.unlock()
	s.data = append(s.data, values...)
	s.push()
}

// SetAt sets the element at the given index and triggers a push.
// Returns false if index is out of bounds.
func (s *VSlice[V]) SetAt(index int, value V) bool {
	s.lock()
	defer s.unlock()
	if index < 0 || index >= len(s.data) {
		return false
	}
	s.data[index] = value
	s.push()
	return true
}

// DeleteAt removes the element at the given index and triggers a push.
// Returns false if index is out of bounds.
func (s *VSlice[V]) DeleteAt(index int) bool {
	s.lock()
	defer s.unlock()
	if index < 0 || index >= len(s.data) {
		return false
	}
	s.data = append(s.data[:index], s.data[index+1:]...)
	s.push()
	return true
}

// Update calls the given function with a pointer to the element at the given index.
// Returns false if index is out of bounds.
func (s *VSlice[V]) Update(index int, fn func(*V)) bool {
	s.lock()
	defer s.unlock()
	if index < 0 || index >= len(s.data) {
		return false
	}
	fn(&s.data[index])
	s.push()
	return true
}

// Batch allows multiple operations on the slice with a single push at the end.
// The function receives a pointer to the raw slice and can modify it directly.
func (s *VSlice[V]) Batch(fn func(*[]V)) {
	s.lock()
	defer s.unlock()
	if !s.cache.engaged() {
		fn(&s.data)
		s.push()
		return
	}
	// The callback would otherwise be handed a pointer to the container's own
	// field, so a caller that keeps it could both replace the slice and mutate
	// its elements later — with no mutating method called, and so no chance to
	// mark the cache dirty. Hand it a local instead and take a fresh copy of the
	// result, leaving anything it kept pointing where the container does not
	// read. It costs one copy, on an operation already doing bulk work.
	scratch := s.data
	fn(&scratch)
	s.data = slices.Clone(scratch)
	s.push()
}

// Clear removes all elements from the slice and triggers a push.
func (s *VSlice[V]) Clear() {
	s.lock()
	defer s.unlock()
	s.data = nil
	s.push()
}

// MarshalJSON implements json.Marshaler.
// No locking - parent already holds lock during marshal.
//
// A clean container returns its previous encoding rather than re-encoding its
// contents, which is what keeps an unchanged subtree out of the marshal
// entirely. Caching is off unless State.Incremental is set and V is safe to
// hand out by copy; see deeplyImmutable.
func (s *VSlice[V]) MarshalJSON() ([]byte, error) {
	if encoded, ok := s.cache.get(); ok {
		return encoded, nil
	}
	if s.data == nil {
		return []byte("[]"), nil
	}
	encoded, err := json.Marshal(s.data)
	if err != nil {
		return nil, err
	}
	s.cache.put(encoded)
	return encoded, nil
}

// UnmarshalJSON implements json.Unmarshaler.
// No locking - parent already holds lock during unmarshal.
func (s *VSlice[V]) UnmarshalJSON(data []byte) error {
	s.data = nil
	s.cache.markDirty()
	return json.Unmarshal(data, &s.data)
}
