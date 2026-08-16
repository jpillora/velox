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
- `v.ondisconnect()` _function_ - When the handler declares no parameters (arity 0, the default), it is called once on each disconnect as a transition notification while velox continues to reconnect on its own with exponential backoff.
- `v.ondisconnect(retry)` _function_ - When the handler declares a `retry` parameter (arity 1), velox suppresses its own backoff retries and instead invokes this handler on every connection close (including while offline), passing a `retry` trigger so the caller controls reconnect timing (e.g. to drive a visible countdown). Call `retry()` to reconnect.
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

### Protocol

velox speaks two protocols and negotiates between them, so old and new
client/server pairings keep working in both directions. A client advertises what
it wants with the `p` query parameter; anything below 3, including its absence,
is served v2 exactly as before. Clients keep both appliers and choose per
message, so asking for v3 and being answered in v2 -- an older server, or a
deployment mid-rollout -- is handled rather than an error.

**v2** sends either a full snapshot or an RFC 7386 merge patch, and a patch only
exists for one version hop. A client that lagged, reconnected or reloaded the
page gets the whole document.

**v3** maintains a merkle tree over the state. Unchanged subtrees are shared
between versions rather than re-diffed, so recent versions are cheap to retain
and a patch can be computed between any two of them. Updates carry:

| field | meaning |
|---|---|
| `root` | opaque token naming the tree the client holds after applying |
| `base` | the tree `ops` applies to |
| `ops` | ordered operation list |

Operations address one child each:

| operation | meaning |
|---|---|
| `["s", path, value]` | assign; `path` targets the child |
| `["d", path]` | delete; `path` targets the child |
| `["n", path, length]` | truncate; `path` targets the array |
| `["x", path, start, delete, values?]` | splice; `path` targets the array |

Path elements are strings for object keys and numbers for array indices, so a
numeric-looking key never collides with an index. Unlike v2, changing one element
of an array does not resend the array. Arrays are only ever changed by `s`, `n`
and `x`; a `d` against an array index is invalid and both appliers reject it.

Array diffs compare the two versions' element hashes serially from both ends.
Elements that survive the trim never travel; an in-place edit diffs pairwise,
and a length change becomes one `x` — inserting or deleting anywhere in a long
array costs a single operation carrying only the affected values, where a purely
positional differ would reassign every element after the change. A pure tail
truncation stays the smaller `n`.

When the operations describing a change would still cost more than the subtree
they patch — reversing a long array, say, which defeats the trim entirely — the
differ sends the subtree instead, so v3 is never much worse than v2 even on the
shapes positional operations handle badly.

A client applies operations only when their `base` matches the root it holds.
Because hashes are opaque it cannot check anything else, and operations applied
to the wrong base often succeed. On a mismatch, or any failed operation, it
discards its document and reconnects for a full snapshot rather than carrying on
with state it knows is wrong.

Root hashes are **server-internal and opaque**: clients store them and echo them
back as `h`, and never recompute them. That keeps cross-language JSON
canonicalisation out of the protocol, and means the Go and Node servers need not
agree on a hash function.

**Resuming.** A client reconnects with `?p=3&id=…&v=…&h=…`. If the server still
retains that tree it replies with operations spanning however many versions were
missed; otherwise it sends a full snapshot. The window is bounded by
`HistoryWindow` (default 5m) and `HistoryMaxNodes`.

Browser clients can opt into persisting the document, which turns a page reload
into the same cheap resume:

```js
velox("/velox", obj, {persist: true}); // or {persist: "my-key", storage: …}
```

Writes are debounced and a quota failure disables persistence rather than
breaking the connection. A stale stored version is harmless -- it only widens the
resume patch.

**Tuning.** `MerkleLeafSize` (default 512) is the subtree size below which the
tree stores an opaque leaf rather than addressable children: larger values mean a
smaller tree and coarser patches. `Incremental` additionally lets VMap/VSlice
reuse their previous encoding when unchanged, so a push re-encodes only what
moved; it engages only for element types that cannot be mutated through a handed
-out copy, and `VerifyIncremental` re-marshals on every push to catch a stale
cache.

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
