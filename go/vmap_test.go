package velox_test

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"

	velox "github.com/jpillora/velox/go"
)

// mockPusher implements Pusher for testing
type mockPusher struct {
	count atomic.Int32
}

func (p *mockPusher) Push() bool {
	p.count.Add(1)
	return true
}

// mockRWLocker implements RLocker for testing
type mockRWLocker struct {
	mu         sync.RWMutex
	lockCount  atomic.Int32
	rlockCount atomic.Int32
}

func (m *mockRWLocker) Lock()    { m.lockCount.Add(1); m.mu.Lock() }
func (m *mockRWLocker) Unlock()  { m.mu.Unlock() }
func (m *mockRWLocker) RLock()   { m.rlockCount.Add(1); m.mu.RLock() }
func (m *mockRWLocker) RUnlock() { m.mu.RUnlock() }

// mockLocker implements sync.Locker (not RLocker) for testing fallback
type mockLocker struct {
	mu        sync.Mutex
	lockCount atomic.Int32
}

func (m *mockLocker) Lock()   { m.lockCount.Add(1); m.mu.Lock() }
func (m *mockLocker) Unlock() { m.mu.Unlock() }

func TestVMapBasicOperations(t *testing.T) {
	vm := &velox.VMap[string, int]{}

	// Test without binding (no locker/pusher)
	vm.Set("a", 1)
	vm.Set("b", 2)
	vm.Set("c", 3)

	if got := vm.Len(); got != 3 {
		t.Errorf("Len() = %d, want 3", got)
	}

	if v, ok := vm.Get("a"); !ok || v != 1 {
		t.Errorf("Get(a) = %v, %v, want 1, true", v, ok)
	}

	if v, ok := vm.Get("nonexistent"); ok {
		t.Errorf("Get(nonexistent) = %v, %v, want zero, false", v, ok)
	}

	if !vm.Has("b") {
		t.Error("Has(b) = false, want true")
	}

	if vm.Has("nonexistent") {
		t.Error("Has(nonexistent) = true, want false")
	}

	vm.Delete("b")
	if vm.Has("b") {
		t.Error("Has(b) after delete = true, want false")
	}
}

func TestVMapKeys(t *testing.T) {
	vm := &velox.VMap[string, int]{}
	vm.Set("x", 1)
	vm.Set("y", 2)

	keys := vm.Keys()
	if len(keys) != 2 {
		t.Errorf("Keys() len = %d, want 2", len(keys))
	}

	keyMap := make(map[string]bool)
	for _, k := range keys {
		keyMap[k] = true
	}
	if !keyMap["x"] || !keyMap["y"] {
		t.Errorf("Keys() = %v, want [x, y]", keys)
	}
}

func TestVMapValues(t *testing.T) {
	vm := &velox.VMap[string, int]{}
	vm.Set("x", 10)
	vm.Set("y", 20)

	values := vm.Values()
	if len(values) != 2 {
		t.Errorf("Values() len = %d, want 2", len(values))
	}

	sum := 0
	for _, v := range values {
		sum += v
	}
	if sum != 30 {
		t.Errorf("Values() sum = %d, want 30", sum)
	}
}

func TestVMapSnapshot(t *testing.T) {
	vm := &velox.VMap[string, int]{}
	vm.Set("a", 1)
	vm.Set("b", 2)

	snap := vm.Snapshot()
	if len(snap) != 2 {
		t.Errorf("Snapshot() len = %d, want 2", len(snap))
	}
	if snap["a"] != 1 || snap["b"] != 2 {
		t.Errorf("Snapshot() = %v, want map[a:1 b:2]", snap)
	}

	// Modify snapshot shouldn't affect original
	snap["c"] = 3
	if vm.Has("c") {
		t.Error("Modifying snapshot affected original map")
	}
}

