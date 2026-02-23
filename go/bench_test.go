package velox

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
)

// benchState is a realistic struct that users would sync.
type benchState struct {
	sync.RWMutex
	State
	Counter  int               `json:"counter"`
	Name     string            `json:"name"`
	Users    map[string]string `json:"users"`
	Messages []string          `json:"messages"`
	Config   map[string]any    `json:"config"`
}

func newBenchState(nUsers, nMessages int) *benchState {
	s := &benchState{
		Name:     "test-server",
		Users:    make(map[string]string, nUsers),
		Messages: make([]string, 0, nMessages),
		Config: map[string]any{
			"debug":    false,
			"maxConns": 100,
			"version":  "1.0.0",
			"features": []string{"sync", "delta", "ws"},
		},
	}
	for i := range nUsers {
		s.Users[fmt.Sprintf("user-%d", i)] = "online"
	}
	for range nMessages {
		s.Messages = append(s.Messages, "message content here that is moderately sized")
	}
	return s
}

// -------------------------------------------------------------------
// Individual stages
// -------------------------------------------------------------------

// BenchmarkMarshal measures json.Marshal allocations on the user struct.
func BenchmarkMarshal(b *testing.B) {
	for _, size := range []struct {
		name              string
		nUsers, nMessages int
	}{
		{"small", 5, 10},
		{"medium", 50, 100},
		{"large", 500, 1000},
	} {
		s := newBenchState(size.nUsers, size.nMessages)
		b.Run(size.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				data, err := json.Marshal(s)
				if err != nil {
					b.Fatal(err)
				}
				_ = data
			}
		})
	}
}

// BenchmarkCreateMergePatch measures the cached merge patcher.
func BenchmarkCreateMergePatch(b *testing.B) {
	for _, size := range []struct {
		name              string
		nUsers, nMessages int
	}{
		{"small", 5, 10},
		{"medium", 50, 100},
		{"large", 500, 1000},
	} {
		s := newBenchState(size.nUsers, size.nMessages)
		oldBytes, _ := json.Marshal(s)
		s.Counter = 42
		s.Users["user-0"] = "offline"
		newBytes, _ := json.Marshal(s)

		mp := &mergePatcher{}
		mp.patch(oldBytes) // seed cache

		b.Run(size.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				delta, err := mp.patch(newBytes)
				if err != nil {
					b.Fatal(err)
				}
				_ = delta
			}
		})
	}
}

// BenchmarkUpdateMarshal measures json.Marshal of the Update envelope.
func BenchmarkUpdateMarshal(b *testing.B) {
	body, _ := json.Marshal(newBenchState(50, 100))
	upd := &Update{Version: 42, Body: body}

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		data, err := json.Marshal(upd)
		if err != nil {
			b.Fatal(err)
		}
		_ = data
	}
}

// -------------------------------------------------------------------
// Full push cycle
// -------------------------------------------------------------------

// BenchmarkFullPushCycle simulates the complete gopush hot path.
func BenchmarkFullPushCycle(b *testing.B) {
	s := newBenchState(50, 100)
	initBytes, _ := json.Marshal(s)

	mp := &mergePatcher{}
	mp.patch(initBytes)

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		s.Counter++
		newBytes, _ := json.Marshal(s)
		delta, _ := mp.patch(newBytes)
		upd := &Update{Version: int64(s.Counter), Delta: true, Body: delta}
		out, _ := json.Marshal(upd)
		_ = out
	}
}

// -------------------------------------------------------------------
// Pooled buffer encoder
// -------------------------------------------------------------------

var bufferPool = sync.Pool{
	New: func() any { return new(bytes.Buffer) },
}

func BenchmarkMarshalEncoder(b *testing.B) {
	for _, size := range []struct {
		name              string
		nUsers, nMessages int
	}{
		{"small", 5, 10},
		{"medium", 50, 100},
		{"large", 500, 1000},
	} {
		s := newBenchState(size.nUsers, size.nMessages)
		b.Run(size.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				buf := bufferPool.Get().(*bytes.Buffer)
				buf.Reset()
				if err := json.NewEncoder(buf).Encode(s); err != nil {
					b.Fatal(err)
				}
				_ = buf.Bytes()
				bufferPool.Put(buf)
			}
		})
	}
}

func BenchmarkUpdateMarshalEncoder(b *testing.B) {
	body, _ := json.Marshal(newBenchState(50, 100))
	upd := &Update{Version: 42, Body: body}

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		buf := bufferPool.Get().(*bytes.Buffer)
		buf.Reset()
		if err := json.NewEncoder(buf).Encode(upd); err != nil {
			b.Fatal(err)
		}
		_ = buf.Bytes()
		bufferPool.Put(buf)
	}
}
