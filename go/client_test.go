package velox_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	velox "github.com/jpillora/velox/go"
	"google.golang.org/grpc/test/bufconn"
)

// bufconnTransport creates an http.RoundTripper that dials through a bufconn listener
func bufconnTransport(l *bufconn.Listener) http.RoundTripper {
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return l.DialContext(ctx)
		},
	}
}

// ClientTestData is the struct used by the server for TestClient
type ClientTestData struct {
	velox.State
	sync.Mutex
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// ClientData is a simple struct for client-side unmarshalling
type ClientData struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func TestClient(t *testing.T) {
	data := &ClientTestData{Name: "initial", Count: 0}
	data.State.Throttle = 10 * time.Millisecond

	// Setup in-memory listener and server
	l := bufconn.Listen(64 * 1024)
	defer l.Close()

	server := &http.Server{Handler: velox.SyncHandler(data)}
	go server.Serve(l)
	defer server.Close()

	// Create client with bufconn transport
	client := velox.NewClient("http://bufconn/sync")
	client.Transport = bufconnTransport(l)
	client.Retry = false // disable retry for testing

	// Track updates
	var updates []json.RawMessage
	var updatesMu sync.Mutex
	connected := make(chan struct{})
	disconnected := make(chan struct{})

	client.OnUpdate = func(body json.RawMessage) {
		updatesMu.Lock()
		updates = append(updates, body)
		updatesMu.Unlock()
	}
	client.OnConnect = func() {
		close(connected)
	}
	client.OnDisconnect = func() {
		close(disconnected)
	}

	// Connect in background
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		if err := client.Connect(ctx); err != nil && ctx.Err() == nil {
			t.Logf("Connect error: %v", err)
		}
	}()

	// Wait for connection
	select {
	case <-connected:
		t.Log("Client connected")
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for connection")
	}

	// Wait for initial data
	time.Sleep(100 * time.Millisecond)

	// Push some updates
	for i := 1; i <= 3; i++ {
		data.Lock()
		data.Name = "updated"
		data.Count = i
		data.Unlock()
		data.Push()
		time.Sleep(50 * time.Millisecond)
	}

	// Wait for updates to arrive
	time.Sleep(200 * time.Millisecond)

	// Verify we received updates
	updatesMu.Lock()
	numUpdates := len(updates)
	updatesMu.Unlock()

	if numUpdates < 1 {
		t.Fatalf("Expected at least 1 update, got %d", numUpdates)
	}
	t.Logf("Received %d updates", numUpdates)

	// Check the last update has correct data
	updatesMu.Lock()
	lastUpdate := updates[len(updates)-1]
	updatesMu.Unlock()

	t.Logf("Last update JSON: %s", string(lastUpdate))

	var lastData ClientData
	if err := json.Unmarshal(lastUpdate, &lastData); err != nil {
		t.Fatalf("Failed to unmarshal last update: %v", err)
	}

	if lastData.Name != "updated" {
		t.Errorf("Expected name 'updated', got '%s'", lastData.Name)
	}
	if lastData.Count != 3 {
		t.Errorf("Expected count 3, got %d", lastData.Count)
	}

	// Verify version is tracked
	if client.Version() == 0 {
		t.Error("Expected version to be > 0")
	}
	t.Logf("Final version: %d", client.Version())

	// Disconnect
	client.Disconnect()

	// Wait for disconnect
	select {
	case <-disconnected:
		t.Log("Client disconnected")
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for disconnect")
	}
}

func TestSyncClient(t *testing.T) {
	// Server data structure
	type ServerData struct {
		velox.State
		sync.Mutex
		Message string `json:"message"`
		Value   int    `json:"value"`
	}

	// Client data structure (can be subset of server)
	type ClientData struct {
		Message string `json:"message"`
		Value   int    `json:"value"`
	}

	serverData := &ServerData{Message: "hello", Value: 42}
	serverData.State.Throttle = 10 * time.Millisecond

	// Setup in-memory listener and server
	l := bufconn.Listen(64 * 1024)
	defer l.Close()

	server := &http.Server{Handler: velox.SyncHandler(serverData)}
	go server.Serve(l)
	defer server.Close()

	// Create typed sync client
	client := velox.NewSyncClient[ClientData]("http://bufconn/sync")
	client.Transport = bufconnTransport(l)
	client.Retry = false

	updateCount := atomic.Int32{}
	connected := make(chan struct{})

	client.UserOnUpdate = func(data ClientData) {
		updateCount.Add(1)
	}
	client.OnConnect = func() {
		close(connected)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		client.Connect(ctx)
	}()

	// Wait for connection
	select {
	case <-connected:
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for connection")
	}

	// Wait for initial data
	time.Sleep(100 * time.Millisecond)

	// Check initial data
	data := client.Data()
	if data.Message != "hello" {
		t.Errorf("Expected message 'hello', got '%s'", data.Message)
	}
	if data.Value != 42 {
		t.Errorf("Expected value 42, got %d", data.Value)
	}

	// Push an update
	serverData.Lock()
	serverData.Message = "world"
	serverData.Value = 100
	serverData.Unlock()
	serverData.Push()

	// Wait for update
	time.Sleep(100 * time.Millisecond)

	// Check updated data
	data = client.Data()
	if data.Message != "world" {
		t.Errorf("Expected message 'world', got '%s'", data.Message)
	}
	if data.Value != 100 {
		t.Errorf("Expected value 100, got %d", data.Value)
	}

	client.Disconnect()
}