func TestVMapUpdate(t *testing.T) {
	vm := &velox.VMap[string, int]{}
	vm.Set("counter", 10)

	// Update existing key
	ok := vm.Update("counter", func(v *int) {
		*v += 5
	})
	if !ok {
		t.Error("Update() returned false for existing key")
	}

	if v, _ := vm.Get("counter"); v != 15 {
		t.Errorf("After Update(), Get(counter) = %d, want 15", v)
	}

	// Update non-existent key
	ok = vm.Update("nonexistent", func(v *int) {
		*v = 100
	})
	if ok {
		t.Error("Update() returned true for non-existent key")
	}
}

func TestVMapBatch(t *testing.T) {
	pusher := &mockPusher{}
	locker := &mockLocker{}

	vm := &velox.VMap[string, int]{}
	velox.BindAll(vm, locker, pusher)

	vm.Batch(func(data map[string]int) {
		data["a"] = 1
		data["b"] = 2
		data["c"] = 3
	})

	if vm.Len() != 3 {
		t.Errorf("After Batch(), Len() = %d, want 3", vm.Len())
	}

	// Batch should trigger exactly one push
	if pusher.count.Load() != 1 {
		t.Errorf("Batch() triggered %d pushes, want 1", pusher.count.Load())
	}
}

func TestVMapClear(t *testing.T) {
	pusher := &mockPusher{}
	vm := &velox.VMap[string, int]{}
	velox.BindAll(vm, nil, pusher)

	vm.Set("a", 1)
	vm.Set("b", 2)
	pushCount := pusher.count.Load()

	vm.Clear()

	if vm.Len() != 0 {
		t.Errorf("After Clear(), Len() = %d, want 0", vm.Len())
	}

	if pusher.count.Load() != pushCount+1 {
		t.Error("Clear() did not trigger push")
	}
}

func TestVMapRange(t *testing.T) {
	vm := &velox.VMap[string, int]{}
	vm.Set("a", 1)
	vm.Set("b", 2)
	vm.Set("c", 3)

	sum := 0
	count := 0
	vm.Range(func(key string, value int) bool {
		sum += value
		count++
		return true
	})

	if count != 3 {
		t.Errorf("Range() iterated %d times, want 3", count)
	}
	if sum != 6 {
		t.Errorf("Range() sum = %d, want 6", sum)
	}

	// Test early termination
	count = 0
	vm.Range(func(key string, value int) bool {
		count++
		return count < 2
	})
	if count != 2 {
		t.Errorf("Range() with early termination iterated %d times, want 2", count)
	}
}

func TestVMapWithRLocker(t *testing.T) {
	locker := &mockRWLocker{}
	pusher := &mockPusher{}

	vm := &velox.VMap[string, int]{}
	velox.BindAll(vm, locker, pusher)

	// Write operations should use Lock
	vm.Set("key", 42)
	if locker.lockCount.Load() < 1 {
		t.Error("Set() did not use Lock()")
	}

	// Read operations should use RLock
	initialRLock := locker.rlockCount.Load()
	vm.Get("key")
	if locker.rlockCount.Load() <= initialRLock {
		t.Error("Get() did not use RLock()")
	}

	vm.Len()
	vm.Keys()
	vm.Values()
	vm.Has("key")
	vm.Snapshot()

	if locker.rlockCount.Load() < initialRLock+5 {
		t.Errorf("Read operations did not use RLock(), count=%d", locker.rlockCount.Load())
	}
}

func TestVMapWithLockerFallback(t *testing.T) {
	locker := &mockLocker{}

	vm := &velox.VMap[string, int]{}
	velox.BindAll(vm, locker, nil)

	initialLock := locker.lockCount.Load()

	// Read operations should fall back to Lock when RLock not available
	vm.Get("key")
	vm.Len()
	vm.Keys()

	if locker.lockCount.Load() <= initialLock {
		t.Error("Read operations did not fall back to Lock()")
	}
}

