package velox

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
)

type bindingContainers struct {
	Items VMap[string, int] `json:"items"`
	List  VSlice[int]       `json:"list"`
}

type bindingClientData struct {
	State
	Nested  bindingContainers `json:"nested"`
	inspect func()
}

func (d *bindingClientData) UnmarshalJSON(body []byte) error {
	type plain bindingClientData
	if err := json.Unmarshal(body, (*plain)(d)); err != nil {
		return err
	}
	if d.inspect != nil {
		d.inspect()
	}
	return nil
}

type bindingTestLocker struct {
	sync.RWMutex
	reading chan struct{}
}

func (l *bindingTestLocker) RLock() {
	if l.reading != nil {
		l.reading <- struct{}{}
	}
	l.RWMutex.RLock()
}

func TestClientContainerBindingsSurviveUpdates(t *testing.T) {
	locker := &bindingTestLocker{}
	d := &bindingClientData{State: State{Locker: locker}}
	c, err := NewClient("http://unused", d)
	if err != nil {
		t.Fatal(err)
	}
	if d.Nested.Items.binding.Load() == nil || d.Nested.List.binding.Load() == nil {
		t.Fatal("client containers must be bound before the first update")
	}
	if err := c.applyUpdate(&Update{Version: 1, Body: json.RawMessage(`{"nested":{"items":{"old":1},"list":[1]}}`)}); err != nil {
		t.Fatal(err)
	}

	for _, body := range []string{
		`{}`,
		`{"nested":{"items":{"new":2},"list":[2]}}`,
	} {
		locker.reading = make(chan struct{}, 1)
		var readers []chan struct{}
		d.inspect = func() {
			for _, read := range []func(){
				func() { d.Nested.Items.Range(func(string, int) bool { return true }) },
				func() { d.Nested.List.Range(func(int, int) bool { return true }) },
			} {
				done := make(chan struct{})
				readers = append(readers, done)
				go func() { read(); close(done) }()
				select {
				case <-locker.reading:
				case <-done:
					t.Error("container reader bypassed the update's write lock")
				case <-time.After(3 * time.Second):
					t.Error("reader did not attempt to acquire the state lock")
				}
			}
		}
		if err := c.applyUpdate(&Update{Version: c.Version() + 1, Body: json.RawMessage(body)}); err != nil {
			t.Fatal(err)
		}
		for _, done := range readers {
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("reader remained blocked after the update")
			}
		}
		locker.reading = nil
		if body == `{}` && (d.Nested.Items.Len() != 0 || d.Nested.List.Len() != 0) {
			t.Fatal("omitted containers retained stale data")
		}
	}
	if value, ok := d.Nested.Items.Get("new"); !ok || value != 2 {
		t.Fatal("updated map value missing")
	}
	if value, ok := d.Nested.List.At(0); !ok || value != 2 {
		t.Fatal("updated slice value missing")
	}
}

type bindingCountingLocker struct {
	locks, unlocks int
}

func (l *bindingCountingLocker) Lock()   { l.locks++ }
func (l *bindingCountingLocker) Unlock() { l.unlocks++ }

func TestContainerOperationsReleaseAcquiredLock(t *testing.T) {
	for _, name := range []string{"map read", "map write", "slice read", "slice write"} {
		t.Run(name, func(t *testing.T) {
			old, next := &bindingCountingLocker{}, &bindingCountingLocker{}
			var m VMap[string, int]
			var s VSlice[int]
			m.Set("key", 1)
			s.Append(1)
			m.bind(old, nil)
			s.bind(old, nil)
			switch name {
			case "map read":
				m.Range(func(string, int) bool { m.bind(next, nil); return false })
			case "map write":
				m.Update("key", func(*int) { m.bind(next, nil) })
			case "slice read":
				s.Range(func(int, int) bool { s.bind(next, nil); return false })
			case "slice write":
				s.Update(0, func(*int) { s.bind(next, nil) })
			}
			if old.locks != 1 || old.unlocks != 1 || next.unlocks != 0 {
				t.Fatalf("lock mismatch: old=%+v next=%+v", old, next)
			}
		})
	}
}