func TestClientReconnect(t *testing.T) {
	type TestData struct {
		velox.State
		sync.Mutex
		Counter int `json:"counter"`
	}

	data := &TestData{Counter: 0}
	data.State.Throttle = 10 * time.Millisecond

	// Setup in-memory listener and server
	l := bufconn.Listen(64 * 1024)

	server := &http.Server{Handler: velox.SyncHandler(data)}
	go server.Serve(l)

	client := velox.NewClient("http://bufconn/sync")
	client.Transport = bufconnTransport(l)
	client.Retry = true
	client.MinRetryDelay = 50 * time.Millisecond
	client.MaxRetryDelay = 200 * time.Millisecond

	connectCount := atomic.Int32{}
	disconnectCount := atomic.Int32{}
	var updatesMu sync.Mutex
	var lastCounter int

	client.OnConnect = func() {
		connectCount.Add(1)
	}
	client.OnDisconnect = func() {
		disconnectCount.Add(1)
	}
	client.OnUpdate = func(body json.RawMessage) {
		var d TestData
		if err := json.Unmarshal(body, &d); err == nil {
			updatesMu.Lock()
			lastCounter = d.Counter
			updatesMu.Unlock()
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		client.Connect(ctx)
	}()

	// Wait for first connection
	time.Sleep(200 * time.Millisecond)

	if connectCount.Load() != 1 {
		t.Fatalf("Expected 1 connect, got %d", connectCount.Load())
	}

	// Push initial update
	data.Lock()
	data.Counter = 1
	data.Unlock()
	data.Push()
	time.Sleep(100 * time.Millisecond)

	// Verify we got the update
	updatesMu.Lock()
	if lastCounter != 1 {
		t.Errorf("Expected counter 1, got %d", lastCounter)
	}
	updatesMu.Unlock()

	// Close server and listener to simulate failure
	server.Close()
	l.Close()
	time.Sleep(200 * time.Millisecond)

	// Client should disconnect
	t.Logf("Disconnects: %d", disconnectCount.Load())
	if disconnectCount.Load() < 1 {
		t.Errorf("Expected at least 1 disconnect after server closed")
	}

	client.Disconnect()
}

func TestMultipleClients(t *testing.T) {
	type TestData struct {
		velox.State
		sync.Mutex
		Value int `json:"value"`
	}

	data := &TestData{Value: 0}
	data.State.Throttle = 5 * time.Millisecond

	// Setup in-memory listener and server
	l := bufconn.Listen(256 * 1024) // larger buffer for multiple clients
	defer l.Close()

	server := &http.Server{Handler: velox.SyncHandler(data)}
	go server.Serve(l)
	defer server.Close()

	transport := bufconnTransport(l)

	const numClients = 5
	clients := make([]*velox.Client, numClients)
	connected := make([]chan struct{}, numClients)
	lastValues := make([]atomic.Int32, numClients)

	for i := 0; i < numClients; i++ {
		clients[i] = velox.NewClient("http://bufconn/sync")
		clients[i].Transport = transport
		clients[i].Retry = false
		connected[i] = make(chan struct{})

		idx := i
		clients[i].OnConnect = func() {
			close(connected[idx])
		}
		clients[i].OnUpdate = func(body json.RawMessage) {
			var d TestData
			if err := json.Unmarshal(body, &d); err == nil {
				lastValues[idx].Store(int32(d.Value))
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Connect all clients
	for i := 0; i < numClients; i++ {
		go func(c *velox.Client) {
			c.Connect(ctx)
		}(clients[i])
	}

	// Wait for all to connect
	for i := 0; i < numClients; i++ {
		select {
		case <-connected[i]:
		case <-time.After(2 * time.Second):
			t.Fatalf("Client %d failed to connect", i)
		}
	}

	t.Log("All clients connected")

	// Push updates
	const numUpdates = 10
	for i := 1; i <= numUpdates; i++ {
		data.Lock()
		data.Value = i
		data.Unlock()
		data.Push()
		time.Sleep(30 * time.Millisecond)
	}

	// Wait for updates to propagate
	time.Sleep(200 * time.Millisecond)

	// Verify all clients got the final value
	for i := 0; i < numClients; i++ {
		val := lastValues[i].Load()
		if val != numUpdates {
			t.Errorf("Client %d: expected value %d, got %d", i, numUpdates, val)
		}
	}

	// Disconnect all
	for i := 0; i < numClients; i++ {
		clients[i].Disconnect()
	}
}
