package velox_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jpillora/eventsource"
	velox "github.com/jpillora/velox/go"
	"golang.org/x/sync/errgroup"
)

func TestBasicSync(t *testing.T) {
	// Create a test struct that embeds velox.State
	type TestStruct struct {
		velox.State
		Value string
	}
	test := &TestStruct{Value: "test-value"}
	// setup HTTP server for testing
	server := httptest.NewServer(velox.SyncHandler(test))
	hclient := server.Client()
	defer server.Close()
	t.Log("server up")
	// Test successful sync connection
	client := &testClient{
		id:   1,
		url:  server.URL,
		http: hclient,
	}
	if err := client.connect(t.Context()); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer client.disconnect()
	t.Log("client up")
	u, id, err := client.next()
	if err != nil {
		t.Fatalf("Failed to get next event: %v", err)
	}
	if id != "0" {
		t.Fatalf("Expected event ID 0, got %s", id)
	}
	if !u.Ping {
		t.Fatalf("Expected ping, got: %+v", u)
	}
	t.Log("got ping")
	nid := 1
	next := func() *TestStruct {
		u, id, err := client.next()
		if err != nil {
			t.Fatalf("Failed to get next event: %v", err)
		}
		if id != strconv.Itoa(nid) {
			t.Fatalf("Expected event ID %d, got %s", nid, id)
		}
		nid++
		if u.Ping {
			t.Fatalf("Expected data, got ping: %+v", u)
		}
		data := &TestStruct{}
		if err := json.Unmarshal(u.Body, data); err != nil {
			t.Fatalf("Failed to unmarshal user data: %v", err)
		}
		return data
	}
	if t1 := next(); t1.Value != "test-value" {
		t.Fatalf("Expected event data to be test-value, got %s", t1.Value)
	}
	t.Log("got event 0 and 1")
	// push an update
	test.Value = "test-value-2"
	test.Push()
	// wait for the update
	if t2 := next(); t2.Value != "test-value-2" {
		t.Fatalf("Expected event data to be test-value-2, got %s", t2.Value)
	}
	t.Log("got event 2")
}

func TestMultiSync(t *testing.T) {
	// context 5 second timeout
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// Create a test struct that embeds velox.State
	type TestStruct struct {
		velox.State
		sync.Mutex
		Value string
	}
	test := &TestStruct{Value: "test-value-0"}
	test.State.Throttle = 2 * time.Millisecond
	// setup HTTP server for testing
	server := httptest.NewServer(velox.SyncHandler(test))
	defer server.Close()
	// Test successful sync connection
	clients := newClients(5, server.URL)
	defer func() {
		for _, client := range clients {
			client.disconnect()
		}
	}()
	// ping phase
	ping, _ := errgroup.WithContext(ctx)
	for _, client := range clients {
		ping.Go(func() error {
			if err := client.connect(ctx); err != nil {
				return fmt.Errorf("Failed to connect: %v", err)
			}
			u, id, err := client.next()
			if err != nil {
				return fmt.Errorf("Failed to get next event: %v", err)
			}
			if id != "0" {
				return fmt.Errorf("Expected event ID 0, got %s", id)
			}
			if !u.Ping {
				return fmt.Errorf("Expected ping, got: %+v", u)
			}
			return nil
		})
	}
	if err := ping.Wait(); err != nil {
		t.Fatalf("Failed to ping: %v", err)
	}
	// now server sends out 100 updates
	const count = 100
	go func() {
		for i := range count {
			test.Lock()
			test.Value = fmt.Sprintf("test-value-%d", i+1)
			test.Unlock()
			test.Push()
			time.Sleep(50 * time.Millisecond)
		}
	}()
	// data phase
	data, _ := errgroup.WithContext(ctx)
	for _, client := range clients {
		data.Go(func() error {
			curr := -1
			for {
				u, _, err := client.next()
				if err != nil {
					return fmt.Errorf("client %d: failed to get next event: %v", client.id, err)
				}
				// if expect := strconv.Itoa(i + 1); id != expect {
				// 	return fmt.Errorf("client %d: expected event ID %s, got %s", client.id, expect, id)
				// }
				data := &TestStruct{}
				if err := json.Unmarshal(u.Body, data); err != nil {
					return fmt.Errorf("client %d: failed to unmarshal user data: %v", client.id, err)
				}
				n := strings.TrimPrefix(data.Value, "test-value-")
				v, err := strconv.Atoi(n)
				if err != nil {
					return fmt.Errorf("client %d: failed to parse value: %v", client.id, err)
				}
				if v <= curr {
					return fmt.Errorf("client %d: expected event ID to be > %d, got %s", client.id, curr, data.Value)
				}
				curr = v
				if v == count {
					break
				}
			}
			t.Logf("client %d: done", client.id)
			return nil
		})
	}
	if err := data.Wait(); err != nil {
		t.Fatalf("Failed to receive data: %v", err)
	}
	t.Log("done")
}

