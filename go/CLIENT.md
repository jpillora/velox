# Velox Go Client

A Go client for [Velox](https://github.com/jpillora/velox) real-time object synchronization.

## Summary

This adds a native Go client to consume Velox sync endpoints. The client connects via Server-Sent Events (SSE) and automatically handles:

- Delta patches (JSON merge patch) for efficient updates
- Full state replacement when needed
- Automatic reconnection with exponential backoff
- Version tracking for resumable connections
- Thread-safe updates with optional `sync.Locker` support

## Interface

```go
// Client connects to a Velox server and keeps a local struct in sync.
type Client[T any] struct {
    // Configuration
    URL        string
    HTTPClient *http.Client  // Optional, for custom transports (e.g., testing)

    // Retry settings
    Retry         bool          // Enable auto-reconnect (default: true)
    MinRetryDelay time.Duration // Default: 100ms
    MaxRetryDelay time.Duration // Default: 10s

    // Callbacks
    OnUpdate     func()            // Called after data updated (outside lock)
    OnConnect    func()
    OnDisconnect func()
    OnError      func(err error)
}

// Constructor - data must be a pointer to a struct
// If the struct implements sync.Locker, it will be locked during updates
func NewClient[T any](url string, data *T) (*Client[T], error)

// Methods
func (c *Client[T]) Connect(ctx context.Context) error  // Blocking
func (c *Client[T]) Disconnect()
func (c *Client[T]) ID() string                         // Server-assigned state ID
func (c *Client[T]) Version() int64                     // Current version
func (c *Client[T]) Connected() bool
```

## Usage

### Basic Example

```go
// Define your data struct with embedded mutex for thread-safe access
type GameState struct {
    sync.Mutex                    // Enables automatic locking during updates
    Players []string `json:"players"`
    Score   int      `json:"score"`
}

func main() {
    // Create the data struct
    state := &GameState{}

    // Create client - automatically detects sync.Locker
    client, err := velox.NewClient("http://localhost:3000/sync", state)
    if err != nil {
        log.Fatal(err)
    }

    client.OnConnect = func() {
        log.Println("Connected!")
    }

    client.OnUpdate = func() {
        // Lock when reading (client locks during write)
        state.Lock()
        log.Printf("Score: %d, Players: %v", state.Score, state.Players)
        state.Unlock()
    }

    // Connect (blocking)
    ctx := context.Background()
    if err := client.Connect(ctx); err != nil {
        log.Fatal(err)
    }
}
```

### Background Connection

```go
state := &GameState{}
client, _ := velox.NewClient("http://localhost:3000/sync", state)

ctx, cancel := context.WithCancel(context.Background())

// Connect in background
go client.Connect(ctx)

// ... do other work ...

// Read state safely
state.Lock()
score := state.Score
state.Unlock()

// Disconnect when done
client.Disconnect()
// or: cancel()
```

### Testing with bufconn (in-memory)

```go
import "google.golang.org/grpc/test/bufconn"

// Create in-memory listener
l := bufconn.Listen(64 * 1024)
server := &http.Server{Handler: velox.SyncHandler(serverData)}
go server.Serve(l)

// Create client with custom HTTPClient
state := &MyData{}
client, _ := velox.NewClient("http://test/sync", state)
client.HTTPClient = &http.Client{
    Transport: &http.Transport{
        DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
            return l.DialContext(ctx)
        },
    },
}
```

## Thread Safety

The client automatically locks your data struct during updates if it implements `sync.Locker`. The recommended pattern is to embed `sync.Mutex`:

```go
type MyData struct {
    sync.Mutex              // Embed mutex
    Name  string `json:"name"`
    Value int    `json:"value"`
}

data := &MyData{}
client, _ := velox.NewClient(url, data)

// Client automatically calls data.Lock()/Unlock() during updates
// You must also lock when reading:
data.Lock()
fmt.Println(data.Name, data.Value)
data.Unlock()
```
