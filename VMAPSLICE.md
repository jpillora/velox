# State Encapsulation Plan

**Goal**: Hide locking discipline using velox-provided generic containers.

## Current Problem

Scattered state mutation pattern:
```go
c.state.Lock()
c.state.WebClients[conn.ID()] = client
c.state.Push()
c.state.Unlock()
```

Issues:
1. Locking is caller's responsibility
2. Push() must be called after mutations
3. Lock/Unlock order varies
4. Implementation detail leaked to all callers

## Solution: velox.VMap and velox.VSlice

Move generic containers into velox package. velox handles binding after unmarshal.

---

## Velox Changes Required

### 1. RLocker Interface

```go
// velox/go/lock.go
package velox

import "sync"

// RLocker extends sync.Locker with read-lock methods.
// If a struct only implements sync.Locker, velox falls back to Lock() for reads.
type RLocker interface {
    sync.Locker
    RLock()
    RUnlock()
}
```

### 2. Bindable Interface and BindAll

```go
// velox/go/bind.go
package velox

// Pusher calls Push() to sync state. Implemented by velox.State.
type Pusher interface {
    Push() bool
}

// bindable is implemented by VMap/VSlice (internal interface)
type bindable interface {
    bind(locker sync.Locker, pusher Pusher)
}

// BindAll walks a struct and binds all VMap/VSlice fields.
// Called by user constructors and velox client after unmarshal.
func BindAll(v any, locker sync.Locker, pusher Pusher) {
    bindValue(reflect.ValueOf(v), locker, pusher)
}

func bindValue(v reflect.Value, locker sync.Locker, pusher Pusher) {
    if !v.IsValid() {
        return
    }
    if v.CanInterface() {
        if b, ok := v.Interface().(bindable); ok {
            b.bind(locker, pusher)
        }
    }
    switch v.Kind() {
    case reflect.Ptr, reflect.Interface:
        if !v.IsNil() {
            bindValue(v.Elem(), locker, pusher)
        }
    case reflect.Struct:
        for i := 0; i < v.NumField(); i++ {
            field := v.Field(i)
            if field.CanSet() || field.CanAddr() {
                bindValue(field, locker, pusher)
            }
        }
    }
}
```

### 3. VMap Implementation

```go
// velox/go/vmap.go
package velox

type VMap[K comparable, V any] struct {
    locker sync.Locker  // may also implement RLocker
    pusher Pusher       // nil on client (no push)
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

// Read operations use RLock
func (m *VMap[K, V]) Get(key K) (V, bool) {
    m.rlock()
    defer m.runlock()
    v, ok := m.data[key]
    return v, ok
}

func (m *VMap[K, V]) Len() int {
    m.rlock()
    defer m.runlock()
    return len(m.data)
}

func (m *VMap[K, V]) Keys() []K {
    m.rlock()
    defer m.runlock()
    keys := make([]K, 0, len(m.data))
    for k := range m.data {
        keys = append(keys, k)
    }
    return keys
}

func (m *VMap[K, V]) Snapshot() map[K]V {
    m.rlock()
    defer m.runlock()
    cp := make(map[K]V, len(m.data))
    for k, v := range m.data {
        cp[k] = v
    }
    return cp
}

// Write operations use Lock and call Push
func (m *VMap[K, V]) Set(key K, value V) {
    m.lock()
    defer m.unlock()
    if m.data == nil {
        m.data = make(map[K]V)
    }
    m.data[key] = value
    m.push()
}

func (m *VMap[K, V]) Delete(key K) {
    m.lock()
    defer m.unlock()
    delete(m.data, key)
    m.push()
}

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

func (m *VMap[K, V]) Batch(fn func(data map[K]V)) {
    m.lock()
    defer m.unlock()
    if m.data == nil {
        m.data = make(map[K]V)
    }
    fn(m.data)
    m.push()
}

// JSON - no locking (parent already holds lock during marshal/unmarshal)
func (m *VMap[K, V]) MarshalJSON() ([]byte, error) {
    if m.data == nil {
        return []byte("{}"), nil
    }
    return json.Marshal(m.data)
}

func (m *VMap[K, V]) UnmarshalJSON(data []byte) error {
    m.data = make(map[K]V)  // Clear to handle deletions
    return json.Unmarshal(data, &m.data)
}
```

