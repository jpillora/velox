package velox_test

import (
	"encoding/json"
	"sync"
	"testing"

	velox "github.com/jpillora/velox/go"
)

func TestVSliceBasicOperations(t *testing.T) {
	vs := &velox.VSlice[int]{}

	// Test without binding
	vs.Append(1, 2, 3)

	if got := vs.Len(); got != 3 {
		t.Errorf("Len() = %d, want 3", got)
	}

	if v, ok := vs.At(0); !ok || v != 1 {
		t.Errorf("At(0) = %v, %v, want 1, true", v, ok)
	}

	if v, ok := vs.At(2); !ok || v != 3 {
		t.Errorf("At(2) = %v, %v, want 3, true", v, ok)
	}

	// Out of bounds
	if _, ok := vs.At(-1); ok {
		t.Error("At(-1) should return false")
	}

	if _, ok := vs.At(100); ok {
		t.Error("At(100) should return false")
	}
}

func TestVSliceGet(t *testing.T) {
	vs := &velox.VSlice[string]{}
	vs.Set([]string{"a", "b", "c"})

	got := vs.Get()
	if len(got) != 3 {
		t.Errorf("Get() len = %d, want 3", len(got))
	}
	if got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("Get() = %v, want [a b c]", got)
	}

	// Modifying returned slice shouldn't affect original
	got[0] = "modified"
	if v, _ := vs.At(0); v == "modified" {
		t.Error("Modifying Get() result affected original slice")
	}
}

func TestVSliceGetNil(t *testing.T) {
	vs := &velox.VSlice[int]{}

	got := vs.Get()
	if got != nil {
		t.Errorf("Get() on empty VSlice = %v, want nil", got)
	}
}

func TestVSliceSet(t *testing.T) {
	pusher := &mockPusher{}
	vs := &velox.VSlice[int]{}
	velox.BindAll(vs, nil, pusher)

	vs.Set([]int{10, 20, 30})

	if vs.Len() != 3 {
		t.Errorf("After Set(), Len() = %d, want 3", vs.Len())
	}

	if v, _ := vs.At(1); v != 20 {
		t.Errorf("At(1) = %d, want 20", v)
	}

	if pusher.count.Load() != 1 {
		t.Errorf("Set() triggered %d pushes, want 1", pusher.count.Load())
	}
}

func TestVSliceAppend(t *testing.T) {
	pusher := &mockPusher{}
	vs := &velox.VSlice[int]{}
	velox.BindAll(vs, nil, pusher)

	vs.Append(1)
	vs.Append(2, 3)

	if vs.Len() != 3 {
		t.Errorf("After Append(), Len() = %d, want 3", vs.Len())
	}

	if pusher.count.Load() != 2 {
		t.Errorf("Append() triggered %d pushes, want 2", pusher.count.Load())
	}
}

func TestVSliceSetAt(t *testing.T) {
	vs := &velox.VSlice[int]{}
	vs.Set([]int{1, 2, 3})

	if !vs.SetAt(1, 20) {
		t.Error("SetAt(1) returned false")
	}

	if v, _ := vs.At(1); v != 20 {
		t.Errorf("After SetAt, At(1) = %d, want 20", v)
	}

	// Out of bounds
	if vs.SetAt(-1, 0) {
		t.Error("SetAt(-1) returned true")
	}
	if vs.SetAt(100, 0) {
		t.Error("SetAt(100) returned true")
	}
}

func TestVSliceDeleteAt(t *testing.T) {
	vs := &velox.VSlice[string]{}
	vs.Set([]string{"a", "b", "c"})

	if !vs.DeleteAt(1) {
		t.Error("DeleteAt(1) returned false")
	}

	if vs.Len() != 2 {
		t.Errorf("After DeleteAt, Len() = %d, want 2", vs.Len())
	}

	got := vs.Get()
	if got[0] != "a" || got[1] != "c" {
		t.Errorf("After DeleteAt, Get() = %v, want [a c]", got)
	}

	// Out of bounds
	if vs.DeleteAt(-1) {
		t.Error("DeleteAt(-1) returned true")
	}
	if vs.DeleteAt(100) {
		t.Error("DeleteAt(100) returned true")
	}
}

