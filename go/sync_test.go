package velox_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jpillora/eventsource"
	velox "github.com/jpillora/velox/go"
)

func TestBasicSync(t *testing.T) {
	// Create a test struct that embeds velox.State
	type TestStruct struct {
		velox.State
		Value string
	}
	test := &TestStruct{Value: "test-value"}
	handler := velox.SyncHandler(test)
	// setup HTTP server for testing
	server := httptest.NewServer(handler)
	defer server.Close()
	// Test successful sync connection
	req, err := http.NewRequest("GET", server.URL, nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatalf("HTTP request failed: %v", err)
	}
	defer resp.Body.Close()
	// Should get a 200 response with proper headers for event streaming
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Errorf("Expected Content-Type text/event-stream, got %s", resp.Header.Get("Content-Type"))
	}
	// decode 2 events
	d := eventsource.NewDecoder(resp.Body)
	e0 := &eventsource.Event{}
	if err := d.Decode(e0); err != nil {
		t.Fatalf("Failed to decode event 0: %v", err)
	}
	if e0.ID != "0" {
		t.Errorf("Expected event ID 0, got %s", e0.ID)
	}
	if string(e0.Data) != `{"ping":true}` {
		t.Errorf("Expected ping, got %s", e0.Data)
	}

	e1 := &eventsource.Event{}
	if err := d.Decode(e1); err != nil {
		t.Fatalf("Failed to decode event 1: %v", err)
	}
	if e1.ID != "1" {
		t.Errorf("Expected event ID 1, got %s", e1.ID)
	}

	u := &velox.Update{}
	if err := json.Unmarshal([]byte(e1.Data), u); err != nil {
		t.Fatalf("Failed to unmarshal event data: %v", err)
	}

	if string(u.Body) != `{"Value":"test-value"}` {
		t.Errorf("Expected event data to be %s, got %s", `{"Value":"test-value"}`, string(u.Body))
	}
}
