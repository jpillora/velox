package velox

import (
	"reflect"
	"sync"
	"time"
)

// immutableTypes are types that are safe to copy despite containing a pointer,
// because nothing in their API mutates what it points at. Without this,
// deeplyImmutable's structural test rejects time.Time — it carries a
// *time.Location — and with it practically every real state struct, which would
// leave the incremental path switched off almost everywhere it matters.
var immutableTypes = map[reflect.Type]bool{
	reflect.TypeFor[time.Time]():     true,
	reflect.TypeFor[time.Month]():    true,
	reflect.TypeFor[time.Weekday]():  true,
	reflect.TypeFor[time.Duration](): true,
}

// jsonCache memoises a container's encoding so that a push does not re-encode
// subtrees that have not changed. It is what makes the marshal incremental:
// json.Marshal still walks the root struct top to bottom, but a clean container
// hands back bytes instead of re-encoding its contents.
//
// The cache is guarded by its own mutex rather than relying on the state lock.
// Mutations happen under the state's write lock and marshals under its read
// lock, so the two never interleave — but Marshal is exported and the state's
// Locker is optional, and a stale cache is a silent divergence rather than a
// crash. The mutex is uncontended in the normal path.
type jsonCache struct {
	mut       sync.Mutex
	encoded   []byte
	dirty     bool
	cacheable bool
}

// enable turns caching on for a container whose element type is safe for it.
// See deeplyImmutable for what "safe" means and why it matters.
func (c *jsonCache) enable(elem reflect.Type) {
	c.mut.Lock()
	defer c.mut.Unlock()
	c.cacheable = deeplyImmutable(elem, map[reflect.Type]bool{})
	c.dirty = true
	c.encoded = nil
}

func (c *jsonCache) markDirty() {
	c.mut.Lock()
	c.dirty = true
	c.mut.Unlock()
}

func (c *jsonCache) get() ([]byte, bool) {
	c.mut.Lock()
	defer c.mut.Unlock()
	if c.cacheable && !c.dirty && c.encoded != nil {
		return c.encoded, true
	}
	return nil, false
}

func (c *jsonCache) put(encoded []byte) {
	c.mut.Lock()
	defer c.mut.Unlock()
	if !c.cacheable {
		return
	}
	c.encoded = encoded
	c.dirty = false
}

// invalidate forces the next marshal to re-encode. It is what
// State.VerifyIncremental uses to obtain an uncached encoding to compare
// against.
func (c *jsonCache) invalidate() {
	c.markDirty()
}

// engaged reports whether this container is currently caching, which callers
// use to decide whether the extra work of keeping the cache honest is worth
// doing at all.
func (c *jsonCache) engaged() bool {
	c.mut.Lock()
	defer c.mut.Unlock()
	return c.cacheable
}

// deeplyImmutable reports whether a value of type t can be handed out by copy
// without the container losing track of mutations made through it.
//
// This is the safety gate on the whole incremental path. VMap.Get, Range,
// Values and Snapshot all hand out V by copy. If V contains a pointer, map,
// slice, interface, channel or function, the copy aliases the original and a
// caller can mutate the container's contents without any mutating method being
// called — the cache would then serve bytes that no longer describe the state,
// and clients would silently diverge. Rather than trying to detect that, the
// cache simply refuses to engage for such types.
func deeplyImmutable(t reflect.Type, seen map[reflect.Type]bool) bool {
	if t == nil {
		return false
	}
	if immutableTypes[t] {
		return true
	}
	switch t.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Interface,
		reflect.Chan, reflect.Func, reflect.UnsafePointer:
		return false
	case reflect.Array:
		return deeplyImmutable(t.Elem(), seen)
	case reflect.Struct:
		// A recursive struct can only recurse through a pointer, which is
		// already rejected, so revisiting a type mid-walk is safe to accept.
		if seen[t] {
			return true
		}
		seen[t] = true
		for i := range t.NumField() {
			if !deeplyImmutable(t.Field(i).Type, seen) {
				return false
			}
		}
		return true
	default:
		// Booleans, all numeric kinds and strings copy by value.
		return true
	}
}

// incrementalEnabler lets a container ask the pusher it is bound to whether the
// incremental marshal is switched on, without vmap/vslice depending on State.
type incrementalEnabler interface {
	incrementalEnabled() bool
}

func incrementalRequested(pusher Pusher) bool {
	enabler, ok := pusher.(incrementalEnabler)
	return ok && enabler.incrementalEnabled()
}

func (m *VMap[K, V]) jsonCacheRef() *jsonCache { return &m.cache }

func (s *VSlice[V]) jsonCacheRef() *jsonCache { return &s.cache }