func TestVSliceUpdate(t *testing.T) {
	vs := &velox.VSlice[int]{}
	vs.Set([]int{10, 20, 30})

	ok := vs.Update(1, func(v *int) {
		*v += 5
	})
	if !ok {
		t.Error("Update(1) returned false")
	}

	if v, _ := vs.At(1); v != 25 {
		t.Errorf("After Update, At(1) = %d, want 25", v)
	}

	// Out of bounds
	if vs.Update(-1, func(v *int) {}) {
		t.Error("Update(-1) returned true")
	}
	if vs.Update(100, func(v *int) {}) {
		t.Error("Update(100) returned true")
	}
}

func TestVSliceBatch(t *testing.T) {
	pusher := &mockPusher{}
	vs := &velox.VSlice[int]{}
	velox.BindAll(vs, nil, pusher)

	vs.Batch(func(data *[]int) {
		*data = append(*data, 1, 2, 3)
		(*data)[0] = 10
	})

	if vs.Len() != 3 {
		t.Errorf("After Batch(), Len() = %d, want 3", vs.Len())
	}

	if v, _ := vs.At(0); v != 10 {
		t.Errorf("At(0) = %d, want 10", v)
	}

	// Batch should trigger exactly one push
	if pusher.count.Load() != 1 {
		t.Errorf("Batch() triggered %d pushes, want 1", pusher.count.Load())
	}
}

func TestVSliceClear(t *testing.T) {
	pusher := &mockPusher{}
	vs := &velox.VSlice[int]{}
	velox.BindAll(vs, nil, pusher)

	vs.Set([]int{1, 2, 3})
	pushCount := pusher.count.Load()

	vs.Clear()

	if vs.Len() != 0 {
		t.Errorf("After Clear(), Len() = %d, want 0", vs.Len())
	}

	if pusher.count.Load() != pushCount+1 {
		t.Error("Clear() did not trigger push")
	}
}

func TestVSliceRange(t *testing.T) {
	vs := &velox.VSlice[int]{}
	vs.Set([]int{1, 2, 3, 4, 5})

	sum := 0
	count := 0
	vs.Range(func(index int, value int) bool {
		if index != count {
			t.Errorf("Range index = %d, expected %d", index, count)
		}
		sum += value
		count++
		return true
	})

	if count != 5 {
		t.Errorf("Range() iterated %d times, want 5", count)
	}
	if sum != 15 {
		t.Errorf("Range() sum = %d, want 15", sum)
	}

	// Test early termination
	count = 0
	vs.Range(func(index int, value int) bool {
		count++
		return count < 3
	})
	if count != 3 {
		t.Errorf("Range() with early termination iterated %d times, want 3", count)
	}
}

func TestVSliceWithRLocker(t *testing.T) {
	locker := &mockRWLocker{}
	pusher := &mockPusher{}

	vs := &velox.VSlice[int]{}
	velox.BindAll(vs, locker, pusher)

	// Write operations should use Lock
	vs.Set([]int{1, 2, 3})
	if locker.lockCount.Load() < 1 {
		t.Error("Set() did not use Lock()")
	}

	// Read operations should use RLock
	initialRLock := locker.rlockCount.Load()
	vs.Get()
	vs.Len()
	vs.At(0)

	if locker.rlockCount.Load() < initialRLock+3 {
		t.Errorf("Read operations did not use RLock(), count=%d", locker.rlockCount.Load())
	}
}

func TestVSliceWithLockerFallback(t *testing.T) {
	locker := &mockLocker{}

	vs := &velox.VSlice[int]{}
	velox.BindAll(vs, locker, nil)

	vs.Set([]int{1})
	initialLock := locker.lockCount.Load()

	// Read operations should fall back to Lock when RLock not available
	vs.Get()
	vs.Len()
	vs.At(0)

	if locker.lockCount.Load() <= initialLock {
		t.Error("Read operations did not fall back to Lock()")
	}
}

