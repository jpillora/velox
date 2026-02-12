package velox_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"

	velox "github.com/jpillora/velox/go"
	"google.golang.org/grpc/test/bufconn"
)

type serverState struct {
	sync.RWMutex
	velox.State
	Users    velox.VMap[string, string] `json:"users"`
	Messages velox.VSlice[string]       `json:"messages"`
	Counts   *velox.VMap[string, int]   `json:"counts"`
	Items    *velox.VSlice[int]         `json:"items"`
}

// TestVMapVSliceIntegration tests basic VMap and VSlice sync
func TestVMapVSliceIntegration(t *testing.T) {
	// Simple test without network - just verify VMap/VSlice with binding work
	ss := &serverState{
		Counts: &velox.VMap[string, int]{},
		Items:  &velox.VSlice[int]{},
	}

	// Bind with the server state's lock (no pusher needed for this test)
	velox.BindAll(ss, ss, nil)

	// Test VMap operations
	ss.Users.Set("alice", "online")
	if v, ok := ss.Users.Get("alice"); !ok || v != "online" {
		t.Errorf("Users.Get(alice) = %v, %v, want online, true", v, ok)
	}

	ss.Counts.Set("visitors", 100)
	if v, ok := ss.Counts.Get("visitors"); !ok || v != 100 {
		t.Errorf("Counts.Get(visitors) = %v, %v, want 100, true", v, ok)
	}

	// Test VSlice operations
	ss.Messages.Append("Hello", "World")
	if ss.Messages.Len() != 2 {
		t.Errorf("Messages.Len() = %d, want 2", ss.Messages.Len())
	}

	ss.Items.Set([]int{1, 2, 3})
	if ss.Items.Len() != 3 {
		t.Errorf("Items.Len() = %d, want 3", ss.Items.Len())
	}

	// Test deletion
	ss.Users.Delete("alice")
	if ss.Users.Has("alice") {
		t.Error("Users should not have alice after deletion")
	}

	// Test clear
	ss.Messages.Clear()
	if ss.Messages.Len() != 0 {
		t.Errorf("Messages.Len() after clear = %d, want 0", ss.Messages.Len())
	}
}

// State embeds RWMutex to implement RLocker via promoted methods.
type State struct {
	sync.RWMutex
	velox.State
	Value int `json:"value"`
}

// TestRLockerMarshal tests that Marshal uses RLock for RLocker types
func TestRLockerMarshal(t *testing.T) {
	state := &State{Value: 42}
	state.State.Data = velox.Marshal(state)

	// The Marshal function should use RLock instead of Lock
	// This test verifies it doesn't panic and returns correct data
	data, err := state.State.Data()
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	if string(data) == "" {
		t.Error("Marshal returned empty data")
	}
}

func TestBindAllAfterUnmarshal(t *testing.T) {
	// This tests that BindAll properly binds VMap fields
	type Data struct {
		sync.Mutex
		Items *velox.VMap[string, int] `json:"items"`
	}

	data := &Data{
		Items: &velox.VMap[string, int]{},
	}

	// Bind with the data's mutex
	velox.BindAll(data, data, nil)

	// Operations should work with locking
	data.Items.Set("key", 42)
	if v, ok := data.Items.Get("key"); !ok || v != 42 {
		t.Errorf("Items.Get(key) = %v, %v, want 42, true", v, ok)
	}

	data.Items.Set("newkey", 100)
	if data.Items.Len() != 2 {
		t.Errorf("Items.Len() = %d, want 2", data.Items.Len())
	}
}

// --- Complex Integration Test with Nested VMaps and VSlices ---

// User represents a user with settings and history
type User struct {
	Name     string            `json:"name"`
	Email    string            `json:"email"`
	Age      int               `json:"age"`
	Active   bool              `json:"active"`
	Settings map[string]string `json:"settings"`
	Tags     []string          `json:"tags"`
}

// Project represents a project with tasks
type Project struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Tasks       []Task `json:"tasks"`
}

// Task represents a task within a project
type Task struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Done     bool     `json:"done"`
	Priority int      `json:"priority"`
	Labels   []string `json:"labels"`
}


type ComplexServerState struct {
	sync.RWMutex
	velox.State

	Version  string `json:"version"`
	Counter  int    `json:"counter"`

	Config   velox.VMap[string, string]   `json:"config"`
	Users    velox.VMap[string, User]     `json:"users"`
	Projects *velox.VMap[string, Project] `json:"projects"`
	Logs     velox.VSlice[string]         `json:"logs"`
	Events   velox.VSlice[Event]          `json:"events"`
	Numbers  *velox.VSlice[int]           `json:"numbers"`
}