type testClient struct {
	id   int
	url  string
	http *http.Client
	body io.ReadCloser
	dec  *eventsource.Decoder
}

func (c *testClient) do(req *http.Request) (*http.Response, error) {
	if c.http != nil {
		return c.http.Do(req)
	}
	return http.DefaultTransport.RoundTrip(req)
}

func (c *testClient) connect(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		if err := c.disconnect(); err != nil {
			fmt.Printf("Failed to disconnect: %v", err)
		}
	}()
	req, err := http.NewRequestWithContext(ctx, "GET", c.url, nil)
	if err != nil {
		return fmt.Errorf("Failed to create request: %v", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.do(req)
	if err != nil {
		return fmt.Errorf("HTTP request failed: %v", err)
	}
	c.body = resp.Body
	// Should get a 200 response with proper headers for event streaming
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Expected status 200, got %d", resp.StatusCode)
	}
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		return fmt.Errorf("Expected Content-Type text/event-stream, got %s", resp.Header.Get("Content-Type"))
	}
	c.dec = eventsource.NewDecoder(resp.Body)
	return nil
}

func (c *testClient) disconnect() error {
	if c.body != nil {
		if err := c.body.Close(); err != nil {
			return fmt.Errorf("Failed to close body: %v", err)
		}
		c.body = nil
	}
	return nil
}

func (c *testClient) next() (update *velox.Update, eventID string, err error) {
	if c.dec == nil {
		return nil, "", fmt.Errorf("not connecteds")
	}
	e := &eventsource.Event{}
	if err := c.dec.Decode(e); err != nil {
		return nil, e.ID, fmt.Errorf("Failed to decode event: %v", err)
	}
	u := &velox.Update{}
	if err := json.Unmarshal([]byte(e.Data), u); err != nil {
		return nil, e.ID, fmt.Errorf("Failed to unmarshal velox update: %v", err)
	}
	return u, e.ID, nil
}

func TestNilMapMarshal(t *testing.T) {
	// Reproduces the panic: reflect: call of reflect.Value.Set on zero Value
	// when a struct with a nil map is pushed.
	type Inner struct {
		Tags map[string]string
	}
	type TestStruct struct {
		velox.State
		sync.Mutex
		Name  string
		Inner *Inner
	}
	test := &TestStruct{Name: "test", Inner: &Inner{Tags: nil}}
	server := httptest.NewServer(velox.SyncHandler(test))
	defer server.Close()
	client := &testClient{id: 1, url: server.URL}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.connect(ctx); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer client.disconnect()
	// consume ping
	if u, _, err := client.next(); err != nil {
		t.Fatalf("Failed to get ping: %v", err)
	} else if !u.Ping {
		t.Fatalf("Expected ping")
	}
	// consume initial state
	if _, _, err := client.next(); err != nil {
		t.Fatalf("Failed to get initial state: %v", err)
	}
	// now set a nil map and push - this previously caused a panic
	test.Lock()
	test.Inner = &Inner{Tags: nil}
	test.Unlock()
	test.Push()
	// wait for throttle to pass and push again with valid data
	time.Sleep(300 * time.Millisecond)
	test.Lock()
	test.Name = "updated"
	test.Unlock()
	test.Push()
	// should still be able to receive updates (server didn't crash)
	u, _, err := client.next()
	if err != nil {
		t.Fatalf("Failed to get update after nil map push: %v", err)
	}
	if u.Ping {
		t.Fatalf("Expected data, got ping")
	}
	data := &TestStruct{}
	if err := json.Unmarshal(u.Body, data); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}
	if data.Name != "updated" {
		t.Fatalf("Expected name=updated, got %s", data.Name)
	}
}

func newClients(count int, url string) []*testClient {
	clients := make([]*testClient, count)
	for i := 0; i < count; i++ {
		clients[i] = &testClient{id: i + 1, url: url}
	}
	return clients
}
