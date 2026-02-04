package velox

import (
	"encoding/json"
	"sync"
)

// VSlice is a generic slice container that provides automatic locking and push support.
// It can be bound to a locker and pusher via BindAll.
type VSlice[V any] struct {
	locker sync.Locker
	pusher Pusher
	data   []V
}

func (s *VSlice[V]) bind(locker sync.Locker, pusher Pusher) {
	s.locker = locker
	s.pusher = pusher
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

func (s *VSlice[V]) push() {
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
	fn(&s.data)
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
func (s *VSlice[V]) MarshalJSON() ([]byte, error) {
	if s.data == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(s.data)
}

// UnmarshalJSON implements json.Unmarshaler.
// No locking - parent already holds lock during unmarshal.
func (s *VSlice[V]) UnmarshalJSON(data []byte) error {
	s.data = nil
	return json.Unmarshal(data, &s.data)
}
