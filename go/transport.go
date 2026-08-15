package velox

import (
	"encoding/json"
	"net/http"
)

// ProtoVersion is the newest velox protocol this build speaks. A client
// advertises the version it wants with the "p" query parameter; anything older
// than 3, including its absence, is served protocol v2 unchanged.
const ProtoVersion = 3

// Update is a single message sent to the client.
//
// Protocol v2 carries either a full snapshot (Body) or an RFC 7386 merge patch
// (Body with Delta set). Protocol v3 adds Ops, an ordered operation list that
// can address array elements individually and that applies to the tree named by
// Base; Root names the tree the client holds once the update is applied, and is
// what it echoes back as "h" to resume.
type Update struct {
	ID      string          `json:"id,omitempty"`
	Ping    bool            `json:"ping,omitempty"`
	Delta   bool            `json:"delta,omitempty"`   // v2 only
	Version int64           `json:"version,omitempty"` //53 usable bits
	Proto   int             `json:"proto,omitempty"`   // v3 only, on the first update
	Root    string          `json:"root,omitempty"`    // v3 only, opaque resume token
	Base    string          `json:"base,omitempty"`    // v3 only, tree Ops applies to
	Ops     json.RawMessage `json:"ops,omitempty"`     // v3 only
	Body    json.RawMessage `json:"body,omitempty"`
}

type transport interface {
	connect(w http.ResponseWriter, r *http.Request) error
	send(upd *Update) error
	wait() error
	//drain blocks until nothing is still writing to the http.ResponseWriter,
	//so callers can let ServeHTTP return without net/http pulling the
	//connection out from under an in-flight write
	drain()
	close() error
}