### 4. VSlice Implementation

```go
// velox/go/vslice.go
package velox

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

func (s *VSlice[V]) Get() []V {
    s.rlock()
    defer s.runlock()
    cp := make([]V, len(s.data))
    copy(cp, s.data)
    return cp
}

func (s *VSlice[V]) Len() int {
    s.rlock()
    defer s.runlock()
    return len(s.data)
}

func (s *VSlice[V]) Set(data []V) {
    s.lock()
    defer s.unlock()
    s.data = data
    s.push()
}

func (s *VSlice[V]) Append(values ...V) {
    s.lock()
    defer s.unlock()
    s.data = append(s.data, values...)
    s.push()
}

func (s *VSlice[V]) MarshalJSON() ([]byte, error) {
    if s.data == nil {
        return []byte("[]"), nil
    }
    return json.Marshal(s.data)
}

func (s *VSlice[V]) UnmarshalJSON(data []byte) error {
    s.data = nil
    return json.Unmarshal(data, &s.data)
}
```

### 5. Update Marshal/Unmarshal to use RLocker and Bind

```go
// velox/go/sync.go
func Marshal(gostruct interface{}) MarshalFunc {
    // Use RLock if available, else Lock
    rlock := func() {
        if rl, ok := gostruct.(RLocker); ok {
            rl.RLock()
        } else if l, ok := gostruct.(sync.Locker); ok {
            l.Lock()
        }
    }
    runlock := func() {
        if rl, ok := gostruct.(RLocker); ok {
            rl.RUnlock()
        } else if l, ok := gostruct.(sync.Locker); ok {
            l.Unlock()
        }
    }
    return func() (json.RawMessage, error) {
        rlock()
        defer runlock()
        return json.Marshal(gostruct)
    }
}

// velox/go/client.go - after unmarshal, bind all fields
func (c *Client[T]) applyUpdate(newState json.RawMessage) error {
    // Get locker from data (may be sync.Locker or RLocker)
    locker, _ := any(c.data).(sync.Locker)

    if locker != nil {
        locker.Lock()
        defer locker.Unlock()
    }

    clearMaps(c.data)
    if err := json.Unmarshal(newState, c.data); err != nil {
        return err
    }

    // Bind all VMap/VSlice fields (nil pusher on client)
    BindAll(c.data, locker, nil)

    return nil
}
```

### 6. Server-side State initialization

For server-side, the user's struct constructor handles binding:

```go
// rais: internal/app/state/state_control.go
func NewRoot() *Root {
    r := &Root{
        Machines:   &velox.VMap[string, *Machine]{},
        Users:      &velox.VMap[string, *User]{},
        WebClients: &velox.VMap[string, *WebClient]{},
    }
    r.State.Data = velox.Marshal(r)
    // Bind with r as locker (implements RLocker) and &r.State as pusher
    velox.BindAll(r, r, &r.State)
    return r
}
```

---

## Rais State Types (using velox.VMap/VSlice)

```go
// internal/app/state/state_control.go
package state

import (
    "sync"
    velox "github.com/jpillora/velox/go"
)

type Root struct {
    mu sync.RWMutex `json:"-"`
    velox.State
    Version    string                        `json:"version,omitzero"`
    Compiled   string                        `json:"compiled,omitzero"`
    Channel    string                        `json:"channel,omitzero"`
    Machines   *velox.VMap[string, *Machine]    `json:"machines"`
    Users      *velox.VMap[string, *User]       `json:"users"`
    WebClients *velox.VMap[string, *WebClient]  `json:"webClients"`
}

// RLocker implementation
func (r *Root) RLock()   { r.mu.RLock() }
func (r *Root) RUnlock() { r.mu.RUnlock() }
func (r *Root) Lock()    { r.mu.Lock() }
func (r *Root) Unlock()  { r.mu.Unlock() }

func NewRoot() *Root {
    r := &Root{
        Machines:   &velox.VMap[string, *Machine]{},
        Users:      &velox.VMap[string, *User]{},
        WebClients: &velox.VMap[string, *WebClient]{},
    }
    r.State.Data = velox.Marshal(r)
    velox.BindAll(r, r, &r.State)  // r is RLocker, &r.State is Pusher
    return r
}
```

