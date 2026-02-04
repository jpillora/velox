package velox_test

import (
	"sync"
	"sync/atomic"
	"testing"

	velox "github.com/jpillora/velox/go"
)

// TestMarshalWithRLocker tests that Marshal uses RLock when available
func TestMarshalWithRLocker(t *testing.T) {
	rl := &RLockerStruct{Value: 42}

	// Create marshal func
	marshal := velox.Marshal(rl)

	// Marshal should use RLock
	data, err := marshal()
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	if string(data) != `{"value":42}` {
		t.Errorf("Marshal result = %s, want {\"value\":42}", string(data))
	}

	// Verify RLock was used
	if rl.rlockCount.Load() != 1 {
		t.Errorf("RLock count = %d, want 1", rl.rlockCount.Load())
	}

	if rl.lockCount.Load() != 0 {
		t.Errorf("Lock count = %d, want 0", rl.lockCount.Load())
	}
}

// RLockerStruct implements RLocker for testing
type RLockerStruct struct {
	mu         sync.RWMutex
	lockCount  atomic.Int32
	rlockCount atomic.Int32
	Value      int `json:"value"`
}

func (r *RLockerStruct) Lock() {
	r.lockCount.Add(1)
	r.mu.Lock()
}

func (r *RLockerStruct) Unlock() {
	r.mu.Unlock()
}

func (r *RLockerStruct) RLock() {
	r.rlockCount.Add(1)
	r.mu.RLock()
}

func (r *RLockerStruct) RUnlock() {
	r.mu.RUnlock()
}

func TestMarshalUsesRLock(t *testing.T) {
	rl := &RLockerStruct{Value: 42}

	marshal := velox.Marshal(rl)

	// Call marshal multiple times
	for i := 0; i < 5; i++ {
		_, err := marshal()
		if err != nil {
			t.Fatalf("Marshal error: %v", err)
		}
	}

	// Should have used RLock, not Lock
	if rl.rlockCount.Load() != 5 {
		t.Errorf("RLock count = %d, want 5", rl.rlockCount.Load())
	}

	if rl.lockCount.Load() != 0 {
		t.Errorf("Lock count = %d, want 0 (should use RLock instead)", rl.lockCount.Load())
	}
}

// LockerOnlyStruct implements only sync.Locker (not RLocker)
type LockerOnlyStruct struct {
	mu        sync.Mutex
	lockCount atomic.Int32
	Value     int `json:"value"`
}

func (l *LockerOnlyStruct) Lock() {
	l.lockCount.Add(1)
	l.mu.Lock()
}

func (l *LockerOnlyStruct) Unlock() {
	l.mu.Unlock()
}

func TestMarshalFallsBackToLock(t *testing.T) {
	lo := &LockerOnlyStruct{Value: 100}

	marshal := velox.Marshal(lo)

	// Call marshal
	data, err := marshal()
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	if string(data) != `{"value":100}` {
		t.Errorf("Marshal result = %s, want {\"value\":100}", string(data))
	}

	// Should have used Lock (fallback when RLock not available)
	if lo.lockCount.Load() != 1 {
		t.Errorf("Lock count = %d, want 1", lo.lockCount.Load())
	}
}

func TestMarshalWithoutLocker(t *testing.T) {
	type NoLockStruct struct {
		Value int `json:"value"`
	}

	s := &NoLockStruct{Value: 123}

	marshal := velox.Marshal(s)

	// Should work without implementing Locker
	data, err := marshal()
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	if string(data) != `{"value":123}` {
		t.Errorf("Marshal result = %s, want {\"value\":123}", string(data))
	}
}

func TestRLockerInterface(t *testing.T) {
	// Verify that RLocker extends sync.Locker
	var _ velox.RLocker = &mockRWLocker{}

	// Verify sync.RWMutex satisfies RLocker
	var mu sync.RWMutex
	var _ velox.RLocker = &mu
}

func TestMarshalConcurrentReads(t *testing.T) {
	rl := &RLockerStruct{Value: 42}

	marshal := velox.Marshal(rl)

	// Concurrent marshals should all use RLock (non-blocking for each other)
	var wg sync.WaitGroup
	const goroutines = 10

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_, err := marshal()
				if err != nil {
					t.Errorf("Marshal error: %v", err)
				}
			}
		}()
	}

	wg.Wait()

	// All should have used RLock
	expectedRLocks := int32(goroutines * 100)
	if rl.rlockCount.Load() != expectedRLocks {
		t.Errorf("RLock count = %d, want %d", rl.rlockCount.Load(), expectedRLocks)
	}

	if rl.lockCount.Load() != 0 {
		t.Errorf("Lock count = %d, want 0", rl.lockCount.Load())
	}
}