func TestVMapWithPusher(t *testing.T) {
	pusher := &mockPusher{}

	vm := &velox.VMap[string, int]{}
	velox.BindAll(vm, nil, pusher)

	// Write operations should trigger push
	vm.Set("a", 1)
	if pusher.count.Load() != 1 {
		t.Errorf("Set() pushed %d times, want 1", pusher.count.Load())
	}

	vm.Delete("a")
	if pusher.count.Load() != 2 {
		t.Errorf("Delete() pushed %d times total, want 2", pusher.count.Load())
	}

	vm.Set("b", 2)
	vm.Update("b", func(v *int) { *v++ })
	if pusher.count.Load() != 4 {
		t.Errorf("Update() pushed %d times total, want 4", pusher.count.Load())
	}
}

func TestVMapJSON(t *testing.T) {
	vm := &velox.VMap[string, int]{}
	vm.Set("a", 1)
	vm.Set("b", 2)

	// Marshal
	data, err := json.Marshal(vm)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	// Verify JSON
	var raw map[string]int
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal raw error = %v", err)
	}
	if raw["a"] != 1 || raw["b"] != 2 {
		t.Errorf("Marshal() = %s, want {a:1, b:2}", string(data))
	}

	// Unmarshal into new VMap
	vm2 := &velox.VMap[string, int]{}
	if err := json.Unmarshal(data, vm2); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if v, ok := vm2.Get("a"); !ok || v != 1 {
		t.Errorf("After Unmarshal, Get(a) = %v, %v", v, ok)
	}
}

func TestVMapJSONEmpty(t *testing.T) {
	vm := &velox.VMap[string, int]{}

	data, err := json.Marshal(vm)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	if string(data) != "{}" {
		t.Errorf("Marshal() empty = %s, want {}", string(data))
	}
}

func TestVMapJSONUnmarshalClears(t *testing.T) {
	vm := &velox.VMap[string, int]{}
	vm.Set("old", 100)

	// Unmarshal new data (without "old" key)
	if err := json.Unmarshal([]byte(`{"new": 200}`), vm); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	// Old key should be gone
	if vm.Has("old") {
		t.Error("Unmarshal did not clear existing entries")
	}

	if v, ok := vm.Get("new"); !ok || v != 200 {
		t.Errorf("Get(new) = %v, %v, want 200, true", v, ok)
	}
}

func TestVMapConcurrency(t *testing.T) {
	locker := &mockRWLocker{}
	vm := &velox.VMap[int, int]{}
	velox.BindAll(vm, locker, nil)

	var wg sync.WaitGroup
	const goroutines = 10
	const iterations = 100

	// Concurrent writes
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				vm.Set(id*iterations+j, j)
			}
		}(i)
	}

	// Concurrent reads
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				vm.Len()
				vm.Keys()
				vm.Snapshot()
			}
		}()
	}

	wg.Wait()

	if vm.Len() != goroutines*iterations {
		t.Errorf("After concurrent ops, Len() = %d, want %d", vm.Len(), goroutines*iterations)
	}
}

func TestVMapWithoutBinding(t *testing.T) {
	// VMap should work without binding (nil locker and pusher)
	vm := &velox.VMap[string, string]{}

	vm.Set("key", "value")
	if v, ok := vm.Get("key"); !ok || v != "value" {
		t.Errorf("Get() = %v, %v, want value, true", v, ok)
	}

	vm.Delete("key")
	if vm.Has("key") {
		t.Error("Delete() did not work without binding")
	}

	vm.Batch(func(data map[string]string) {
		data["a"] = "A"
	})
	if !vm.Has("a") {
		t.Error("Batch() did not work without binding")
	}
}

func TestVMapBatchWithNilData(t *testing.T) {
	// Batch on uninitialized VMap should create the map
	vm := &velox.VMap[string, int]{}

	// Call Batch without any prior operations (data is nil)
	vm.Batch(func(data map[string]int) {
		data["first"] = 1
		data["second"] = 2
	})

	if vm.Len() != 2 {
		t.Errorf("After Batch on nil data, Len() = %d, want 2", vm.Len())
	}

	if v, ok := vm.Get("first"); !ok || v != 1 {
		t.Errorf("Get(first) = %v, %v, want 1, true", v, ok)
	}
}