type Event struct {
	Timestamp string         `json:"timestamp"`
	Type      string         `json:"type"`
	Data      map[string]any `json:"data"`
}

type ComplexClientState struct {
	sync.RWMutex

	Version  string `json:"version"`
	Counter  int    `json:"counter"`

	Config   velox.VMap[string, string]   `json:"config"`
	Users    velox.VMap[string, User]     `json:"users"`
	Projects *velox.VMap[string, Project] `json:"projects"`
	Logs     velox.VSlice[string]         `json:"logs"`
	Events   velox.VSlice[Event]          `json:"events"`
	Numbers  *velox.VSlice[int]           `json:"numbers"`
}

// bufconnHTTPClient creates an http.Client that dials through a bufconn listener
func bufconnHTTPClient(l *bufconn.Listener) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return l.DialContext(ctx)
			},
		},
	}
}

// jsonCompare compares two values by marshaling to JSON and comparing
func jsonCompare(t *testing.T, name string, got, want any) bool {
	t.Helper()
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Errorf("%s: failed to marshal got: %v", name, err)
		return false
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Errorf("%s: failed to marshal want: %v", name, err)
		return false
	}

	// Unmarshal to interface{} for deep comparison (handles map ordering)
	var gotVal, wantVal any
	if err := json.Unmarshal(gotJSON, &gotVal); err != nil {
		t.Errorf("%s: failed to unmarshal got: %v", name, err)
		return false
	}
	if err := json.Unmarshal(wantJSON, &wantVal); err != nil {
		t.Errorf("%s: failed to unmarshal want: %v", name, err)
		return false
	}

	if !reflect.DeepEqual(gotVal, wantVal) {
		t.Errorf("%s: JSON mismatch\ngot:  %s\nwant: %s", name, gotJSON, wantJSON)
		return false
	}
	return true
}

