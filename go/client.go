package velox

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/jpillora/eventsource"
)

// DefaultMaxEventSize bounds one decoded server-sent event. The bound is on
// decompressed bytes as well, so it also prevents a compressed response from
// expanding without limit in a client process.
const DefaultMaxEventSize = 16 << 20

// ErrEventTooLarge is returned when a peer sends an SSE event larger than the
// configured Client.MaxEventSize.
var ErrEventTooLarge = errors.New("velox: SSE event exceeds configured limit")

// ErrSelectiveUnsupported means the server did not acknowledge a requested
// selective path. The client stops retrying because reconnecting cannot fix it.
var ErrSelectiveUnsupported = errors.New("velox: server does not support selective sync")

type eventDecoder interface {
	Decode(*eventsource.Event) error
}

// boundedSSEDecoder is the small subset of eventsource.Decoder the client
// needs, with a per-event byte cap. eventsource.Decoder uses ReadString, whose
// unbounded allocation makes a malicious `data:` line enough to exhaust a
// client before it can inspect the event.
type boundedSSEDecoder struct {
	r   *bufio.Reader
	max int
}

func newBoundedSSEDecoder(r io.Reader, max int) *boundedSSEDecoder {
	if max <= 0 {
		max = DefaultMaxEventSize
	}
	// Keep the idle allocation small. readLine joins ReadSlice fragments only
	// for the event actually being received, and stops once its cap is reached.
	return &boundedSSEDecoder{r: bufio.NewReaderSize(r, 32<<10), max: max}
}