func TestVSliceWithPusher(t *testing.T) {
	pusher := &mockPusher{}

	vs := &velox.VSlice[int]{}
	velox.BindAll(vs, nil, pusher)

	vs.Append(1)
	if pusher.count.Load() != 1 {
		t.Errorf("Append() pushed %d times, want 1", pusher.count.Load())
	}

	vs.SetAt(0, 10) // Out of bounds after append, but index 0 should work
	if pusher.count.Load() != 2 {
		t.Errorf("SetAt() pushed %d times total, want 2", pusher.count.Load())
	}

	vs.DeleteAt(0)
	if pusher.count.Load() != 3 {
		t.Errorf("DeleteAt() pushed %d times total, want 3", pusher.count.Load())
	}
}

func TestVSliceJSON(t *testing.T) {
	vs := &velox.VSlice[int]{}
	vs.Set([]int{1, 2, 3})

	// Marshal
	data, err := json.Marshal(vs)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	if string(data) != "[1,2,3]" {
		t.Errorf("Marshal() = %s, want [1,2,3]", string(data))
	}

	// Unmarshal into new VSlice
	vs2 := &velox.VSlice[int]{}
	if err := json.Unmarshal(data, vs2); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if vs2.Len() != 3 {
		t.Errorf("After Unmarshal, Len() = %d, want 3", vs2.Len())
	}
}

func TestVSliceJSONEmpty(t *testing.T) {
	vs := &velox.VSlice[int]{}

	data, err := json.Marshal(vs)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	if string(data) != "[]" {
		t.Errorf("Marshal() empty = %s, want []", string(data))
	}
}

func TestVSliceJSONUnmarshalClears(t *testing.T) {
	vs := &velox.VSlice[int]{}
	vs.Set([]int{100, 200, 300})

	// Unmarshal new data
	if err := json.Unmarshal([]byte(`[1, 2]`), vs); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	// Old data should be gone
	if vs.Len() != 2 {
		t.Errorf("Unmarshal() did not replace, Len() = %d, want 2", vs.Len())
	}

	if v, _ := vs.At(0); v != 1 {
		t.Errorf("At(0) = %d, want 1", v)
	}
}

func TestVSliceConcurrency(t *testing.T) {
	locker := &mockRWLocker{}
	vs := &velox.VSlice[int]{}
	velox.BindAll(vs, locker, nil)

	vs.Set(make([]int, 100))

	var wg sync.WaitGroup
	const goroutines = 10
	const iterations = 100

	// Concurrent writes
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				idx := (id*iterations + j) % 100
				vs.SetAt(idx, j)
			}
		}(i)
	}

	// Concurrent reads
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				vs.Len()
				vs.Get()
				vs.At(j % 100)
			}
		}()
	}

	wg.Wait()

	if vs.Len() != 100 {
		t.Errorf("After concurrent ops, Len() = %d, want 100", vs.Len())
	}
}

func TestVSliceWithoutBinding(t *testing.T) {
	// VSlice should work without binding (nil locker and pusher)
	vs := &velox.VSlice[string]{}

	vs.Append("hello")
	if v, ok := vs.At(0); !ok || v != "hello" {
		t.Errorf("At(0) = %v, %v, want hello, true", v, ok)
	}

	vs.SetAt(0, "world")
	if v, _ := vs.At(0); v != "world" {
		t.Errorf("After SetAt, At(0) = %v, want world", v)
	}

	vs.DeleteAt(0)
	if vs.Len() != 0 {
		t.Errorf("After DeleteAt, Len() = %d, want 0", vs.Len())
	}
}

func TestVSliceWithStructType(t *testing.T) {
	type Item struct {
		ID   int
		Name string
	}

	vs := &velox.VSlice[Item]{}
	vs.Append(Item{1, "first"}, Item{2, "second"})

	if vs.Len() != 2 {
		t.Errorf("Len() = %d, want 2", vs.Len())
	}

	vs.Update(0, func(item *Item) {
		item.Name = "updated"
	})

	if item, ok := vs.At(0); !ok || item.Name != "updated" {
		t.Errorf("At(0).Name = %v, want updated", item.Name)
	}

	// Test JSON with struct
	data, err := json.Marshal(vs)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	vs2 := &velox.VSlice[Item]{}
	if err := json.Unmarshal(data, vs2); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if vs2.Len() != 2 {
		t.Errorf("After Unmarshal, Len() = %d, want 2", vs2.Len())
	}
}
