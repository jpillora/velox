package velox_test

import (
	"context"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	velox "github.com/jpillora/velox/go"
	"google.golang.org/grpc/test/bufconn"
)

// bufconnClient creates an http.Client that dials through a bufconn listener
func bufconnClient(l *bufconn.Listener) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return l.DialContext(ctx)
			},
		},
	}
}

// ServerData is the struct used by the server
type ServerData struct {
	velox.State
	sync.Mutex
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// ClientData is the struct used by the client (embeds sync.Mutex for locking)
type ClientData struct {
	sync.Mutex
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func TestClient(t *testing.T) {
	serverData := &ServerData{Name: "initial", Count: 0}
	serverData.State.Throttle = 10 * time.Millisecond

	// Setup in-memory listener and server
	l := bufconn.Listen(64 * 1024)
	defer l.Close()

	server := &http.Server{Handler: velox.SyncHandler(serverData)}
	go server.Serve(l)
	defer server.Close()

	// Create client data struct with embedded mutex
	clientData := &ClientData{}

	// Create client
	client, err := velox.NewClient("http://bufconn/sync", clientData)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	client.HTTPClient = bufconnClient(l)
	client.Retry = false

	// Track updates
	updateCount := atomic.Int32{}
	connected := make(chan struct{})
	disconnected := make(chan struct{})

	client.OnUpdate = func() {
		updateCount.Add(1)
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
		serverData.Lock()
		serverData.Name = "updated"
		serverData.Count = i
		serverData.Unlock()
		serverData.Push()
		time.Sleep(50 * time.Millisecond)
	}

	// Wait for updates to arrive
	time.Sleep(200 * time.Millisecond)

	// Verify we received updates
	if updateCount.Load() < 1 {
		t.Fatalf("Expected at least 1 update, got %d", updateCount.Load())
	}
	t.Logf("Received %d updates", updateCount.Load())

	// Check the data struct was updated (lock to read safely)
	clientData.Lock()
	name := clientData.Name
	count := clientData.Count
	clientData.Unlock()

	t.Logf("Client data: name=%s, count=%d", name, count)

	if name != "updated" {
		t.Errorf("Expected name 'updated', got '%s'", name)
	}
	if count != 3 {
		t.Errorf("Expected count 3, got %d", count)
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

func TestClientWithoutMutex(t *testing.T) {
	// Test that client works with structs that don't embed sync.Mutex
	// Note: Without a mutex, you must synchronize access yourself
	type SimpleData struct {
		sync.Mutex // Add mutex to avoid race in test
		Value      int `json:"value"`
	}

	serverData := &struct {
		velox.State
		Value int `json:"value"`
	}{Value: 42}
	serverData.State.Throttle = 10 * time.Millisecond

	l := bufconn.Listen(64 * 1024)
	defer l.Close()

	server := &http.Server{Handler: velox.SyncHandler(serverData)}
	go server.Serve(l)
	defer server.Close()

	clientData := &SimpleData{}
	client, err := velox.NewClient("http://bufconn/sync", clientData)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	client.HTTPClient = bufconnClient(l)
	client.Retry = false

	updated := make(chan struct{}, 1)
	client.OnUpdate = func() {
		select {
		case updated <- struct{}{}:
		default:
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go client.Connect(ctx)

	// Wait for update
	select {
	case <-updated:
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for update")
	}

	// Safe to read now - client locks during update
	clientData.Lock()
	val := clientData.Value
	clientData.Unlock()

	if val != 42 {
		t.Errorf("Expected value 42, got %d", val)
	}

	client.Disconnect()
}

func TestClientReconnect(t *testing.T) {
	type ServerData struct {
		velox.State
		sync.Mutex
		Counter int `json:"counter"`
	}

	type ClientData struct {
		sync.Mutex
		Counter int `json:"counter"`
	}

	serverData := &ServerData{Counter: 0}
	serverData.State.Throttle = 10 * time.Millisecond

	// Setup in-memory listener and server
	l := bufconn.Listen(64 * 1024)

	server := &http.Server{Handler: velox.SyncHandler(serverData)}
	go server.Serve(l)

	clientData := &ClientData{}
	client, err := velox.NewClient("http://bufconn/sync", clientData)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	client.HTTPClient = bufconnClient(l)
	client.Retry = true
	client.MinRetryDelay = 50 * time.Millisecond
	client.MaxRetryDelay = 200 * time.Millisecond

	connectCount := atomic.Int32{}
	disconnectCount := atomic.Int32{}

	client.OnConnect = func() {
		connectCount.Add(1)
	}
	client.OnDisconnect = func() {
		disconnectCount.Add(1)
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
	serverData.Lock()
	serverData.Counter = 1
	serverData.Unlock()
	serverData.Push()
	time.Sleep(100 * time.Millisecond)

	// Verify we got the update
	clientData.Lock()
	counter := clientData.Counter
	clientData.Unlock()
	if counter != 1 {
		t.Errorf("Expected counter 1, got %d", counter)
	}

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
	type ServerData struct {
		velox.State
		sync.Mutex
		Value int `json:"value"`
	}

	type ClientData struct {
		sync.Mutex
		Value int `json:"value"`
	}

	serverData := &ServerData{Value: 0}
	serverData.State.Throttle = 5 * time.Millisecond

	// Setup in-memory listener and server
	l := bufconn.Listen(256 * 1024) // larger buffer for multiple clients
	defer l.Close()

	server := &http.Server{Handler: velox.SyncHandler(serverData)}
	go server.Serve(l)
	defer server.Close()

	httpClient := bufconnClient(l)

	const numClients = 5
	clients := make([]*velox.Client[ClientData], numClients)
	clientData := make([]*ClientData, numClients)
	connected := make([]chan struct{}, numClients)

	for i := 0; i < numClients; i++ {
		clientData[i] = &ClientData{}
		var err error
		clients[i], err = velox.NewClient("http://bufconn/sync", clientData[i])
		if err != nil {
			t.Fatalf("Failed to create client %d: %v", i, err)
		}
		clients[i].HTTPClient = httpClient
		clients[i].Retry = false
		connected[i] = make(chan struct{})

		idx := i
		clients[i].OnConnect = func() {
			close(connected[idx])
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Connect all clients
	for i := 0; i < numClients; i++ {
		go func(c *velox.Client[ClientData]) {
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
		serverData.Lock()
		serverData.Value = i
		serverData.Unlock()
		serverData.Push()
		time.Sleep(30 * time.Millisecond)
	}

	// Wait for updates to propagate
	time.Sleep(200 * time.Millisecond)

	// Verify all clients got the final value
	for i := 0; i < numClients; i++ {
		clientData[i].Lock()
		val := clientData[i].Value
		clientData[i].Unlock()
		if val != numUpdates {
			t.Errorf("Client %d: expected value %d, got %d", i, numUpdates, val)
		}
	}

	// Disconnect all
	for i := 0; i < numClients; i++ {
		clients[i].Disconnect()
	}
}

func TestNewClientValidation(t *testing.T) {
	// Test nil data
	_, err := velox.NewClient[struct{}]("http://test", nil)
	if err == nil {
		t.Error("Expected error for nil data")
	}

	// Test non-struct type - this won't compile due to generics,
	// but we can test with a valid struct
	data := &struct{ Value int }{}
	client, err := velox.NewClient("http://test", data)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if client == nil {
		t.Error("Expected non-nil client")
	}
}