func (d *boundedSSEDecoder) readLine() ([]byte, error) {
	var line []byte
	for {
		part, err := d.r.ReadSlice('\n')
		if len(part) > d.max-len(line) {
			return nil, ErrEventTooLarge
		}
		line = append(line, part...)
		if err == nil {
			return line, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return nil, err
	}
}

func (d *boundedSSEDecoder) Decode(event *eventsource.Event) error {
	if event == nil {
		return errors.New("event is nil")
	}
	*event = eventsource.Event{}
	var total int
	var hasData bool
	for {
		line, err := d.readLine()
		if err != nil {
			return err
		}
		total += len(line)
		if total > d.max {
			return ErrEventTooLarge
		}
		if len(line) > 0 && line[len(line)-1] == '\n' {
			line = line[:len(line)-1]
		}
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		if len(line) == 0 {
			return nil
		}
		if line[0] == ':' {
			continue
		}
		colon := 0
		for colon < len(line) && line[colon] != ':' {
			colon++
		}
		field, value := line, []byte(nil)
		if colon < len(line) {
			field, value = line[:colon], line[colon+1:]
			if len(value) > 0 && value[0] == ' ' {
				value = value[1:]
			}
		}
		switch string(field) {
		case "id":
			event.ID = string(value)
		case "event":
			event.Type = string(value)
		case "retry":
			event.Retry = string(value)
		case "data":
			if hasData {
				event.Data = append(event.Data, '\n')
			}
			event.Data = append(event.Data, value...)
			hasData = true
		}
	}
}

// Client connects to a Velox server and keeps local data in sync.
type Client[T any] struct {
	// URL is the velox sync endpoint URL
	URL string
	// Path selects one JSON subtree (for example, machines.local).
	// An empty Path selects the whole document; a leading $. is optional.
	// Set before Connect. The server must acknowledge it on every update.
	Path string
	// Paths selects several subtrees into a sparse document that preserves
	// their original locations. An empty list selects the whole document.
	// Use either Path or Paths, not both; set before Connect.
	Paths []string
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
	// MaxEventSize is the maximum decompressed size of one server-sent event
	// (default: DefaultMaxEventSize). A connection is dropped when a server
	// exceeds it rather than allowing one event to allocate unbounded memory.
	MaxEventSize int

	// internal state
	mu        sync.Mutex
	data      *T             // pointer to user's struct
	locker    sync.Locker    // non-nil if data implements sync.Locker
	stateMap  map[string]any // cached unmarshaled state for fast delta merge
	id        string         // server-assigned state ID
	version   int64          // current version
	root      string         // opaque v3 resume token for the state we hold
	connected bool
	body      io.ReadCloser
	dec       eventDecoder
	cancel    context.CancelFunc
	done      chan struct{}
}

// NewClient creates a new Velox client that syncs to the given pointer.
// data may point to a struct, map, or slice. If a struct embeds
// sync.Mutex (or implements sync.Locker), it will be locked during updates.
func NewClient[T any](url string, data *T) (*Client[T], error) {
	if data == nil {
		return nil, fmt.Errorf("data must not be nil")
	}

	// A selected subtree may be an object or array; an unselected document
	// remains an object as required by the wire protocol.
	t := reflect.TypeOf(data).Elem()
	if t.Kind() != reflect.Struct && t.Kind() != reflect.Map && t.Kind() != reflect.Slice {
		return nil, fmt.Errorf("data must be a pointer to a struct, map, or slice, got pointer to %s", t.Kind())
	}

	c := &Client[T]{
		URL:           url,
		data:          data,
		Retry:         true,
		MinRetryDelay: 100 * time.Millisecond,
		MaxRetryDelay: 10 * time.Second,
		MaxEventSize:  DefaultMaxEventSize,
	}

	if se, ok := any(data).(stateEmbedded); ok && se.self().Locker != nil {
		c.locker = se.self().Locker
	} else if l, ok := any(data).(sync.Locker); ok {
		c.locker = l
	}

	if c.locker != nil {
		c.locker.Lock()
	}
	bindAll(c.data, c.locker, nil)
	if c.locker != nil {
		c.locker.Unlock()
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
	if c.Path != "" && len(c.Paths) > 0 {
		return fmt.Errorf("velox: set Path or Paths, not both")
	}
	if c.Path != "" {
		if _, err := parseSyncPath(c.Path); err != nil {
			return err
		}
	}
	if _, _, err := parseSyncPaths(c.Paths); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	c.mu.Lock()
	if c.done != nil {
		c.mu.Unlock()
		cancel()
		return fmt.Errorf("velox: client is already connecting")
	}
	c.cancel = cancel
	c.done = done
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		// Do not let a finished connection clear a later connection's lifecycle
		// state. Connect currently rejects overlap, but keeping this ownership
		// check makes the cleanup safe if that policy changes.
		if c.done == done {
			c.cancel = nil
			c.done = nil
			close(done)
		}
		c.mu.Unlock()
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
		if errors.Is(err, ErrSelectiveUnsupported) {
			return err
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
	q := u.Query()
	q.Set("p", strconv.Itoa(ProtoVersion))
	if c.Path != "" {
		if _, err := parseSyncPath(c.Path); err != nil {
			c.mu.Unlock()
			return err
		}
		q.Set("path", c.Path)
	}
	if len(c.Paths) > 0 {
		ordered, _, err := parseSyncPaths(c.Paths)
		if err != nil {
			c.mu.Unlock()
			return err
		}
		encoded, _ := json.Marshal(ordered)
		q.Set("paths", string(encoded))
	}
	if c.version > 0 {
		q.Set("v", strconv.FormatInt(c.version, 10))
		if c.id != "" {
			q.Set("id", c.id)
		}
		// The resume token lets the server send operations spanning however many
		// versions were missed while disconnected, rather than a full snapshot.
		if c.root != "" {
			q.Set("h", c.root)
		}
	}
	u.RawQuery = q.Encode()
	c.mu.Unlock()

	// Create request
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Accept-Encoding", "gzip")

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

	// Wrap body with gzip reader if server sent compressed response
	var bodyReader io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gzReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			resp.Body.Close()
			return fmt.Errorf("failed to create gzip reader: %w", err)
		}
		bodyReader = gzReader
		resp.Body = &gzipReadCloser{gzReader: gzReader, body: resp.Body}
	}

	c.mu.Lock()
	c.body = resp.Body
	c.dec = newBoundedSSEDecoder(bodyReader, c.MaxEventSize)
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

		if update.Ping {
			continue
		}
		if err := c.applyUpdate(update); err != nil {
			return err
		}
	}
}

