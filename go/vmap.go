package velox

import (
	"encoding/json"
	"maps"
	"reflect"
	"sync"
	"sync/atomic"
)

// VMap is a generic map container that provides automatic locking and push support.
// Binding to a locker and pusher happens automatically via SyncHandler (server)
// and Client (after unmarshal).
type VMap[K comparable, V any] struct {
	binding atomic.Pointer[containerBinding]
	data    map[K]V
	cache   jsonCache // memoised encoding, engaged only when State.Incremental is set
}

func (m *VMap[K, V]) bind(locker sync.Locker, pusher Pusher) {
	m.binding.Store(&containerBinding{locker: locker, pusher: pusher})
	if m.data == nil {
		m.data = make(map[K]V)
	}
	m.cache.markDirty()
	if incrementalRequested(pusher) {
		m.cache.enable(reflect.TypeFor[V]())
	}
}

// push notifies the state that this container changed. Marking the cache dirty
// here rather than in each mutator means a mutator added later cannot forget to
// do it, and it runs even on the client, where there is no pusher.
func (m *VMap[K, V]) push() {
	m.cache.markDirty()
	if binding := m.binding.Load(); binding != nil && binding.pusher != nil {
		binding.pusher.Push()
	}
}

// Get returns the value for the given key and whether it exists.
func (m *VMap[K, V]) Get(key K) (V, bool) {
	unlock := m.binding.Load().rlock()
	defer unlock()
	v, ok := m.data[key]
	return v, ok
}

// Len returns the number of entries in the map.
func (m *VMap[K, V]) Len() int {
	unlock := m.binding.Load().rlock()
	defer unlock()
	return len(m.data)
}

// Keys returns a slice of all keys in the map.
func (m *VMap[K, V]) Keys() []K {
	unlock := m.binding.Load().rlock()
	defer unlock()
	keys := make([]K, 0, len(m.data))
	for k := range m.data {
		keys = append(keys, k)
	}
	return keys
}

// Values returns a slice of all values in the map.
func (m *VMap[K, V]) Values() []V {
	unlock := m.binding.Load().rlock()
	defer unlock()
	values := make([]V, 0, len(m.data))
	for _, v := range m.data {
		values = append(values, v)
	}
	return values
}

// Snapshot returns a copy of the underlying map.
func (m *VMap[K, V]) Snapshot() map[K]V {
	unlock := m.binding.Load().rlock()
	defer unlock()
	cp := make(map[K]V, len(m.data))
	for k, v := range m.data {
		cp[k] = v
	}
	return cp
}

// Has returns true if the key exists in the map.
func (m *VMap[K, V]) Has(key K) bool {
	unlock := m.binding.Load().rlock()
	defer unlock()
	_, ok := m.data[key]
	return ok
}

// Range calls the given function for each key-value pair in the map.
// If the function returns false, iteration stops.
// Note: The function is called with the lock held.
func (m *VMap[K, V]) Range(fn func(key K, value V) bool) {
	unlock := m.binding.Load().rlock()
	defer unlock()
	for k, v := range m.data {
		if !fn(k, v) {
			return
		}
	}
}

// Set sets the value for the given key and triggers a push.
func (m *VMap[K, V]) Set(key K, value V) {
	unlock := m.binding.Load().lock()
	defer unlock()
	if m.data == nil {
		m.data = make(map[K]V)
	}
	m.data[key] = value
	m.push()
}

// Delete removes the key from the map and triggers a push.
func (m *VMap[K, V]) Delete(key K) {
	unlock := m.binding.Load().lock()
	defer unlock()
	delete(m.data, key)
	m.push()
}

// Update calls the given function with a pointer to the value for the given key.
// If the key exists, the function is called and a push is triggered.
// Returns true if the key existed and was updated.
func (m *VMap[K, V]) Update(key K, fn func(*V)) bool {
	unlock := m.binding.Load().lock()
	defer unlock()
	v, ok := m.data[key]
	if !ok {
		return false
	}
	fn(&v)
	m.data[key] = v
	m.push()
	return true
}

// Batch allows multiple operations on the map with a single push at the end.
// The function receives the raw map and can modify it directly.
func (m *VMap[K, V]) Batch(fn func(data map[K]V)) {
	unlock := m.binding.Load().lock()
	defer unlock()
	if m.data == nil {
		m.data = make(map[K]V)
	}
	fn(m.data)
	if m.cache.engaged() {
		// The callback was handed the container's own map. A caller that keeps
		// that reference can mutate the contents later, with no mutating method
		// called and so no chance to mark the cache dirty — which would serve
		// bytes that no longer describe the state. Re-homing the contents means
		// any reference the callback kept now points somewhere the container no
		// longer reads. It costs one copy, on an operation that is already doing
		// bulk work.
		m.data = maps.Clone(m.data)
	}
	m.push()
}

// Clear removes all entries from the map and triggers a push.
func (m *VMap[K, V]) Clear() {
	unlock := m.binding.Load().lock()
	defer unlock()
	m.data = make(map[K]V)
	m.push()
}

// MarshalJSON implements json.Marshaler.
// No locking - parent already holds lock during marshal.
//
// A clean container returns its previous encoding rather than re-encoding its
// contents, which is what keeps an unchanged subtree out of the marshal
// entirely. Caching is off unless State.Incremental is set and V is safe to
// hand out by copy; see deeplyImmutable.
func (m *VMap[K, V]) MarshalJSON() ([]byte, error) {
	if encoded, ok := m.cache.get(); ok {
		return encoded, nil
	}
	if m.data == nil {
		return []byte("{}"), nil
	}
	encoded, err := json.Marshal(m.data)
	if err != nil {
		return nil, err
	}
	m.cache.put(encoded)
	return encoded, nil
}

// UnmarshalJSON implements json.Unmarshaler.
// No locking - parent already holds lock during unmarshal.
func (m *VMap[K, V]) UnmarshalJSON(data []byte) error {
	m.data = make(map[K]V) // Clear to handle deletions
	m.cache.markDirty()
	return json.Unmarshal(data, &m.data)
}

// clearForUnmarshal preserves the binding while the parent holds its write lock.
func (m *VMap[K, V]) clearForUnmarshal() {
	m.data = nil
	m.cache.markDirty()
}