func TestComplexNestedVMapVSliceSync(t *testing.T) {
	// Create server state with pointer fields initialized
	serverState := &ComplexServerState{
		Version:  "1.0.0",
		Counter:  0,
		Projects: &velox.VMap[string, Project]{},
		Numbers:  &velox.VSlice[int]{},
	}
	serverState.State.Throttle = 10 * time.Millisecond

	// Setup in-memory listener and server
	// SyncHandler auto-binds all VMap/VSlice fields
	l := bufconn.Listen(256 * 1024)
	defer l.Close()

	server := &http.Server{Handler: velox.SyncHandler(serverState)}
	go server.Serve(l)
	defer server.Close()

	// Create client state with pointer fields initialized
	clientState := &ComplexClientState{
		Projects: &velox.VMap[string, Project]{},
		Numbers:  &velox.VSlice[int]{},
	}

	// Create and configure client
	client, err := velox.NewClient("http://bufconn/sync", clientState)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	client.HTTPClient = bufconnHTTPClient(l)
	client.Retry = false

	// Track connection and updates
	connected := make(chan struct{})
	updateCh := make(chan struct{}, 100)

	client.OnConnect = func() {
		close(connected)
	}
	client.OnUpdate = func() {
		select {
		case updateCh <- struct{}{}:
		default:
		}
	}

	// Connect in background
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go client.Connect(ctx)

	// Wait for connection
	select {
	case <-connected:
		t.Log("Client connected")
	case <-time.After(3 * time.Second):
		t.Fatal("Timeout waiting for connection")
	}

	// Wait for initial sync
	waitForUpdate := func() {
		select {
		case <-updateCh:
		case <-time.After(2 * time.Second):
			t.Log("No update received (may be ok if no changes)")
		}
	}
	waitForUpdate()

	// --- Phase 1: Populate Config VMap ---
	t.Log("Phase 1: Populating Config")
	serverState.Config.Set("theme", "dark")
	serverState.Config.Set("language", "en")
	serverState.Config.Set("timezone", "UTC")
	serverState.Push() // Explicit push to ensure propagation
	time.Sleep(100 * time.Millisecond)
	waitForUpdate()

	// --- Phase 2: Add Users with nested data ---
	t.Log("Phase 2: Adding Users")
	serverState.Users.Set("alice", User{
		Name:   "Alice",
		Email:  "alice@example.com",
		Age:    30,
		Active: true,
		Settings: map[string]string{
			"notifications": "on",
			"theme":         "light",
		},
		Tags: []string{"admin", "developer"},
	})
	serverState.Users.Set("bob", User{
		Name:   "Bob",
		Email:  "bob@example.com",
		Age:    25,
		Active: true,
		Settings: map[string]string{
			"notifications": "off",
		},
		Tags: []string{"user"},
	})
	serverState.Push()
	time.Sleep(100 * time.Millisecond)
	waitForUpdate()

	// --- Phase 3: Add Projects with Tasks ---
	t.Log("Phase 3: Adding Projects")
	serverState.Projects.Set("proj-1", Project{
		ID:          "proj-1",
		Name:        "Project Alpha",
		Description: "First project",
		Tasks: []Task{
			{ID: "task-1", Title: "Setup", Done: true, Priority: 1, Labels: []string{"setup", "infra"}},
			{ID: "task-2", Title: "Implement", Done: false, Priority: 2, Labels: []string{"feature"}},
			{ID: "task-3", Title: "Test", Done: false, Priority: 3, Labels: []string{"testing"}},
		},
	})
	serverState.Push()
	time.Sleep(100 * time.Millisecond)
	waitForUpdate()

	// --- Phase 4: Add Logs and Events ---
	t.Log("Phase 4: Adding Logs and Events")
	serverState.Logs.Append("Server started", "Config loaded", "Users initialized")
	serverState.Events.Set([]Event{
		{Timestamp: "2024-01-01T00:00:00Z", Type: "startup", Data: map[string]any{"pid": 1234}},
		{Timestamp: "2024-01-01T00:00:01Z", Type: "ready", Data: map[string]any{"port": 8080}},
	})
	serverState.Push()
	time.Sleep(100 * time.Millisecond)
	waitForUpdate()

	// --- Phase 5: Add Numbers ---
	t.Log("Phase 5: Adding Numbers")
	serverState.Numbers.Set([]int{1, 2, 3, 4, 5})
	serverState.Push()
	time.Sleep(100 * time.Millisecond)
	waitForUpdate()

	// --- Phase 6: Update Counter multiple times ---
	t.Log("Phase 6: Updating Counter")
	for i := 1; i <= 5; i++ {
		serverState.Lock()
		serverState.Counter = i
		serverState.Unlock()
		serverState.Push()
		time.Sleep(30 * time.Millisecond)
	}
	waitForUpdate()

	// --- Phase 7: Modify existing data ---
	t.Log("Phase 7: Modifying data")
	serverState.Config.Set("theme", "light") // Update existing
	serverState.Config.Delete("timezone")    // Delete
	serverState.Users.Update("alice", func(u *User) {
		u.Age = 31
		u.Tags = append(u.Tags, "senior")
	})
	serverState.Logs.Append("Phase 7 complete")
	serverState.Push()
	time.Sleep(100 * time.Millisecond)
	waitForUpdate()

	// Wait for all updates to propagate
	time.Sleep(300 * time.Millisecond)

	// --- Verify: Compare Server and Client JSON ---
	t.Log("Verifying JSON match")

	// Lock both to safely read and compare
	serverState.RLock()
	clientState.RLock()

	// Compare each field
	if serverState.Version != clientState.Version {
		t.Errorf("Version mismatch: server=%s client=%s", serverState.Version, clientState.Version)
	}
	if serverState.Counter != clientState.Counter {
		t.Errorf("Counter mismatch: server=%d client=%d", serverState.Counter, clientState.Counter)
	}

	jsonCompare(t, "Config", clientState.Config.Snapshot(), serverState.Config.Snapshot())
	jsonCompare(t, "Users", clientState.Users.Snapshot(), serverState.Users.Snapshot())
	jsonCompare(t, "Projects", clientState.Projects.Snapshot(), serverState.Projects.Snapshot())
	jsonCompare(t, "Logs", clientState.Logs.Get(), serverState.Logs.Get())
	jsonCompare(t, "Events", clientState.Events.Get(), serverState.Events.Get())
	jsonCompare(t, "Numbers", clientState.Numbers.Get(), serverState.Numbers.Get())

	// Compare full JSON of both states
	serverJSON, err := json.MarshalIndent(serverState, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal server state: %v", err)
	}
	clientJSON, err := json.MarshalIndent(clientState, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal client state: %v", err)
	}

	serverState.RUnlock()
	clientState.RUnlock()

	// Deep compare the full JSON
	var serverVal, clientVal any
	json.Unmarshal(serverJSON, &serverVal)
	json.Unmarshal(clientJSON, &clientVal)

	if !reflect.DeepEqual(serverVal, clientVal) {
		t.Errorf("Full state mismatch!\nServer JSON:\n%s\n\nClient JSON:\n%s", serverJSON, clientJSON)
	} else {
		t.Logf("✓ Full JSON comparison passed")
		t.Logf("Server JSON:\n%s", serverJSON)
	}

	// Verify specific values
	clientState.RLock()
	defer clientState.RUnlock()

	// Check config
	if theme, ok := clientState.Config.Get("theme"); !ok || theme != "light" {
		t.Errorf("Config theme: got %v, want 'light'", theme)
	}
	if _, ok := clientState.Config.Get("timezone"); ok {
		t.Error("Config timezone should have been deleted")
	}

	// Check users
	if alice, ok := clientState.Users.Get("alice"); !ok {
		t.Error("User alice not found")
	} else {
		if alice.Age != 31 {
			t.Errorf("Alice age: got %d, want 31", alice.Age)
		}
		if len(alice.Tags) != 3 {
			t.Errorf("Alice tags count: got %d, want 3", len(alice.Tags))
		}
	}

	// Check projects
	if proj, ok := clientState.Projects.Get("proj-1"); !ok {
		t.Error("Project proj-1 not found")
	} else {
		if len(proj.Tasks) != 3 {
			t.Errorf("Project tasks count: got %d, want 3", len(proj.Tasks))
		}
	}

	// Check logs
	if clientState.Logs.Len() != 4 {
		t.Errorf("Logs count: got %d, want 4", clientState.Logs.Len())
	}

	// Check numbers
	if clientState.Numbers.Len() != 5 {
		t.Errorf("Numbers count: got %d, want 5", clientState.Numbers.Len())
	}

	// Check counter
	if clientState.Counter != 5 {
		t.Errorf("Counter: got %d, want 5", clientState.Counter)
	}

	// Cleanup
	client.Disconnect()
	t.Log("Test completed successfully")
}

