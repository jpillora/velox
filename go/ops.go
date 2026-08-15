package velox

import (
	"encoding/json"
	"errors"
	"strconv"
)

// Operation kinds carried by a protocol v3 patch. Operations are ordered and
// must be applied in sequence.
const (
	opSet = "s" // ["s", path, value] — path targets the child being assigned
	opDel = "d" // ["d", path]        — path targets the child being removed
	opLen = "n" // ["n", path, len]   — path targets the array being truncated
)

// op is one entry of a v3 patch. Path elements are strings for object keys and
// ints for array indices, so a numeric-looking object key never collides with
// an index.
type op struct {
	kind   string
	path   []any
	value  json.RawMessage
	length int
}

// MarshalJSON encodes the operation as a positional array, which is
// meaningfully smaller on the wire than an object with named fields.
func (o op) MarshalJSON() ([]byte, error) {
	out := []byte{'['}
	out = strconv.AppendQuote(out, o.kind)
	out = append(out, ',')
	encodedPath, err := json.Marshal(o.path)
	if err != nil {
		return nil, err
	}
	out = append(out, encodedPath...)
	switch o.kind {
	case opSet:
		out = append(out, ',')
		out = append(out, o.value...)
	case opLen:
		out = append(out, ',')
		out = strconv.AppendInt(out, int64(o.length), 10)
	}
	return append(out, ']'), nil
}

// UnmarshalJSON decodes the positional array form. JSON has one number type, so
// path elements arrive as float64 and array indices are narrowed back to int —
// which is also what distinguishes them from string object keys downstream.
func (o *op) UnmarshalJSON(data []byte) error {
	var fields []json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if len(fields) < 2 {
		return errors.New("velox: malformed operation")
	}
	if err := json.Unmarshal(fields[0], &o.kind); err != nil {
		return err
	}
	var path []any
	if err := json.Unmarshal(fields[1], &path); err != nil {
		return err
	}
	o.path = make([]any, len(path))
	for i, elem := range path {
		switch value := elem.(type) {
		case string:
			o.path[i] = value
		case float64:
			index := int(value)
			if float64(index) != value || index < 0 {
				return errors.New("velox: invalid array index in path")
			}
			o.path[i] = index
		default:
			return errors.New("velox: invalid path element")
		}
	}
	switch o.kind {
	case opSet:
		if len(fields) < 3 {
			return errors.New("velox: set operation has no value")
		}
		o.value = fields[2]
	case opLen:
		if len(fields) < 3 {
			return errors.New("velox: truncate operation has no length")
		}
		if err := json.Unmarshal(fields[2], &o.length); err != nil {
			return err
		}
	case opDel:
	default:
		return errors.New("velox: unknown operation " + o.kind)
	}
	return nil
}

// errRootReplaced reports that the two trees disagree at the root, so no patch
// can express the transition and the caller must send a full snapshot.
var errRootReplaced = errors.New("velox: root replaced")

// differ walks two merkle trees and emits the operations that turn the first
// into the second. Cost is proportional to what changed, not to document size:
// a subtree shared by both trees is dismissed on pointer identity.
//
// arrayOps selects the granularity. With it set, arrays diff per index, which
// is what protocol v3 carries. With it clear, any change inside an array
// collapses to a single whole-array assignment, which is all an RFC 7386 merge
// patch can express.
type differ struct {
	arrayOps bool
	path     []any
	ops      []op
}

func (d *differ) emit(o op) {
	o.path = append([]any(nil), d.path...)
	d.ops = append(d.ops, o)
}

func (d *differ) emitSet(node *mnode) {
	d.emit(op{kind: opSet, value: node.appendJSON(nil)})
}

// emitReplacement assigns b over a. A v3 "s" operation is a true assignment,
// so it can just carry b. An RFC 7386 merge patch cannot replace an object
// wholesale — placing one there merges it, stranding the keys a had and b does
// not — so whenever both sides are objects, v2 has to carry a diff instead.
func (d *differ) emitReplacement(a, b *mnode) error {
	if d.arrayOps || a == nil {
		d.emitSet(b)
		return nil
	}
	previous, current := a.appendJSON(nil), b.appendJSON(nil)
	if firstJSONByte(previous) != '{' || firstJSONByte(current) != '{' {
		d.emitSet(b)
		return nil
	}
	previousObject, err := rawObject(previous)
	if err != nil {
		return err
	}
	currentObject, err := rawObject(current)
	if err != nil {
		return err
	}
	fragment, err := rawObjectDiff(previousObject, currentObject)
	if err != nil {
		return err
	}
	if len(fragment) == 0 {
		return nil
	}
	encoded, err := json.Marshal(fragment)
	if err != nil {
		return err
	}
	d.emit(op{kind: opSet, value: encoded})
	return nil
}

func (d *differ) push(elem any) { d.path = append(d.path, elem) }
func (d *differ) pop()          { d.path = d.path[:len(d.path)-1] }

// diffTrees returns the operations turning tree a into tree b. A nil root is a
// null state, which holds no keys; going from one to the other is expressed as
// adding or removing every top-level key rather than as a root replacement.
func diffTrees(a, b *mnode, arrayOps bool) ([]op, error) {
	if a == nil && b == nil {
		return nil, nil
	}
	d := &differ{arrayOps: arrayOps}
	switch {
	case a == nil:
		for i, key := range b.keys {
			d.push(key)
			d.emitSet(b.kids[i])
			d.pop()
		}
	case b == nil:
		for _, key := range a.keys {
			d.push(key)
			d.emit(op{kind: opDel})
			d.pop()
		}
	default:
		if a.kind != b.kind {
			return nil, errRootReplaced
		}
		if err := d.walk(a, b); err != nil {
			return nil, err
		}
	}
	return d.ops, nil
}

