package velox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"github.com/jpillora/eventsource"
)

// Client connects to a Velox server and keeps a local object in sync.
type Client struct {
	// URL is the velox sync endpoint URL
	URL string
	// Transport is the HTTP transport to use (optional, useful for testing with bufconn)
	Transport http.RoundTripper
	// HTTPClient is the HTTP client to use for SSE connections (optional, overrides Transport)
	HTTPClient *http.Client

	// Callbacks - OnUpdate receives the full merged state after each update
	OnUpdate     func(data json.RawMessage)
	OnConnect    func()
	OnDisconnect func()
	OnError      func(err error)

	// Retry enables automatic reconnection with backoff (default: true)
	Retry bool
	// MinRetryDelay is the minimum retry delay (default: 100ms)
	MinRetryDelay time.Duration
	// MaxRetryDelay is the maximum retry delay (default: 10s)
	MaxRetryDelay time.Duration

	// state
	mu        sync.Mutex
	id        string          // server-assigned state ID
	version   int64           // current version
	state     json.RawMessage // current full state
	connected bool
	body      io.ReadCloser
	dec       *eventsource.Decoder
	cancel    context.CancelFunc
	done      chan struct{}
}

// NewClient creates a new Velox client for the given URL.
func NewClient(url string) *Client {
	return &Client{
		URL:           url,
		Retry:         true,
		MinRetryDelay: 100 * time.Millisecond,
		MaxRetryDelay: 10 * time.Second,
	}
}

// ID returns the server-assigned state ID.
func (c *Client) ID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.id
}

// Version returns the current version.
func (c *Client) Version() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.version
}

// Connected returns true if the client is currently connected.
func (c *Client) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected
}

// State returns the current synced state as raw JSON.
func (c *Client) State() json.RawMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// Connect starts the client connection. It blocks until the context is
// cancelled or an unrecoverable error occurs. If Retry is true (default),
// it will automatically reconnect on connection failures.
func (c *Client) Connect(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.cancel = cancel
	c.done = make(chan struct{})
	c.mu.Unlock()

	defer func() {
		close(c.done)
	}()

	retryDelay := c.MinRetryDelay
	if retryDelay == 0 {
		retryDelay = 100 * time.Millisecond
	}
	maxDelay := c.MaxRetryDelay
	if maxDelay == 0 {
		maxDelay = 10 * time.Second
	}

	for {
		err := c.connectOnce(ctx)
		if err == nil {
			return nil // clean shutdown
		}

		// Check if context was cancelled
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Call error callback
		if c.OnError != nil {
			c.OnError(err)
		}

		// Don't retry if disabled
		if !c.Retry {
			return err
		}

		// Wait before retrying
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(retryDelay):
		}

		// Exponential backoff
		retryDelay *= 2
		if retryDelay > maxDelay {
			retryDelay = maxDelay
		}
	}
}

// Disconnect stops the client connection.
func (c *Client) Disconnect() {
	c.mu.Lock()
	cancel := c.cancel
	done := c.done
	c.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

// connectOnce attempts a single connection to the server.
func (c *Client) connectOnce(ctx context.Context) error {
	// Build URL with query params
	u, err := url.Parse(c.URL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	c.mu.Lock()
	if c.version > 0 {
		q := u.Query()
		q.Set("v", strconv.FormatInt(c.version, 10))
		if c.id != "" {
			q.Set("id", c.id)
		}
		u.RawQuery = q.Encode()
	}
	c.mu.Unlock()

	// Create request
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")

	// Make request
	httpClient := c.HTTPClient
	if httpClient == nil {
		if c.Transport != nil {
			httpClient = &http.Client{Transport: c.Transport}
		} else {
			httpClient = http.DefaultClient
		}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP request failed: %w", err)
	}

	// Check response
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		resp.Body.Close()
		return fmt.Errorf("unexpected content-type: %s", ct)
	}

	c.mu.Lock()
	c.body = resp.Body
	c.dec = eventsource.NewDecoder(resp.Body)
	c.connected = true
	c.mu.Unlock()

	// Notify connect
	if c.OnConnect != nil {
		c.OnConnect()
	}

	// Read events
	err = c.readEvents(ctx)

	// Cleanup
	c.mu.Lock()
	c.connected = false
	if c.body != nil {
		c.body.Close()
		c.body = nil
	}
	c.dec = nil
	c.mu.Unlock()

	// Notify disconnect
	if c.OnDisconnect != nil {
		c.OnDisconnect()
	}

	return err
}

// readEvents reads and processes events from the SSE stream.
func (c *Client) readEvents(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		c.mu.Lock()
		dec := c.dec
		c.mu.Unlock()

		if dec == nil {
			return nil
		}

		e := &eventsource.Event{}
		if err := dec.Decode(e); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("failed to decode event: %w", err)
		}

		update := &Update{}
		if err := json.Unmarshal([]byte(e.Data), update); err != nil {
			return fmt.Errorf("failed to unmarshal update: %w", err)
		}

		// Handle ping
		if update.Ping {
			continue
		}

		// Update state ID if provided
		c.mu.Lock()
		if update.ID != "" {
			c.id = update.ID
		}
		if update.Version > 0 {
			c.version = update.Version
		}

		// Apply update to state
		if len(update.Body) > 0 {
			if update.Delta && len(c.state) > 0 {
				// Apply delta patch using JSON merge patch
				merged, err := jsonpatch.MergePatch(c.state, update.Body)
				if err != nil {
					c.mu.Unlock()
					if c.OnError != nil {
						c.OnError(fmt.Errorf("failed to apply patch: %w", err))
					}
					continue
				}
				c.state = merged
			} else {
				// Full state replacement
				c.state = update.Body
			}
		}
		currentState := c.state
		c.mu.Unlock()

		// Notify update with full merged state
		if c.OnUpdate != nil && len(currentState) > 0 {
			c.OnUpdate(currentState)
		}
	}
}

// SyncClient is a higher-level client that automatically syncs to a typed struct.
type SyncClient[T any] struct {
	*Client
	// UserOnUpdate is called after internal state is updated (optional)
	UserOnUpdate func(data T)
	data         T
	dataMu       sync.RWMutex
}

// NewSyncClient creates a new typed sync client.
func NewSyncClient[T any](url string) *SyncClient[T] {
	sc := &SyncClient[T]{
		Client: NewClient(url),
	}
	sc.Client.OnUpdate = sc.handleUpdate
	return sc
}

// Data returns a copy of the current synced data.
func (sc *SyncClient[T]) Data() T {
	sc.dataMu.RLock()
	defer sc.dataMu.RUnlock()
	return sc.data
}

// handleUpdate processes incoming updates.
func (sc *SyncClient[T]) handleUpdate(body json.RawMessage) {
	sc.dataMu.Lock()
	var newData T
	if err := json.Unmarshal(body, &newData); err != nil {
		sc.dataMu.Unlock()
		if sc.Client.OnError != nil {
			sc.Client.OnError(fmt.Errorf("failed to unmarshal data: %w", err))
		}
		return
	}
	sc.data = newData
	sc.dataMu.Unlock()

	// Call user callback if set
	if sc.UserOnUpdate != nil {
		sc.UserOnUpdate(newData)
	}
}
