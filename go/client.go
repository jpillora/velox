package velox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"sync"
	"time"

	"github.com/jpillora/eventsource"
)

// Client connects to a Velox server and keeps a local struct in sync.
type Client[T any] struct {
	// URL is the velox sync endpoint URL
	URL string
	// HTTPClient is the HTTP client to use (optional, useful for testing)
	HTTPClient *http.Client

	// Callbacks
	OnUpdate     func() // Called after data is updated (outside lock)
	OnConnect    func()
	OnDisconnect func()
	OnError      func(err error)

	// Retry enables automatic reconnection with backoff (default: true)
	Retry bool
	// MinRetryDelay is the minimum retry delay (default: 100ms)
	MinRetryDelay time.Duration
	// MaxRetryDelay is the maximum retry delay (default: 10s)
	MaxRetryDelay time.Duration

	// internal state
	mu        sync.Mutex
	data      *T               // pointer to user's struct
	locker    sync.Locker      // non-nil if data implements sync.Locker
	stateMap  map[string]any   // cached unmarshaled state for fast delta merge
	id        string           // server-assigned state ID
	version   int64            // current version
	connected bool
	body      io.ReadCloser
	dec       *eventsource.Decoder
	cancel    context.CancelFunc
	done      chan struct{}
}

// NewClient creates a new Velox client that syncs to the given struct pointer.
// The data parameter must be a pointer to a struct. If the struct embeds
// sync.Mutex (or implements sync.Locker), it will be locked during updates.
func NewClient[T any](url string, data *T) (*Client[T], error) {
	if data == nil {
		return nil, fmt.Errorf("data must not be nil")
	}

	// Verify T is a struct type
	t := reflect.TypeOf(data).Elem()
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("data must be a pointer to a struct, got pointer to %s", t.Kind())
	}

	c := &Client[T]{
		URL:           url,
		data:          data,
		Retry:         true,
		MinRetryDelay: 100 * time.Millisecond,
		MaxRetryDelay: 10 * time.Second,
	}

	if se, ok := any(data).(stateEmbedded); ok && se.self().Locker != nil {
		c.locker = se.self().Locker
	} else if l, ok := any(data).(sync.Locker); ok {
		c.locker = l
	}

	return c, nil
}

// ID returns the server-assigned state ID.
func (c *Client[T]) ID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.id
}

// Version returns the current version.
func (c *Client[T]) Version() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.version
}

// Connected returns true if the client is currently connected.
func (c *Client[T]) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected
}

// Connect starts the client connection. It blocks until the context is
// cancelled or an unrecoverable error occurs. If Retry is true (default),
// it will automatically reconnect on connection failures.
func (c *Client[T]) Connect(ctx context.Context) error {
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
func (c *Client[T]) Disconnect() {
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
func (c *Client[T]) connectOnce(ctx context.Context) error {
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
		httpClient = http.DefaultClient
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
func (c *Client[T]) readEvents(ctx context.Context) error {
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
				// If context was cancelled, treat as clean shutdown
				select {
				case <-ctx.Done():
					return nil
				default:
				}
				// Otherwise return error so retry loop can reconnect
				return fmt.Errorf("event stream closed unexpectedly: %w", err)
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

		// Update metadata
		c.mu.Lock()
		if update.ID != "" {
			c.id = update.ID
		}
		if update.Version > 0 {
			c.version = update.Version
		}

		// Apply update to internal state tracker
		var newState json.RawMessage
		if len(update.Body) == 0 {
			// Treat empty body as explicit state clear
			c.stateMap = nil
		} else if update.Delta && c.stateMap != nil {
			// Apply delta patch in-place using mergeObjects (zero-alloc)
			var patchMap map[string]any
			if err := json.Unmarshal(update.Body, &patchMap); err != nil {
				c.mu.Unlock()
				if c.OnError != nil {
					c.OnError(fmt.Errorf("failed to unmarshal patch: %w", err))
				}
				continue
			}
			mergeObjects(c.stateMap, patchMap)
			// Marshal the updated map to bytes for struct unmarshal
			merged, err := json.Marshal(c.stateMap)
			if err != nil {
				c.mu.Unlock()
				if c.OnError != nil {
					c.OnError(fmt.Errorf("failed to marshal state: %w", err))
				}
				continue
			}
			newState = merged
		} else {
			// Full state replacement — cache as map for future deltas
			var m map[string]any
			if err := json.Unmarshal(update.Body, &m); err == nil {
				c.stateMap = m
			}
			newState = update.Body
		}
		c.mu.Unlock()

		// Apply to user's data struct (with locking if supported)
		// Clear all maps in the data struct before unmarshaling to ensure deleted
		// map entries are properly removed (json.Unmarshal into existing maps doesn't delete keys)
		if len(newState) > 0 {
			if c.locker != nil {
				c.locker.Lock()
			}
			clearMaps(c.data)
			if err := json.Unmarshal(newState, c.data); err != nil {
				if c.locker != nil {
					c.locker.Unlock()
				}
				if c.OnError != nil {
					c.OnError(fmt.Errorf("failed to unmarshal into data: %w", err))
				}
				continue
			}
			// Bind all VMap/VSlice fields (nil pusher on client)
			bindAll(c.data, c.locker, nil)
			if c.locker != nil {
				c.locker.Unlock()
			}

			// Notify update (outside lock)
			if c.OnUpdate != nil {
				c.OnUpdate()
			}
		}
	}
}

// clearMaps recursively clears all maps in a struct using reflection.
// This is needed because json.Unmarshal only adds/updates map entries,
// it doesn't remove entries that are absent in the JSON.
func clearMaps(v any) {
	clearMapsValue(reflect.ValueOf(v))
}

func clearMapsValue(v reflect.Value) {
	if !v.IsValid() {
		return
	}
	switch v.Kind() {
	case reflect.Ptr, reflect.Interface:
		if !v.IsNil() {
			clearMapsValue(v.Elem())
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			field := v.Field(i)
			if field.CanSet() {
				clearMapsValue(field)
			}
		}
	case reflect.Map:
		if v.CanSet() && !v.IsNil() {
			v.Set(reflect.MakeMap(v.Type()))
		}
	case reflect.Slice:
		if v.CanSet() && !v.IsNil() {
			for i := 0; i < v.Len(); i++ {
				clearMapsValue(v.Index(i))
			}
		}
	}
}
