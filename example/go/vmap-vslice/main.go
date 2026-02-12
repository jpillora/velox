package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	velox "github.com/jpillora/velox/go"
	"google.golang.org/grpc/test/bufconn"
)

// -- Server-side state --

type TodoItem struct {
	Title string `json:"title"`
	Done  bool   `json:"done"`
}

type AppState struct {
	sync.RWMutex
	velox.State

	// VMap: key-value settings
	Settings velox.VMap[string, string] `json:"settings"`

	// VMap: user scores
	Scores velox.VMap[string, int] `json:"scores"`

	// VSlice: ordered todo list
	Todos velox.VSlice[TodoItem] `json:"todos"`

	// VSlice: simple log messages
	Logs velox.VSlice[string] `json:"logs"`
}

// -- Client-side state (mirrors server, no velox.State needed) --

type ClientState struct {
	sync.RWMutex
	Settings velox.VMap[string, string] `json:"settings"`
	Scores   velox.VMap[string, int]    `json:"scores"`
	Todos    velox.VSlice[TodoItem]     `json:"todos"`
	Logs     velox.VSlice[string]       `json:"logs"`
}

func main() {
	// === 1. Create server state ===
	server := &AppState{}
	server.State.Throttle = 10 * time.Millisecond

	// === 2. Start in-memory HTTP server ===
	// SyncHandler auto-binds all VMap/VSlice fields to the struct's locker and pusher
	lis := bufconn.Listen(256 * 1024)
	defer lis.Close()

	httpServer := &http.Server{Handler: velox.SyncHandler(server)}
	go httpServer.Serve(lis)
	defer httpServer.Close()

	// === 3. Create client and connect ===
	client := &ClientState{}
	vc, err := velox.NewClient("http://bufconn/sync", client)
	if err != nil {
		log.Fatalf("NewClient: %v", err)
	}
	vc.HTTPClient = &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return lis.DialContext(ctx)
			},
		},
	}
	vc.Retry = false

	connected := make(chan struct{})
	updateCh := make(chan struct{}, 50)
	vc.OnConnect = func() { close(connected) }
	vc.OnUpdate = func() {
		select {
		case updateCh <- struct{}{}:
		default:
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go vc.Connect(ctx)

	// Wait for connection
	select {
	case <-connected:
		fmt.Println("Client connected to server")
	case <-time.After(3 * time.Second):
		log.Fatal("timeout waiting for connection")
	}

	waitUpdate := func() {
		select {
		case <-updateCh:
		case <-time.After(2 * time.Second):
		}
		// Small buffer for propagation
		time.Sleep(50 * time.Millisecond)
	}
	waitUpdate() // initial sync

	// === 4. VMap: Set some settings ===
	// Each Set/Delete auto-pushes (throttled)
	fmt.Println("\n--- VMap: Settings ---")
	server.Settings.Set("theme", "dark")
	server.Settings.Set("lang", "en")
	server.Settings.Set("notifications", "on")
	waitUpdate()

	client.RLock()
	fmt.Printf("  server settings: theme=%q, lang=%q, notifications=%q\n", must(server.Settings.Get("theme")), must(server.Settings.Get("lang")), must(server.Settings.Get("notifications")))
	fmt.Printf("  client settings: theme=%q, lang=%q, notifications=%q\n", must(client.Settings.Get("theme")), must(client.Settings.Get("lang")), must(client.Settings.Get("notifications")))
	client.RUnlock()

	// Update and delete
	server.Settings.Set("theme", "light")
	server.Settings.Delete("notifications")
	waitUpdate()

	client.RLock()
	_, hasNotif := client.Settings.Get("notifications")
	fmt.Printf("  after update: theme=%q, has notifications=%v\n", must(client.Settings.Get("theme")), hasNotif)
	client.RUnlock()

	// === 5. VMap: Scores with Batch ===
	fmt.Println("\n--- VMap: Scores (Batch) ---")
	server.Scores.Batch(func(data map[string]int) {
		data["alice"] = 100
		data["bob"] = 85
		data["charlie"] = 92
	})
	waitUpdate()

	client.RLock()
	fmt.Printf("  client scores: %v\n", client.Scores.Snapshot())
	client.RUnlock()

	// Update a score
	server.Scores.Update("bob", func(v *int) { *v += 15 })
	waitUpdate()

	client.RLock()
	fmt.Printf("  after bob +15: bob=%d\n", must(client.Scores.Get("bob")))
	client.RUnlock()

	// === 6. VSlice: Todo list ===
	fmt.Println("\n--- VSlice: Todos ---")
	server.Todos.Append(
		TodoItem{Title: "Buy groceries", Done: false},
		TodoItem{Title: "Write tests", Done: false},
		TodoItem{Title: "Deploy app", Done: false},
	)
	waitUpdate()

	client.RLock()
	fmt.Printf("  client todos (%d items):\n", client.Todos.Len())
	client.Todos.Range(func(i int, t TodoItem) bool {
		fmt.Printf("    [%d] %s (done=%v)\n", i, t.Title, t.Done)
		return true
	})
	client.RUnlock()

	// Mark one as done, delete another
	server.Todos.Update(0, func(t *TodoItem) { t.Done = true })
	server.Todos.DeleteAt(2)
	waitUpdate()

	client.RLock()
	fmt.Printf("  after update+delete (%d items):\n", client.Todos.Len())
	client.Todos.Range(func(i int, t TodoItem) bool {
		fmt.Printf("    [%d] %s (done=%v)\n", i, t.Title, t.Done)
		return true
	})
	client.RUnlock()

	// === 7. VSlice: Logs ===
	fmt.Println("\n--- VSlice: Logs ---")
	server.Logs.Append("server started")
	server.Logs.Append("client connected")
	server.Logs.Append("settings updated")
	waitUpdate()

	client.RLock()
	fmt.Printf("  client logs: %v\n", client.Logs.Get())
	client.RUnlock()

	// === 8. Verify full state match ===
	fmt.Println("\n--- Full State Comparison ---")
	time.Sleep(200 * time.Millisecond) // final propagation buffer

	server.RLock()
	client.RLock()
	serverJSON, _ := json.MarshalIndent(server, "", "  ")
	clientJSON, _ := json.MarshalIndent(client, "", "  ")
	server.RUnlock()
	client.RUnlock()

	var sv, cv any
	json.Unmarshal(serverJSON, &sv)
	json.Unmarshal(clientJSON, &cv)

	if jsonEqual(sv, cv) {
		fmt.Println("  PASS: server and client state match!")
	} else {
		fmt.Println("  FAIL: state mismatch!")
		fmt.Printf("  Server:\n%s\n", serverJSON)
		fmt.Printf("  Client:\n%s\n", clientJSON)
	}

	fmt.Printf("\n  Server JSON:\n%s\n", serverJSON)

	// Cleanup
	vc.Disconnect()
	fmt.Println("\nDone.")
}

func must[V any](v V, _ bool) V { return v }

func jsonEqual(a, b any) bool {
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	var av, bv any
	json.Unmarshal(aj, &av)
	json.Unmarshal(bj, &bv)
	return fmt.Sprintf("%v", av) == fmt.Sprintf("%v", bv)
}