```go
// internal/app/state/state_machine.go
package state

type Machine struct {
    mu sync.RWMutex `json:"-"`
    velox.State
    ID           string                        `json:"id"`
    Hostname     string                        `json:"hostname"`
    Timezone     string                        `json:"timezone"`
    HomeDir      string                        `json:"homeDir"`
    IPs          *velox.VSlice[string]         `json:"ips"`
    Version      string                        `json:"version,omitzero"`
    Compiled     string                        `json:"compiled,omitzero"`
    Channel      string                        `json:"channel,omitzero"`
    Capabilities *Capabilities                 `json:"capabilities,omitempty"`
    Projects     *velox.VMap[string, Project]  `json:"projects"`
    Terminals    *velox.VMap[string, Terminal] `json:"terminals"`
}

func (m *Machine) RLock()   { m.mu.RLock() }
func (m *Machine) RUnlock() { m.mu.RUnlock() }
func (m *Machine) Lock()    { m.mu.Lock() }
func (m *Machine) Unlock()  { m.mu.Unlock() }

func NewMachine() *Machine {
    m := &Machine{
        IPs:       &velox.VSlice[string]{},
        Projects:  &velox.VMap[string, Project]{},
        Terminals: &velox.VMap[string, Terminal]{},
    }
    m.State.Data = velox.Marshal(m)
    velox.BindAll(m, m, &m.State)  // m is RLocker, &m.State is Pusher
    return m
}
```

---

## Usage Examples

```go
// Set
m.state.Terminals.Set(id, state.Terminal{Cols: 80, Rows: 24})

// Partial update
m.state.Terminals.Update(id, func(t *state.Terminal) {
    t.Viewers = 5
})

// Delete
m.state.Terminals.Delete(id)

// Read
t, ok := m.state.Terminals.Get(id)

// Batch
m.state.Projects.Batch(func(projects map[string]state.Project) {
    for name, p := range projects {
        p.LastUpdated = time.Now().Unix()
        projects[name] = p
    }
})
```

---

## Migration Plan

### Phase 1: Update velox
1. Add `RLocker` interface (embeds `sync.Locker`, adds `RLock`/`RUnlock`)
2. Add `Bindable` interface and `BindAll()` function
3. Add `VMap[K,V]` and `VSlice[V]` types
4. Update `Marshal()` to use RLock when available
5. Update client to call `BindAll()` after unmarshal

### Phase 2: Update rais state types
1. Add `RLock`/`RUnlock` methods to Root and Machine
2. Change map fields to `*velox.VMap`
3. Change slice fields to `*velox.VSlice`
4. Update constructors to use `velox.BindAll()`

### Phase 3: Migrate callers
1. Replace `Lock/map access/Unlock/Push` with VMap methods
2. Replace `Lock/slice access/Unlock/Push` with VSlice methods

### Phase 4: Cleanup
1. Remove manual Lock/Unlock calls
2. Remove direct map/slice field access

---

## Benefits

1. **No boilerplate** - Generic containers work for all types
2. **RWMutex** - Reads don't block reads (when RLocker available)
3. **Fallback** - Works with plain sync.Locker (uses Lock for reads)
4. **Automatic Push** - Server-side VMap calls Push after mutations
5. **Client support** - VMap works on client (no Push, just locking)
6. **Auto-bind** - velox binds VMap/VSlice after unmarshal
