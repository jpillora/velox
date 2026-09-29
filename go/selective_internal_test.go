package velox

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

type selectedCapture struct{ updates []*Update }

func (*selectedCapture) connect(http.ResponseWriter, *http.Request) error { return nil }
func (c *selectedCapture) send(u *Update) error {
	copy := *u
	c.updates = append(c.updates, &copy)
	return nil
}
func (*selectedCapture) wait() error  { return nil }
func (*selectedCapture) drain()       {}
func (*selectedCapture) close() error { return nil }

func TestSelectiveServerSendsOnlyChosenSubtree(t *testing.T) {
	s := New(func() (json.RawMessage, error) {
		return json.RawMessage(`{"chosen":{"value":1},"ignored":"private"}`), nil
	})
	parts, _ := parseSyncPath("chosen")
	c := newConn(1, "test", s, 0, ProtoVersion, "")
	c.path, c.pathParts = "chosen", parts
	transport := &selectedCapture{}
	c.transport = transport
	c.Push()
	if len(transport.updates) != 1 || string(transport.updates[0].Body) != `{"value":1}` || transport.updates[0].Path != c.path {
		t.Fatalf("wrong initial selection: %+v", transport.updates)
	}
	firstRoot := transport.updates[0].Root
	s.data.mut.Lock()
	s.data.bytes = []byte(`{"chosen":{"value":1},"ignored":"changed"}`)
	s.data.version++
	s.data.mut.Unlock()
	c.Push()
	if len(transport.updates) != 2 || string(transport.updates[1].Ops) != "[]" || transport.updates[1].Base != firstRoot {
		t.Fatalf("unrelated change did not produce a no-op: %+v", transport.updates)
	}
	s.data.mut.Lock()
	s.data.bytes = []byte(`{"chosen":{"value":2},"ignored":"changed"}`)
	s.data.version++
	s.data.mut.Unlock()
	c.Push()
	if len(transport.updates) != 3 || string(transport.updates[2].Body) != `{"value":2}` {
		t.Fatalf("changed selection was not sent: %+v", transport.updates)
	}
}

func TestSelectivePathRejectedBeforeStream(t *testing.T) {
	s := New(func() (json.RawMessage, error) { return json.RawMessage(`{"chosen":{}}`), nil })
	for _, query := range []string{"?p=3&path=%24..bad", "?p=2&path=chosen", "?p=3&paths=null", "?p=3&path=a&paths=%5B%22b%22%5D"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "http://example/sync"+query, nil)
		s.ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d", query, w.Code)
		}
	}
}

func TestSelectivePathAndProjection(t *testing.T) {
	for _, tc := range []struct{ path, body, want string }{
		{`users[0]["display name"]`, `{"users":[{"display name":{"value":1}}]}`, `{"value":1}`},
		{`$.users[0]["display name"]`, `{"users":[{"display name":{"value":1}}]}`, `{"value":1}`},
		{`["quoted key"]`, `{"quoted key":{"value":2}}`, `{"value":2}`},
		{`$.users[1]`, `{"users":[{"value":1}]}`, `null`},
		{`$.user.child`, `{"user":7}`, `null`},
	} {
		parts, err := parseSyncPath(tc.path)
		if err != nil {
			t.Fatal(err)
		}
		got, err := selectSyncBody([]byte(tc.body), parts)
		if err != nil || string(got) != tc.want {
			t.Fatalf("%s: %s, %v; want %s", tc.path, got, err, tc.want)
		}
	}
	if parts, err := parseSyncPath(""); err != nil || len(parts) != 0 {
		t.Fatalf("empty path did not select root: %v, %v", parts, err)
	}
	for _, path := range []string{"$", "$.1bad", "$..bad", "$[01]", "$[\"bad\"", "$.ok.*"} {
		if _, err := parseSyncPath(path); err == nil {
			t.Fatalf("accepted invalid path %q", path)
		}
	}
}