// applyUpdate validates and applies one non-ping update as one recovery unit.
// A failed patch must never advance the resume metadata: a v2 server only
// considers the version on reconnect, so advertising an unapplied delta would
// make it believe the client was current forever.
func (c *Client[T]) applyUpdate(update *Update) error {
	c.mu.Lock()
	requestedPaths, _, pathErr := parseSyncPaths(c.Paths)
	if pathErr != nil || c.Path != update.Path || !slices.Equal(requestedPaths, update.Paths) {
		c.clearResumeLocked()
		c.mu.Unlock()
		return fmt.Errorf("%w: %q", ErrSelectiveUnsupported, c.Path)
	}
	if update.Version <= 0 {
		c.clearResumeLocked()
		c.mu.Unlock()
		return fmt.Errorf("velox: update has no version")
	}

	identityChanged := update.ID != "" && c.id != "" && update.ID != c.id
	if !identityChanged {
		switch {
		case update.Version < c.version:
			// A client can persist a forged/future version alongside the genuine
			// root it holds. The server corrects that exact case with a lower
			// version and an empty v3 patch. It is safe to accept because both the
			// base and target prove the document is unchanged. Every other lower
			// message might be a stale replay, so resync instead of rolling back.
			if isNoopForHeldRoot(update, c.root) {
				c.version = update.Version
				c.mu.Unlock()
				return nil
			}
			previousVersion := c.version
			c.clearResumeLocked()
			c.mu.Unlock()
			return fmt.Errorf("velox: stale update version %d after %d", update.Version, previousVersion)
		case update.Version == c.version:
			// A v3 duplicate names the root already held. Its Base names the root
			// before the original update, so trying to apply it again would look
			// like divergence. A v2 same-version message is likewise a duplicate.
			if len(update.Ops) == 0 || update.Root == c.root {
				c.mu.Unlock()
				return nil
			}
			c.clearResumeLocked()
			c.mu.Unlock()
			return fmt.Errorf("velox: version %d names unexpected root %q", update.Version, update.Root)
		}
	}

	if update.Delta && len(update.Ops) > 0 {
		c.clearResumeLocked()
		c.mu.Unlock()
		return fmt.Errorf("velox: update carries both delta and operations")
	}
	if identityChanged && (update.Delta || len(update.Ops) > 0) {
		// A new state ID establishes a new epoch. Patches refer to a document
		// from the old epoch and can appear valid while corrupting it.
		c.clearResumeLocked()
		c.mu.Unlock()
		return fmt.Errorf("velox: new state id %q sent a patch", update.ID)
	}
	if update.Delta && update.Version != c.version+1 {
		c.clearResumeLocked()
		c.mu.Unlock()
		return fmt.Errorf("velox: delta skips from version %d to %d", c.version, update.Version)
	}

	var newState json.RawMessage
	switch {
	case c.Path != "" || len(c.Paths) > 0:
		// Selective streams only contain snapshots of the chosen subtree and
		// empty operation lists for version advances outside it. There is no
		// full-document map to retain or patch.
		if update.Delta || (len(update.Ops) > 0 && len(update.Body) > 0) {
			c.clearResumeLocked()
			c.mu.Unlock()
			return fmt.Errorf("velox: invalid selective sync representation")
		}
		if len(update.Ops) > 0 {
			var ops []op
			if c.root == "" || c.id == "" || update.Base != c.root || update.Root != c.root || json.Unmarshal(update.Ops, &ops) != nil || len(ops) != 0 {
				c.clearResumeLocked()
				c.mu.Unlock()
				return fmt.Errorf("velox: invalid selective sync operations")
			}
			c.version = update.Version
			c.mu.Unlock()
			return nil
		}
		if len(update.Body) == 0 || update.Root == "" || !json.Valid(update.Body) {
			c.clearResumeLocked()
			c.mu.Unlock()
			return fmt.Errorf("velox: invalid selective sync snapshot")
		}
		newState = update.Body
	case len(update.Body) == 0 && len(update.Ops) == 0:
		// A zero body is Velox's wire representation for a cleared state. Feed
		// JSON null through the normal struct-clear path below as well; merely
		// clearing stateMap used to leave the caller's old fields visible.
		c.stateMap = nil
		newState = json.RawMessage(`null`)
	case len(update.Ops) > 0:
		if firstJSONByte(update.Ops) != '[' || c.stateMap == nil || update.Base != c.root {
			c.clearResumeLocked()
			c.mu.Unlock()
			return fmt.Errorf("velox: operations apply to %q, not the state held", update.Base)
		}
		var ops []op
		if err := json.Unmarshal(update.Ops, &ops); err != nil {
			c.clearResumeLocked()
			c.mu.Unlock()
			return fmt.Errorf("velox: invalid operations: %w", err)
		}
		updated, err := applyOps(any(c.stateMap), ops)
		if err != nil {
			c.clearResumeLocked()
			c.mu.Unlock()
			return fmt.Errorf("velox: state diverged: %w", err)
		}
		var ok bool
		if c.stateMap, ok = updated.(map[string]any); !ok {
			c.clearResumeLocked()
			c.mu.Unlock()
			return fmt.Errorf("velox: operations replaced the root")
		}
		newState, err = json.Marshal(c.stateMap)
		if err != nil {
			c.clearResumeLocked()
			c.mu.Unlock()
			return fmt.Errorf("velox: marshal patched state: %w", err)
		}
	case update.Delta:
		if c.stateMap == nil {
			c.clearResumeLocked()
			c.mu.Unlock()
			return fmt.Errorf("velox: delta has no base state")
		}
		var patchMap map[string]any
		if err := json.Unmarshal(update.Body, &patchMap); err != nil || patchMap == nil {
			if err == nil {
				err = errors.New("delta is not an object")
			}
			c.clearResumeLocked()
			c.mu.Unlock()
			return fmt.Errorf("velox: invalid delta: %w", err)
		}
		mergeObjects(c.stateMap, patchMap)
		var err error
		newState, err = json.Marshal(c.stateMap)
		if err != nil {
			c.clearResumeLocked()
			c.mu.Unlock()
			return fmt.Errorf("velox: marshal patched state: %w", err)
		}
	default:
		// A full state must be an object (or null for a clear) so it can serve
		// as the base for later merge patches. Rejecting another JSON shape keeps
		// a malformed snapshot from poisoning stateMap while the caller data is
		// only partially unmarshaled.
		var stateMap map[string]any
		if err := json.Unmarshal(update.Body, &stateMap); err != nil {
			c.clearResumeLocked()
			c.mu.Unlock()
			return fmt.Errorf("velox: invalid full state: %w", err)
		}
		if stateMap == nil && !isJSONNull(update.Body) {
			c.clearResumeLocked()
			c.mu.Unlock()
			return fmt.Errorf("velox: full state is not an object")
		}
		c.stateMap = stateMap
		newState = update.Body
	}

	// stateMap is private to this decoder. Release the metadata lock before
	// acquiring a caller-provided locker: callers commonly read their data and
	// then ask Version(), and holding the locks in the opposite order deadlocks.
	newID := c.id
	if update.ID != "" {
		newID = update.ID
	}
	c.mu.Unlock()

	if c.locker != nil {
		c.locker.Lock()
	}
	clearForUnmarshal(c.data)
	err := json.Unmarshal(newState, c.data)
	if err == nil {
		// Bind all VMap/VSlice fields (nil pusher on client).
		bindAll(c.data, c.locker, nil)
	}
	if c.locker != nil {
		c.locker.Unlock()
	}
	if err != nil {
		c.mu.Lock()
		c.clearResumeLocked()
		c.mu.Unlock()
		return fmt.Errorf("velox: unmarshal into data: %w", err)
	}

	c.mu.Lock()
	c.id = newID
	c.version = update.Version
	// Every non-delta update is a v3 operation or a full snapshot, either of
	// which establishes its target root. A v2 delta deliberately leaves it
	// alone (normally empty) because the old protocol has no root token.
	if !update.Delta {
		c.root = update.Root
	}
	c.mu.Unlock()

	if c.OnUpdate != nil {
		c.OnUpdate()
	}
	return nil
}

