# velox

[![GoDoc](https://godoc.org/github.com/jpillora/velox?status.svg)](https://godoc.org/github.com/jpillora/velox)

Real-time JS object synchronisation over SSE and WebSockets in Go and JavaScript (Node.js and browser)

### Features

- Simple API
- Synchronise any JSON marshallable struct in Go
- Synchronise any JSON stringifiable struct in Node
- Delta updates using [JSONPatch (RFC6902)](https://tools.ietf.org/html/rfc6902)
- Supports [Server-Sent Events (EventSource)](https://en.wikipedia.org/wiki/Server-sent_events) and [WebSockets](https://en.wikipedia.org/wiki/WebSocket)
- SSE [client-side poly-fill](https://github.com/remy/polyfills/blob/master/EventSource.js) to fallback to long-polling in older browsers (IE8+).
- Generic `VMap` and `VSlice` containers with automatic locking and push-on-write
- Go client (`velox.Client[T]`) for server-to-server sync

### Quick Usage

Server (Go)

```go
type Foo struct {
	velox.State
	A, B int
}
foo := &Foo{}
http.Handle("/velox.js", velox.JS)
http.Handle("/sync", velox.SyncHandler(foo))
// make changes and push to all clients
foo.A = 42
foo.B = 21
foo.Push()
```

### Node / Browser

Server (Node)

```js
//syncable object
let foo = {
  a: 1,
  b: 2
};
//express server
let app = express();
//serve velox.js client library (assets/dist/velox.min.js)
app.get("/velox.js", velox.JS);
//serve velox sync endpoint for foo (adds $push method)
app.get("/sync", velox.handle(foo));
//make changes
foo.a = 42;
foo.b = 21;
//push to client
foo.$push();
```

Client (Node and Browser)

```js
// load script /velox.js
var foo = {};
var v = velox("/sync", foo);
v.onupdate = function() {
  //foo.A === 42 and foo.B === 21
};
```

### API

Server API (Go)

[![GoDoc](https://godoc.org/github.com/jpillora/velox?status.svg)](https://godoc.org/github.com/jpillora/velox)

Server API (Node)

- `velox.handle(object)` _function_ returns `v` - Creates a new route handler for use with express
- `velox.state(object)` _function_ returns `state` - Creates or restores a velox state from a given object
- `state.handle(req, res)` _function_ returns `Promise` - Handle the provided `express` request/response. Resolves on connection close. Rejects on any error.

Client API (Node and Browser)

- `velox(url, object)` _function_ returns `v` - Creates a new SSE velox connection
- `velox.sse(url, object)` _function_ returns `v` - Creates a new SSE velox connection
- `velox.ws(url, object)` _function_ returns `v` - Creates a new WS velox connection
- `v.onupdate(object)` _function_ - Called when a server push is received
- `v.onerror(err)` _function_ - Called when a connection error occurs
- `v.onconnect()` _function_ - Called when the connection is opened
- `v.ondisconnect()` _function_ - Called when the connection is closed
- `v.onchange(bool)` _function_ - Called when the connection is opened or closed
- `v.connected` _bool_ - Denotes whether the connection is currently open
- `v.ws` _bool_ - Denotes whether the connection is in web sockets mode
- `v.sse` _bool_ - Denotes whether the connection is in server-sent events mode

### Example

See this [simple `example/`](example/) and view it live here: https://velox.jpillora.com

![screenshot](https://cloud.githubusercontent.com/assets/633843/13481947/8eea1804-e13d-11e5-80c8-be9317c54fbc.png)

_Here is a screenshot from this example page, showing the messages arriving as either a full replacement of the object or just a delta. The server will send which ever is smaller._

### VMap and VSlice

`VMap[K, V]` and `VSlice[V]` are generic containers that automatically lock the
root struct and push changes to clients on every write operation. This removes
the need to manually call `Lock`/`Unlock`/`Push` when mutating map or slice
fields.

```go
type App struct {
	sync.RWMutex
	velox.State
	Settings velox.VMap[string, string] `json:"settings"`
	Scores   velox.VMap[string, int]    `json:"scores"`
	Logs     velox.VSlice[string]       `json:"logs"`
}

app := &App{}
http.Handle("/sync", velox.SyncHandler(app))

// Each call locks the RWMutex, mutates the data, and pushes (throttled).
// No manual Lock/Unlock/Push needed.
app.Settings.Set("theme", "dark")
app.Scores.Batch(func(data map[string]int) {
	data["alice"] = 100
	data["bob"] = 85
})
app.Logs.Append("server started")
```

`SyncHandler` automatically binds all `VMap`/`VSlice` fields to the struct's
mutex and `State` pusher. On the client side, `velox.Client[T]` rebinds after
each update.

**How locking works:**

- Write methods (`Set`, `Delete`, `Append`, `Update`, `Batch`, `Clear`) acquire
  the root struct's `Lock()`, mutate the data, call `State.Push()`, then
  `Unlock()`.
- Read methods (`Get`, `Len`, `Keys`, `Values`, `Snapshot`, `Range`) use
  `RLock()`/`RUnlock()` when the root struct embeds `sync.RWMutex`, allowing
  concurrent readers. Falls back to `Lock()`/`Unlock()` for `sync.Mutex`.
- `State.Push()` is throttled (default 200ms) and non-blocking -- it spawns a
  goroutine that waits for the lock to be released, marshals the struct, computes
  a delta, and sends it to all connected clients. Rapid mutations are coalesced
  into fewer pushes.
- `MarshalJSON`/`UnmarshalJSON` on VMap/VSlice do not lock -- the parent already
  holds the lock during marshal.

**VMap methods:**

| Write (lock + push) | Read (rlock) |
|---|---|
| `Set(key, value)` | `Get(key) (V, bool)` |
| `Delete(key)` | `Has(key) bool` |
| `Update(key, func(*V)) bool` | `Len() int` |
| `Batch(func(map[K]V))` | `Keys() []K` |
| `Clear()` | `Values() []V` |
| | `Snapshot() map[K]V` |
| | `Range(func(K, V) bool)` |

**VSlice methods:**

| Write (lock + push) | Read (rlock) |
|---|---|
| `Set([]V)` | `Get() []V` |
| `Append(values...)` | `At(index) (V, bool)` |
| `SetAt(index, value) bool` | `Len() int` |
| `DeleteAt(index) bool` | `Range(func(int, V) bool)` |
| `Update(index, func(*V)) bool` | |
| `Batch(func(*[]V))` | |
| `Clear()` | |

### Notes

- Object synchronization is one way (server to client) only.
- JS object properties beginning with `$` will be ignored to play nice with Angular.
- JS object with an `$apply` function will automatically be called on each update to play nice with Angular.

#### MIT License

Copyright © 2018 Jaime Pillora &lt;dev@jpillora.com&gt;

Permission is hereby granted, free of charge, to any person obtaining
a copy of this software and associated documentation files (the
'Software'), to deal in the Software without restriction, including
without limitation the rights to use, copy, modify, merge, publish,
distribute, sublicense, and/or sell copies of the Software, and to
permit persons to whom the Software is furnished to do so, subject to
the following conditions:

The above copyright notice and this permission notice shall be
included in all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED 'AS IS', WITHOUT WARRANTY OF ANY KIND,
EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.
IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY
CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,
TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE
SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