func TestMultiPathProjection(t *testing.T) {
	paths, parts, err := parseSyncPaths([]string{"settings.theme", "machines.local", "items[2].id", "settings.theme"})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 3 {
		t.Fatalf("duplicate path was retained: %v", paths)
	}
	body := []byte(`{"machines":{"local":{"name":"laptop"},"remote":{"name":"skip"}},"settings":{"theme":"dark","secret":"skip"},"items":[{"id":0},{"id":1},{"id":2}],"ignored":"skip"}`)
	selected, err := projectSyncBody(body, parts)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"items":[null,null,{"id":2}],"machines":{"local":{"name":"laptop"}},"settings":{"theme":"dark"}}`
	if string(selected) != want {
		t.Fatalf("projection = %s, want %s", selected, want)
	}
	_, parts, err = parseSyncPaths([]string{"machines.local.name", "machines.local"})
	if err != nil {
		t.Fatal(err)
	}
	selected, err = projectSyncBody(body, parts)
	if err != nil || string(selected) != `{"machines":{"local":{"name":"laptop"}}}` {
		t.Fatalf("overlap = %s, %v", selected, err)
	}
	_, parts, _ = parseSyncPaths([]string{"missing", "alsoMissing"})
	selected, err = projectSyncBody(body, parts)
	if err != nil || string(selected) != `{}` {
		t.Fatalf("missing paths = %s, %v", selected, err)
	}
	selected, err = projectSyncBody([]byte(`null`), parts)
	if err != nil || string(selected) != `null` {
		t.Fatalf("cleared source = %s, %v", selected, err)
	}
	if _, _, err := parseSyncPaths([]string{"items[65536]"}); err == nil {
		t.Fatal("accepted unbounded projected array")
	}
}

func TestMultiPathServerSendsOnlySelectedBranches(t *testing.T) {
	s := New(func() (json.RawMessage, error) { return json.RawMessage(`{"a":{"x":1},"b":2,"ignored":"skip"}`), nil })
	paths, parts, _ := parseSyncPaths([]string{"b", "a.x"})
	c := newConn(2, "test", s, 0, ProtoVersion, "")
	c.paths, c.pathSets = paths, parts
	transport := &selectedCapture{}
	c.transport = transport
	c.Push()
	if len(transport.updates) != 1 || string(transport.updates[0].Body) != `{"a":{"x":1},"b":2}` || len(transport.updates[0].Paths) != 2 {
		t.Fatalf("wrong multi-path snapshot: %+v", transport.updates)
	}
	s.data.mut.Lock()
	s.data.bytes = []byte(`{"a":{"x":1},"b":2,"ignored":"changed"}`)
	s.data.version++
	s.data.mut.Unlock()
	c.Push()
	if len(transport.updates) != 2 || string(transport.updates[1].Ops) != "[]" {
		t.Fatalf("unrelated change resent projection: %+v", transport.updates)
	}
}

func TestMultiPathClientSnapshots(t *testing.T) {
	type held struct {
		A struct {
			X int `json:"x"`
		} `json:"a"`
		B int `json:"b"`
	}
	data := &held{}
	c, err := NewClient("http://example", data)
	if err != nil {
		t.Fatal(err)
	}
	c.Paths = []string{"b", "a.x"}
	if err := c.applyUpdate(&Update{ID: "state", Version: 1, Paths: []string{"a.x", "b"}, Root: "first", Body: json.RawMessage(`{"a":{"x":1},"b":2}`)}); err != nil {
		t.Fatal(err)
	}
	if data.A.X != 1 || data.B != 2 || c.stateMap != nil {
		t.Fatal("multi-path snapshot was not applied without full-state cache")
	}
	if err := c.applyUpdate(&Update{Version: 2, Paths: []string{"a.x", "b"}, Root: "first", Base: "first", Ops: json.RawMessage(`[]`)}); err != nil {
		t.Fatal(err)
	}
	if c.Version() != 2 {
		t.Fatal("no-op did not advance version")
	}
	if err := c.applyUpdate(&Update{Version: 3, Paths: []string{"a.x", "b"}, Root: "first", Base: "first", Ops: json.RawMessage(`[]`), Body: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("accepted snapshot and operations together")
	}
	if err := c.applyUpdate(&Update{Version: 3, Path: "a.x", Root: "wrong", Body: json.RawMessage(`{"a":{"x":9}}`)}); err == nil {
		t.Fatal("accepted mismatched path acknowledgement")
	}
}

func TestSelectiveClientSnapshotsAndNoops(t *testing.T) {
	type held struct {
		Value int `json:"value"`
	}
	data := &held{}
	c, err := NewClient("http://example", data)
	if err != nil {
		t.Fatal(err)
	}
	c.Path = "chosen"
	first := &Update{ID: "state", Version: 1, Path: c.Path, Root: "a", Body: json.RawMessage(`{"value":1}`)}
	if err := c.applyUpdate(first); err != nil {
		t.Fatal(err)
	}
	if data.Value != 1 || c.stateMap != nil {
		t.Fatal("selected snapshot retained a full state map")
	}
	if err := c.applyUpdate(&Update{Version: 2, Path: c.Path, Root: "a", Base: "a", Ops: json.RawMessage(`[]`)}); err != nil {
		t.Fatal(err)
	}
	if c.Version() != 2 || data.Value != 1 {
		t.Fatal("unrelated change modified the selection")
	}
	if err := c.applyUpdate(&Update{Version: 3, Path: c.Path, Root: "b", Body: json.RawMessage(`{"value":3}`)}); err != nil {
		t.Fatal(err)
	}
	if data.Value != 3 {
		t.Fatal("changed subtree was not applied")
	}
	if err := c.applyUpdate(&Update{Version: 4, Root: "wrong", Body: json.RawMessage(`{"value":9}`)}); err == nil {
		t.Fatal("accepted unacknowledged path")
	}
	if c.Version() != 0 {
		t.Fatal("path mismatch retained resume metadata")
	}

	var array []int
	a, err := NewClient("http://example", &array)
	if err != nil {
		t.Fatal(err)
	}
	a.Path = "items"
	if err := a.applyUpdate(&Update{ID: "state", Version: 1, Path: a.Path, Root: "array", Body: json.RawMessage(`[1,2,3]`)}); err != nil {
		t.Fatal(err)
	}
	if len(array) != 3 {
		t.Fatal("selected array was not decoded")
	}
	if err := a.applyUpdate(&Update{Version: 2, Path: a.Path, Root: "null", Body: json.RawMessage(`null`)}); err != nil {
		t.Fatal(err)
	}
	if array != nil {
		t.Fatal("missing selected array was not cleared")
	}
}

// Measure live heap, including the client's retained representation. The same
// encoded source document stays resident in both cases; only client state
// changes. This guards the intended benefit as well as logging actual bytes.
func TestSelectiveClientRetainsLessHeap(t *testing.T) {
	ignored := make(map[string]string, 30000)
	for i := 0; i < 30000; i++ {
		ignored[fmt.Sprintf("key-%06d", i)] = strings.Repeat("x", 128)
	}
	body, err := json.Marshal(map[string]any{"chosen": map[string]int{"value": 7}, "ignored": ignored})
	if err != nil {
		t.Fatal(err)
	}
	ignored = nil
	measure := func() uint64 { runtime.GC(); var m runtime.MemStats; runtime.ReadMemStats(&m); return m.HeapAlloc }
	base := measure()
	type fullData struct {
		Chosen  map[string]int    `json:"chosen"`
		Ignored map[string]string `json:"ignored"`
	}
	fullValue := &fullData{}
	full, _ := NewClient("http://example", fullValue)
	if err := full.applyUpdate(&Update{ID: "state", Version: 1, Root: "full", Body: body}); err != nil {
		t.Fatal(err)
	}
	fullBytes := measure() - base
	runtime.KeepAlive(full)
	runtime.KeepAlive(fullValue)
	full, fullValue = nil, nil
	base = measure()
	selectedValue := &struct {
		Value int `json:"value"`
	}{}
	selected, _ := NewClient("http://example", selectedValue)
	selected.Path = "chosen"
	if err := selected.applyUpdate(&Update{ID: "state", Version: 1, Path: selected.Path, Root: "selected", Body: json.RawMessage(`{"value":7}`)}); err != nil {
		t.Fatal(err)
	}
	selectedBytes := measure() - base
	runtime.KeepAlive(selected)
	runtime.KeepAlive(selectedValue)
	selected, selectedValue = nil, nil
	base = measure()
	paths, parts, err := parseSyncPaths([]string{"chosen", "absent"})
	if err != nil {
		t.Fatal(err)
	}
	projected, err := projectSyncBody(body, parts)
	if err != nil {
		t.Fatal(err)
	}
	multiValue := &struct {
		Chosen struct {
			Value int `json:"value"`
		} `json:"chosen"`
	}{}
	multi, _ := NewClient("http://example", multiValue)
	multi.Paths = paths
	if err := multi.applyUpdate(&Update{ID: "state", Version: 1, Paths: paths, Root: "multi", Body: projected}); err != nil {
		t.Fatal(err)
	}
	multiBytes := measure() - base
	runtime.KeepAlive(multi)
	runtime.KeepAlive(multiValue)
	runtime.KeepAlive(body)
	t.Logf("retained client heap: full=%d bytes, one path=%d bytes, two paths=%d bytes", fullBytes, selectedBytes, multiBytes)
	if fullBytes < selectedBytes*4 || fullBytes < multiBytes*4 {
		t.Fatalf("selective sync did not materially reduce retained heap")
	}
}
