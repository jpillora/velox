package velox

import (
	"encoding/json"
	"sync"
)

// VMap is a generic map container that provides automatic locking and push support.
// Binding to a locker and pusher happens automatically via SyncHandler (server)
// and Client (after unmarshal).
type VMap[K comparable, V any] struct {
	locker sync.Locker // may also implement RLocker
	pusher Pusher      // nil on client (no push)
	data   map[K]V
}

func (m *VMap[K, V]) bind(locker sync.Locker, pusher Pusher) {
	m.locker = locker
	m.pusher = pusher
	if m.data == nil {
		m.data = make(map[K]V)
	}
}

// rlock acquires read lock (RLock if available, else Lock)
func (m *VMap[K, V]) rlock() {
	if m.locker == nil {
		return
	}
	if rl, ok := m.locker.(RLocker); ok {
		rl.RLock()
	} else {
		m.locker.Lock()
	}
}

func (m *VMap[K, V]) runlock() {
	if m.locker == nil {
		return
	}
	if rl, ok := m.locker.(RLocker); ok {
		rl.RUnlock()
	} else {
		m.locker.Unlock()
	}
}

func (m *VMap[K, V]) lock() {
	if m.locker != nil {
		m.locker.Lock()
	}
}

func (m *VMap[K, V]) unlock() {
	if m.locker != nil {
		m.locker.Unlock()
	}
}

func (m *VMap[K, V]) push() {
	if m.pusher != nil {
		m.pusher.Push()
	}
}

// Get returns the value for the given key and whether it exists.
func (m *VMap[K, V]) Get(key K) (V, bool) {
	m.rlock()
	defer m.runlock()
	v, ok := m.data[key]
	return v, ok
}

// Len returns the number of entries in the map.
func (m *VMap[K, V]) Len() int {
	m.rlock()
	defer m.runlock()
	return len(m.data)
}

// Keys returns a slice of all keys in the map.
func (m *VMap[K, V]) Keys() []K {
	m.rlock()
	defer m.runlock()
	keys := make([]K, 0, len(m.data))
	for k := range m.data {
		keys = append(keys, k)
	}
	return keys
}

// Values returns a slice of all values in the map.
func (m *VMap[K, V]) Values() []V {
	m.rlock()
	defer m.runlock()
	values := make([]V, 0, len(m.data))
	for _, v := range m.data {
		values = append(values, v)
	}
	return values
}

// Snapshot returns a copy of the underlying map.
func (m *VMap[K, V]) Snapshot() map[K]V {
	m.rlock()
	defer m.runlock()
	cp := make(map[K]V, len(m.data))
	for k, v := range m.data {
		cp[k] = v
	}
	return cp
}

// Has returns true if the key exists in the map.
func (m *VMap[K, V]) Has(key K) bool {
	m.rlock()
	defer m.runlock()
	_, ok := m.data[key]
	return ok
}

// Range calls the given function for each key-value pair in the map.
// If the function returns false, iteration stops.
// Note: The function is called with the lock held.
func (m *VMap[K, V]) Range(fn func(key K, value V) bool) {
	m.rlock()
	defer m.runlock()
	for k, v := range m.data {
		if !fn(k, v) {
			return
		}
	}
}

// Set sets the value for the given key and triggers a push.
func (m *VMap[K, V]) Set(key K, value V) {
	m.lock()
	defer m.unlock()
	if m.data == nil {
		m.data = make(map[K]V)
	}
	m.data[key] = value
	m.push()
}

// Delete removes the key from the map and triggers a push.
func (m *VMap[K, V]) Delete(key K) {
	m.lock()
	defer m.unlock()
	delete(m.data, key)
	m.push()
}

// Update calls the given function with a pointer to the value for the given key.
// If the key exists, the function is called and a push is triggered.
// Returns true if the key existed and was updated.
func (m *VMap[K, V]) Update(key K, fn func(*V)) bool {
	m.lock()
	defer m.unlock()
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
	m.lock()
	defer m.unlock()
	if m.data == nil {
		m.data = make(map[K]V)
	}
	fn(m.data)
	m.push()
}

// Clear removes all entries from the map and triggers a push.
func (m *VMap[K, V]) Clear() {
	m.lock()
	defer m.unlock()
	m.data = make(map[K]V)
	m.push()
}

// MarshalJSON implements json.Marshaler.
// No locking - parent already holds lock during marshal.
func (m *VMap[K, V]) MarshalJSON() ([]byte, error) {
	if m.data == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(m.data)
}

// UnmarshalJSON implements json.Unmarshaler.
// No locking - parent already holds lock during unmarshal.
func (m *VMap[K, V]) UnmarshalJSON(data []byte) error {
	m.data = make(map[K]V) // Clear to handle deletions
	return json.Unmarshal(data, &m.data)
}
