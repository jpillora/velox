package velox

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestGopushByteIdenticalStateLeavesCacheUntouched(t *testing.T) {
	initial := []byte(`{"name":"unchanged","nested":{"value":1}}`)
	s := directGopushState(append([]byte(nil), initial...))
	s.data.bytes = append([]byte(nil), initial...)
	s.data.delta = []byte(`{"existing":true}`)
	s.data.version = 7
	if _, err := s.data.patcher.patch(initial); err != nil {
		t.Fatal(err)
	}

	bytesPointer := reflect.ValueOf(s.data.bytes).Pointer()
	deltaPointer := reflect.ValueOf(s.data.delta).Pointer()
	prevCache := s.data.patcher.prev
	prevSnapshot := patcherSnapshot(t, prevCache)

	s.gopush()

	if got := reflect.ValueOf(s.data.bytes).Pointer(); got != bytesPointer {
		t.Fatalf("cached bytes were replaced: got pointer %x, want %x", got, bytesPointer)
	}
	if got := reflect.ValueOf(s.data.delta).Pointer(); got != deltaPointer {
		t.Fatalf("delta was replaced: got pointer %x, want %x", got, deltaPointer)
	}
	if s.data.version != 7 {
		t.Fatalf("version = %d, want 7", s.data.version)
	}
	prevPointer := reflect.ValueOf(prevCache).Pointer()
	if got := reflect.ValueOf(s.data.patcher.prev).Pointer(); got != prevPointer {
		t.Fatalf("patcher cache was replaced: got pointer %x, want %x", got, prevPointer)
	}
	if got := patcherSnapshot(t, s.data.patcher.prev); !bytes.Equal(got, prevSnapshot) {
		t.Fatalf("patcher cache changed: got %s, want %s", got, prevSnapshot)
	}
}

func TestGopushChangedStateUpdatesDataAndCache(t *testing.T) {
	initial := []byte(`{"keep":1,"value":"before"}`)
	changed := []byte(`{"keep":1,"value":"after"}`)
	s := directGopushState(changed)
	s.data.bytes = append([]byte(nil), initial...)
	s.data.delta = []byte(`{"stale":true}`)
	s.data.version = 7
	if _, err := s.data.patcher.patch(initial); err != nil {
		t.Fatal(err)
	}
	prevCache := s.data.patcher.prev

	s.gopush()

	if !bytes.Equal(s.data.bytes, changed) {
		t.Fatalf("cached bytes = %s, want %s", s.data.bytes, changed)
	}
	if !bytes.Equal(s.data.delta, []byte(`{"value":"after"}`)) {
		t.Fatalf("delta = %s, want changed value", s.data.delta)
	}
	if s.data.version != 8 {
		t.Fatalf("version = %d, want 8", s.data.version)
	}
	if got := reflect.ValueOf(s.data.patcher.prev).Pointer(); got == reflect.ValueOf(prevCache).Pointer() {
		t.Fatalf("patcher cache pointer = %x, want a replacement", got)
	}
	wantPrev := map[string]interface{}{"keep": float64(1), "value": "after"}
	if !reflect.DeepEqual(s.data.patcher.prev, wantPrev) {
		t.Fatalf("patcher cache = %#v, want %#v", s.data.patcher.prev, wantPrev)
	}
}

func TestGopushOwnsMarshalBufferSnapshots(t *testing.T) {
	buffer := json.RawMessage(`{"value":"A"}`)
	s := New(func() (json.RawMessage, error) {
		return buffer, nil
	})
	s.Throttle = 0

	if reflect.ValueOf(s.data.bytes).Pointer() == reflect.ValueOf(buffer).Pointer() {
		t.Fatal("initial cached bytes alias marshal buffer")
	}
	copy(buffer, `{"value":"B"}`)

	s.gopush()

	if !bytes.Equal(s.data.bytes, []byte(`{"value":"B"}`)) {
		t.Fatalf("cached bytes = %s, want value B", s.data.bytes)
	}
	if !bytes.Equal(s.data.delta, []byte(`{"value":"B"}`)) {
		t.Fatalf("delta = %s, want value B", s.data.delta)
	}
	if s.data.version != 2 {
		t.Fatalf("version = %d, want 2", s.data.version)
	}
	wantPrev := map[string]interface{}{"value": "B"}
	if !reflect.DeepEqual(s.data.patcher.prev, wantPrev) {
		t.Fatalf("patcher cache = %#v, want %#v", s.data.patcher.prev, wantPrev)
	}
	if reflect.ValueOf(s.data.bytes).Pointer() == reflect.ValueOf(buffer).Pointer() {
		t.Fatal("updated cached bytes alias marshal buffer")
	}

	copy(buffer, `{"value":"C"}`)
	if !bytes.Equal(s.data.bytes, []byte(`{"value":"B"}`)) {
		t.Fatalf("cached bytes changed after marshal buffer mutation: got %s, want value B", s.data.bytes)
	}
	if !reflect.DeepEqual(s.data.patcher.prev, wantPrev) {
		t.Fatalf("patcher cache changed after marshal buffer mutation: got %#v, want %#v", s.data.patcher.prev, wantPrev)
	}
}