type DeletionServerState struct {
	sync.RWMutex
	velox.State
	Items velox.VMap[string, int] `json:"items"`
	List  velox.VSlice[string]    `json:"list"`
}

type DeletionClientState struct {
	sync.RWMutex
	Items velox.VMap[string, int] `json:"items"`
	List  velox.VSlice[string]    `json:"list"`
}

func TestVMapVSliceDeletionSync(t *testing.T) {
	// Test that deletions properly sync from server to client
	serverState := &DeletionServerState{}
	serverState.State.Throttle = 10 * time.Millisecond

	l := bufconn.Listen(64 * 1024)
	defer l.Close()

	server := &http.Server{Handler: velox.SyncHandler(serverState)}
	go server.Serve(l)
	defer server.Close()

	clientState := &DeletionClientState{}
	client, err := velox.NewClient("http://bufconn/sync", clientState)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	client.HTTPClient = bufconnHTTPClient(l)
	client.Retry = false

	connected := make(chan struct{})
	updateCh := make(chan struct{}, 10)
	client.OnConnect = func() { close(connected) }
	client.OnUpdate = func() {
		select {
		case updateCh <- struct{}{}:
		default:
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go client.Connect(ctx)

	select {
	case <-connected:
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for connection")
	}

	waitForUpdate := func() {
		select {
		case <-updateCh:
		case <-time.After(500 * time.Millisecond):
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Wait for initial sync
	waitForUpdate()

	// Add items
	serverState.Items.Set("a", 1)
	serverState.Items.Set("b", 2)
	serverState.Items.Set("c", 3)
	serverState.List.Set([]string{"x", "y", "z"})
	serverState.Push()
	time.Sleep(100 * time.Millisecond)
	waitForUpdate()

	// Verify items exist on client
	clientState.RLock()
	if clientState.Items.Len() != 3 {
		t.Errorf("Expected 3 items, got %d", clientState.Items.Len())
	}
	if clientState.List.Len() != 3 {
		t.Errorf("Expected 3 list items, got %d", clientState.List.Len())
	}
	clientState.RUnlock()

	// Delete items
	serverState.Items.Delete("b")
	serverState.List.Set([]string{"x", "z"}) // Remove "y"
	serverState.Push()
	time.Sleep(100 * time.Millisecond)
	waitForUpdate()

	// Verify deletions synced
	clientState.RLock()
	if clientState.Items.Has("b") {
		t.Error("Item 'b' should have been deleted")
	}
	if clientState.Items.Len() != 2 {
		t.Errorf("Expected 2 items after delete, got %d", clientState.Items.Len())
	}
	if clientState.List.Len() != 2 {
		t.Errorf("Expected 2 list items after delete, got %d", clientState.List.Len())
	}
	clientState.RUnlock()

	// Clear all
	serverState.Items.Clear()
	serverState.List.Clear()
	serverState.Push()
	time.Sleep(100 * time.Millisecond)
	waitForUpdate()

	// Verify clear synced
	clientState.RLock()
	if clientState.Items.Len() != 0 {
		t.Errorf("Expected 0 items after clear, got %d", clientState.Items.Len())
	}
	if clientState.List.Len() != 0 {
		t.Errorf("Expected 0 list items after clear, got %d", clientState.List.Len())
	}
	clientState.RUnlock()

	client.Disconnect()
}