// isNoopForHeldRoot recognises the one valid version rollback: a server
// correcting an implausibly high resume version while confirming that the root
// the client supplied is already current. Decode Ops rather than comparing
// bytes so harmless JSON whitespace cannot turn a safe correction into a
// reconnect loop.
func isNoopForHeldRoot(update *Update, heldRoot string) bool {
	if heldRoot == "" || update.Root != heldRoot || update.Base != heldRoot || firstJSONByte(update.Ops) != '[' {
		return false
	}
	var ops []op
	return json.Unmarshal(update.Ops, &ops) == nil && len(ops) == 0
}

// clearResumeLocked forgets all protocol state after a failed update. The
// caller must hold c.mu. In particular ID must go too: the next request must
// be indistinguishable from a fresh client so an older v2 peer sends a full
// snapshot instead of deciding its version is current.
func (c *Client[T]) clearResumeLocked() {
	c.stateMap = nil
	c.id = ""
	c.version = 0
	c.root = ""
}

// clearForUnmarshal zeros all JSON-serializable fields in a struct before
// unmarshaling the complete stateMap. Since the stateMap always represents the
// full state, json.Unmarshal will re-populate all fields that should have values.
// Fields tagged with json:"-" are preserved (e.g., sync.Locker, internal state).
// Struct fields are recursed into so container bindings and json:"-" fields
// survive the reset. Custom JSON types retain their zero-before-unmarshal behavior.
func clearForUnmarshal(v any) {
	clearForUnmarshalValue(reflect.ValueOf(v))
}

func clearForUnmarshalValue(v reflect.Value) {
	if !v.IsValid() {
		return
	}
	if v.CanAddr() && v.Addr().CanInterface() {
		if container, ok := v.Addr().Interface().(interface{ clearForUnmarshal() }); ok {
			container.clearForUnmarshal()
			return
		}
	}
	switch v.Kind() {
	case reflect.Ptr:
		if !v.IsNil() {
			clearForUnmarshalValue(v.Elem())
		}
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			field := v.Field(i)
			ft := t.Field(i)
			if !field.CanSet() {
				continue
			}
			tag := ft.Tag.Get("json")
			if tag == "-" {
				continue
			}
			if field.CanAddr() && field.Addr().CanInterface() {
				if container, ok := field.Addr().Interface().(interface{ clearForUnmarshal() }); ok {
					container.clearForUnmarshal()
					continue
				}
			}
			if field.Kind() == reflect.Struct {
				if _, custom := field.Addr().Interface().(json.Unmarshaler); !ft.Anonymous && custom {
					field.Set(reflect.Zero(ft.Type))
					continue
				}
				clearForUnmarshalValue(field)
				continue
			}
			field.Set(reflect.Zero(ft.Type))
		}
	case reflect.Map, reflect.Slice:
		v.Set(reflect.Zero(v.Type()))
	}
}
