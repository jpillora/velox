package velox

import (
	"encoding/hex"
	"time"
)

var (
	// DefaultHistoryWindow is how far back a client may resume from. It is a
	// duration rather than a version count because the useful bound is
	// wall-clock: a page reload a minute later should still resume, and a busy
	// state can publish dozens of versions a second.
	DefaultHistoryWindow = 5 * time.Minute
	// DefaultHistoryMaxNodes caps the nodes retained by the history so that a
	// state churning large subtrees cannot grow it without bound.
	DefaultHistoryMaxNodes = 1 << 20
)

// stateVersion is one published version of the state.
type stateVersion struct {
	version int64
	root    *mnode
	hash    string
	created int // nodes this version added, used as its eviction credit
	at      time.Time
}

// versionHistory retains recent roots so a client that has fallen behind,
// reconnected, or reloaded the page can be sent a patch instead of the whole
// document. Unchanged subtrees are shared between roots, so a retained version
// costs only the nodes along the paths that changed to reach it.
type versionHistory struct {
	entries  []stateVersion // oldest first
	nodes    int
	window   time.Duration
	maxNodes int
}

func newVersionHistory(window time.Duration, maxNodes int) *versionHistory {
	if window <= 0 {
		window = DefaultHistoryWindow
	}
	if maxNodes <= 0 {
		maxNodes = DefaultHistoryMaxNodes
	}
	return &versionHistory{window: window, maxNodes: maxNodes}
}

// rootHash renders a root as the opaque token clients echo back. Hashes are
// server-internal: clients store them and never recompute them, which is what
// keeps JSON canonicalisation out of the protocol entirely.
func rootHash(root *mnode) string {
	if root == nil {
		return ""
	}
	return hex.EncodeToString(root.hash[:])
}

func (h *versionHistory) record(version int64, root *mnode, created int, now time.Time) {
	hash := rootHash(root)
	if hash == "" {
		// A null state holds no keys, so there is nothing for a later client to
		// resume against; it is always served as a full clear.
		return
	}
	h.entries = append(h.entries, stateVersion{
		version: version,
		root:    root,
		hash:    hash,
		created: created,
		at:      now,
	})
	h.nodes += created
	h.evict(now)
}

// evict drops the oldest versions once they age out or the node budget is
// exceeded. The newest is always kept, since it is the state itself.
func (h *versionHistory) evict(now time.Time) {
	for len(h.entries) > 1 {
		oldest := h.entries[0]
		if now.Sub(oldest.at) <= h.window && h.nodes <= h.maxNodes {
			return
		}
		h.nodes -= oldest.created
		h.entries[0] = stateVersion{}
		h.entries = h.entries[1:]
	}
}

// find returns the retained root for an opaque hash.
func (h *versionHistory) find(hash string) (*mnode, bool) {
	if hash == "" {
		return nil, false
	}
	for i := range h.entries {
		if h.entries[i].hash == hash {
			return h.entries[i].root, true
		}
	}
	return nil, false
}

func (h *versionHistory) len() int { return len(h.entries) }