func (d *differ) walk(a, b *mnode) error {
	// Pointer identity means the subtree was shared when b was built, and hash
	// equality covers subtrees that were rebuilt to the same bytes.
	if a == b || a.hash == b.hash {
		return nil
	}
	if a.kind != b.kind {
		return d.emitReplacement(a, b)
	}
	switch b.kind {
	case kindLeaf:
		// Hashes cover raw bytes, so two encodings of the same value land here.
		// Fall back to the value equality the merge patcher has always used.
		equal, err := rawSemanticEqual(a.raw, b.raw)
		if err != nil {
			return err
		}
		if !equal {
			return d.emitReplacement(a, b)
		}
		return nil
	case kindObject:
		return d.walkObject(a, b)
	default:
		return d.walkArray(a, b)
	}
}

func (d *differ) walkObject(a, b *mnode) error {
	for _, key := range a.keys {
		if b.kidByKey(key) == nil {
			d.push(key)
			d.emit(op{kind: opDel})
			d.pop()
		}
	}
	for i, key := range b.keys {
		previous := a.kidByKey(key)
		d.push(key)
		if previous == nil {
			d.emitSet(b.kids[i])
		} else if err := d.walk(previous, b.kids[i]); err != nil {
			d.pop()
			return err
		}
		d.pop()
	}
	return nil
}

func (d *differ) walkArray(a, b *mnode) error {
	// Without per-index operations, recurse only to decide whether anything
	// changed, then discard those operations in favour of one assignment of the
	// whole array.
	savepoint := len(d.ops)

	if d.arrayOps && len(b.kids) < len(a.kids) {
		d.emit(op{kind: opLen, length: len(b.kids)})
	}
	for i, kid := range b.kids {
		d.push(i)
		if i < len(a.kids) {
			if err := d.walk(a.kids[i], kid); err != nil {
				d.pop()
				return err
			}
		} else {
			d.emitSet(kid)
		}
		d.pop()
	}
	if !d.arrayOps && (len(d.ops) > savepoint || len(a.kids) != len(b.kids)) {
		d.ops = d.ops[:savepoint]
		d.emitSet(b)
	}
	return nil
}

// mergePatchFromOps projects operations back into an RFC 7386 merge patch for
// protocol v2 clients. It is only ever handed operations produced with
// arrayOps clear, so every path element is an object key.
func mergePatchFromOps(ops []op) (json.RawMessage, error) {
	if len(ops) == 0 {
		return json.RawMessage(`{}`), nil
	}
	root := map[string]any{}
	for _, o := range ops {
		node := root
		for _, elem := range o.path[:len(o.path)-1] {
			key, ok := elem.(string)
			if !ok {
				return nil, errors.New("velox: array index in merge patch path")
			}
			child, ok := node[key].(map[string]any)
			if !ok {
				child = map[string]any{}
				node[key] = child
			}
			node = child
		}
		last, ok := o.path[len(o.path)-1].(string)
		if !ok {
			return nil, errors.New("velox: array index in merge patch path")
		}
		switch o.kind {
		case opDel:
			node[last] = nil
		case opSet:
			node[last] = o.value
		default:
			return nil, errors.New("velox: array operation in merge patch")
		}
	}
	return json.Marshal(root)
}

// applyOps applies a v3 patch to a decoded document, returning the updated
// document. It is the reference implementation the fuzz test checks the encoder
// against, and it is what the Go client uses to maintain its cached state.
func applyOps(doc any, ops []op) (any, error) {
	for _, o := range ops {
		updated, err := applyOp(doc, o.path, o)
		if err != nil {
			return nil, err
		}
		doc = updated
	}
	return doc, nil
}

// applyOp returns node with the operation applied somewhere beneath it.
// Containers are rebuilt on the way out because appending to a grown array
// reallocates, and the new slice header has to reach the parent's slot.
func applyOp(node any, path []any, o op) (any, error) {
	if len(path) == 0 {
		return applyHere(node, o)
	}
	switch key := path[0].(type) {
	case string:
		object, ok := node.(map[string]any)
		if !ok {
			return nil, errors.New("velox: object key against a non-object")
		}
		if len(path) == 1 && o.kind == opDel {
			delete(object, key)
			return object, nil
		}
		child, err := applyOp(object[key], path[1:], o)
		if err != nil {
			return nil, err
		}
		object[key] = child
		return object, nil
	case int:
		elements, ok := node.([]any)
		if !ok {
			return nil, errors.New("velox: array index against a non-array")
		}
		// One past the end is how a grown array is expressed. Operations are
		// emitted in ascending index order, so the slot is always reachable.
		if len(path) == 1 && o.kind == opSet && key == len(elements) {
			var value any
			if err := json.Unmarshal(o.value, &value); err != nil {
				return nil, err
			}
			return append(elements, value), nil
		}
		if key < 0 || key >= len(elements) {
			return nil, errors.New("velox: array index out of range")
		}
		child, err := applyOp(elements[key], path[1:], o)
		if err != nil {
			return nil, err
		}
		elements[key] = child
		return elements, nil
	default:
		return nil, errors.New("velox: invalid path element")
	}
}

// applyHere applies an operation to the node its path arrived at.
func applyHere(node any, o op) (any, error) {
	switch o.kind {
	case opSet:
		var value any
		if err := json.Unmarshal(o.value, &value); err != nil {
			return nil, err
		}
		return value, nil
	case opLen:
		elements, ok := node.([]any)
		if !ok || o.length > len(elements) {
			return nil, errors.New("velox: truncate outside an array")
		}
		return elements[:o.length], nil
	default:
		return nil, errors.New("velox: unsupported operation " + o.kind)
	}
}