func TestGopushInvalidByteIdenticalStateStillPanics(t *testing.T) {
	tests := []struct {
		name    string
		payload json.RawMessage
	}{
		{name: "nil", payload: nil},
		{name: "empty", payload: json.RawMessage{}},
		{name: "invalid", payload: json.RawMessage(`{`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &State{Data: func() (json.RawMessage, error) {
				return tt.payload, nil
			}}
			if err := s.init(); err != nil {
				t.Fatal(err)
			}
			s.Throttle = 0

			defer func() {
				recovered := recover()
				if recovered == nil {
					t.Fatal("gopush did not panic for invalid byte-identical state")
				}
				err, ok := recovered.(error)
				if !ok || !strings.HasPrefix(err.Error(), "create-patch: ") {
					t.Fatalf("gopush panic = %v, want create-patch error", recovered)
				}
			}()
			s.gopush()
		})
	}
}

func TestGopushNilBytesStillSeedsAndPatches(t *testing.T) {
	initial := []byte(`{"old":true}`)
	s := directGopushState([]byte(`{}`))
	s.data.version = 7
	if _, err := s.data.patcher.patch(initial); err != nil {
		t.Fatal(err)
	}

	s.gopush()

	if !bytes.Equal(s.data.bytes, []byte(`{}`)) {
		t.Fatalf("cached bytes = %s, want {}", s.data.bytes)
	}
	if !bytes.Equal(s.data.delta, []byte(`{"old":null}`)) {
		t.Fatalf("delta = %s, want key deletion", s.data.delta)
	}
	if s.data.version != 8 {
		t.Fatalf("version = %d, want 8", s.data.version)
	}
	if s.data.patcher.prev == nil || len(s.data.patcher.prev) != 0 {
		t.Fatalf("patcher cache = %#v, want non-nil empty map", s.data.patcher.prev)
	}
}

func TestGopushNullStillClearsByteIdenticalState(t *testing.T) {
	s := directGopushState([]byte("null"))
	s.data.bytes = []byte("null")
	s.data.delta = []byte(`{"existing":true}`)
	s.data.version = 7
	if _, err := s.data.patcher.patch([]byte(`{"old":true}`)); err != nil {
		t.Fatal(err)
	}
	prevCache := s.data.patcher.prev
	prevSnapshot := patcherSnapshot(t, prevCache)

	s.gopush()

	if s.data.bytes != nil {
		t.Fatalf("cached bytes = %s, want nil", s.data.bytes)
	}
	if s.data.delta != nil {
		t.Fatalf("delta = %s, want nil", s.data.delta)
	}
	if s.data.version != 8 {
		t.Fatalf("version = %d, want 8", s.data.version)
	}
	prevPointer := reflect.ValueOf(prevCache).Pointer()
	if got := reflect.ValueOf(s.data.patcher.prev).Pointer(); got != prevPointer {
		t.Fatalf("patcher cache was replaced: got pointer %x, want %x", got, prevPointer)
	}
	if got := patcherSnapshot(t, s.data.patcher.prev); !bytes.Equal(got, prevSnapshot) {
		t.Fatalf("patcher cache changed: got %s, want %s", got, prevSnapshot)
	}

	s.gopush()
	if s.data.bytes != nil || s.data.delta != nil {
		t.Fatalf("repeated null did not keep data cleared: bytes=%s delta=%s", s.data.bytes, s.data.delta)
	}
	if s.data.version != 9 {
		t.Fatalf("version after repeated null = %d, want 9", s.data.version)
	}
	if got := reflect.ValueOf(s.data.patcher.prev).Pointer(); got != prevPointer {
		t.Fatalf("repeated null replaced patcher cache: got pointer %x, want %x", got, prevPointer)
	}
	if got := patcherSnapshot(t, s.data.patcher.prev); !bytes.Equal(got, prevSnapshot) {
		t.Fatalf("repeated null changed patcher cache: got %s, want %s", got, prevSnapshot)
	}
}

func directGopushState(payload []byte) *State {
	s := &State{initd: true}
	s.Data = func() (json.RawMessage, error) {
		return payload, nil
	}
	return s
}

func patcherSnapshot(t *testing.T, cache map[string]interface{}) []byte {
	t.Helper()
	data, err := json.Marshal(cache)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
