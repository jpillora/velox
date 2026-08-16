package velox

import (
	"encoding/json"
	"testing"
)

func TestPatcherKeepsOnlyOwnedBuffers(t *testing.T) {
	owned := &mergePatcher{ownsInput: true}
	buf := []byte(`{"a":1}`)
	if _, err := owned.patch(buf); err != nil {
		t.Fatal(err)
	}
	if &owned.prev[0] != &buf[0] {
		t.Fatal("owned buffer was cloned instead of kept")
	}

	unowned := &mergePatcher{}
	if _, err := unowned.patch(buf); err != nil {
		t.Fatal(err)
	}
	if &unowned.prev[0] == &buf[0] {
		t.Fatal("caller's buffer was kept without ownership")
	}
}

func TestOwnedMarshalSurvivesOnlyVeloxData(t *testing.T) {
	s := NewAny(struct{ A int }{1})
	if !s.ownsData() {
		t.Fatal("NewAny's marshaller was not marked owned")
	}
	// Swapping in a caller's function must drop the hand-over: their buffer
	// may be reused between calls.
	s.Data = func() (json.RawMessage, error) { return json.RawMessage(`{"A":2}`), nil }
	if s.ownsData() {
		t.Fatal("a caller-supplied Data kept the owned mark")
	}
	// Swapping one velox marshaller for another keeps it: every Marshal
	// closure shares the same code and the same fresh-buffer behaviour.
	s.Data = Marshal(struct{ A int }{3})
	if !s.ownsData() {
		t.Fatal("a replacement velox marshaller lost the owned mark")
	}

	external := New(func() (json.RawMessage, error) { return json.RawMessage(`{}`), nil })
	if external.ownsData() {
		t.Fatal("an external MarshalFunc was marked owned")
	}
}
