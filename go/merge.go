package velox

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
)

// mergePatcher owns the raw previous state and the merkle tree built from it.
// Building the tree is the diff: a subtree whose bytes are unchanged returns
// the previous node outright, so it is neither re-walked nor re-validated, and
// both versions go on sharing it.
//
// There is one diff engine, not two. The v2 merge patch is derived from the
// same tree as the v3 operations — only the walk differs, selected by the
// differ's arrayOps flag — so supporting both protocols costs one build and two
// O(changed) walks rather than two full side-by-side diffs. The only part of
// the original object diff that survives is rawObjectDiff, which v2 still needs
// for one case the merge patch format cannot otherwise express: replacing an
// object stored as an opaque leaf. See differ.emitReplacement.
type mergePatcher struct {
	prev     []byte
	tree     *mnode
	leafSize int
	created  int // nodes the most recent build allocated
}

func (m *mergePatcher) builder() merkleBuilder {
	size := m.leafSize
	if size <= 0 {
		size = DefaultMerkleLeafSize
	}
	return merkleBuilder{leafSize: size}
}

// build derives the tree for modifiedJSON without publishing anything. A null
// state has no tree; every other state must be a JSON object, which the object
// scan enforces.
func (m *mergePatcher) build(modifiedJSON []byte) (*mnode, int, error) {
	if isJSONNull(modifiedJSON) {
		return nil, 0, nil
	}
	if err := checkJSONRootObject(modifiedJSON); err != nil {
		return nil, 0, err
	}
	builder := m.builder()
	// The root is always expanded, however small it is, so that top-level keys
	// stay individually addressable.
	root, err := builder.buildObject(m.tree, m.prev, modifiedJSON)
	return root, builder.created, err
}

// update refreshes the tree from modifiedJSON and returns the operations that
// carry the previous state to it. The cache is published only after every
// fallible step has succeeded, so a rejected state leaves the patcher untouched.
func (m *mergePatcher) update(modifiedJSON []byte, arrayOps bool) (root *mnode, ops []op, err error) {
	if m.prev != nil && bytes.Equal(m.prev, modifiedJSON) {
		return m.tree, nil, nil
	}
	root, created, err := m.build(modifiedJSON)
	if err != nil {
		return nil, nil, err
	}
	if m.prev != nil {
		if ops, err = diffTrees(m.tree, root, arrayOps); err != nil {
			return nil, nil, err
		}
	}
	// The caller may reuse its marshal buffer, so the snapshot is cloned. Leaves
	// own their bytes too, leaving the tree independent of both buffers.
	//
	// This clone is the state's one authoritative snapshot: State.data.bytes
	// aliases it rather than taking a second copy. Both are immutable once
	// published, so the sharing is safe, and it keeps a push from allocating and
	// copying the whole document twice over.
	m.prev = bytes.Clone(modifiedJSON)
	m.tree = root
	m.created = created
	return root, ops, nil
}

// snapshot returns the published state's bytes. Callers must treat it as
// immutable: State.data.bytes aliases it instead of holding a second copy.
func (m *mergePatcher) snapshot() []byte {
	return m.prev
}

// patch computes an RFC 7386 merge patch from cached previous state to
// modifiedJSON. It returns the patch bytes and updates the cache.
func (m *mergePatcher) patch(modifiedJSON []byte) ([]byte, error) {
	_, ops, err := m.update(modifiedJSON, false)
	if err != nil {
		return nil, err
	}
	return mergePatchFromOps(ops)
}

type rawObjectMap map[string]json.RawMessage

// rawObject indexes one object level with views into data. patch keeps both
// backing documents alive until the resulting patch has been marshaled.
func rawObject(data []byte) (rawObjectMap, error) {
	i := skipJSONSpace(data, 0)
	if i < len(data) && data[i] == 'n' {
		return nil, nil
	}
	if i >= len(data) || data[i] != '{' {
		return nil, errors.New("JSON state must be an object or null")
	}
	i = skipJSONSpace(data, i+1)
	object := make(rawObjectMap)
	if i < len(data) && data[i] == '}' {
		return object, nil
	}
	for {
		keyStart := i
		keyEnd := scanJSONString(data, keyStart)
		var key string
		if err := json.Unmarshal(data[keyStart:keyEnd], &key); err != nil {
			return nil, err
		}
		i = skipJSONSpace(data, keyEnd)
		if i >= len(data) || data[i] != ':' {
			return nil, errors.New("invalid JSON object")
		}
		valueStart := skipJSONSpace(data, i+1)
		valueEnd := scanJSONValue(data, valueStart)
		// An empty span means there was no value at all, as in {"a":,"b":1}.
		// Nothing downstream would catch it: the span is only validated when it
		// reaches a leaf, and a later duplicate of the same key overwrites it
		// first, so the malformed document would be published as if it were
		// whatever the duplicate said.
		if valueEnd == valueStart {
			return nil, errors.New("invalid JSON object")
		}
		// Duplicate keys resolve last-wins, matching encoding/json. The value
		// being dropped still has to be well-formed, though: it is about to
		// become unreachable, and validation only happens where a span comes to
		// rest, so {"a":A,"a":0} would otherwise be published as {"a":0}.
		// Duplicates do not occur in encoding/json's own output, so this costs
		// nothing on the normal path.
		if discarded, ok := object[key]; ok && !json.Valid(discarded) {
			return nil, errors.New("invalid JSON object")
		}
		object[key] = data[valueStart:valueEnd]
		i = skipJSONSpace(data, valueEnd)
		if i < len(data) && data[i] == '}' {
			return object, nil
		}
		if i >= len(data) || data[i] != ',' {
			return nil, errors.New("invalid JSON object")
		}
		i = skipJSONSpace(data, i+1)
	}
}

// rawObjectDiff returns an RFC 7386 merge patch. Raw values are decoded only
// when their bytes differ; object values recurse and arrays compare as a whole.
func rawObjectDiff(a, b rawObjectMap) (map[string]json.RawMessage, error) {
	var diff map[string]json.RawMessage
	for key, bv := range b {
		av, ok := a[key]
		if !ok {
			if diff == nil {
				diff = make(map[string]json.RawMessage)
			}
			diff[key] = bv
			continue
		}

		equal, sub, err := rawValueDiff(av, bv)
		if err != nil {
			return nil, err
		}
		if equal {
			continue
		}
		if diff == nil {
			diff = make(map[string]json.RawMessage)
		}
		if sub != nil {
			diff[key] = sub
		} else {
			diff[key] = bv
		}
	}
	for key := range a {
		if _, ok := b[key]; ok {
			continue
		}
		if diff == nil {
			diff = make(map[string]json.RawMessage)
		}
		diff[key] = json.RawMessage(`null`)
	}
	return diff, nil
}

// rawValueDiff reports semantic equality and, for changed object values, the
// encoded recursive patch. A nil patch means the new raw value replaces old.
func rawValueDiff(a, b json.RawMessage) (equal bool, patch json.RawMessage, err error) {
	if bytes.Equal(a, b) {
		return true, nil, nil
	}

	aObject := firstJSONByte(a) == '{'
	bObject := firstJSONByte(b) == '{'
	if aObject && bObject {
		aMap, err := rawObject(a)
		if err != nil {
			return false, nil, err
		}
		bMap, err := rawObject(b)
		if err != nil {
			return false, nil, err
		}
		diff, err := rawObjectDiff(aMap, bMap)
		if err != nil || len(diff) == 0 {
			return len(diff) == 0, nil, err
		}
		encoded, err := json.Marshal(diff)
		return false, encoded, err
	}
	if aObject != bObject {
		return false, nil, nil
	}

	// This path covers scalars and arrays. Decoding preserves encoding/json's
	// float64 comparison behavior, including equality of 1 and 1.0.
	var av, bv interface{}
	if err := json.Unmarshal(a, &av); err != nil {
		return false, nil, err
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		return false, nil, err
	}
	return valueEqual(av, bv), nil, nil
}

func firstJSONByte(data []byte) byte {
	for _, c := range data {
		switch c {
		case ' ', '\t', '\r', '\n':
			continue
		default:
			return c
		}
	}
	return 0
}

func skipJSONSpace(data []byte, i int) int {
	for i < len(data) {
		switch data[i] {
		case ' ', '\t', '\r', '\n':
			i++
		default:
			return i
		}
	}
	return i
}

// scanJSONString returns the offset just past the string opening at start.
// Locating the closing quote with IndexByte lets the string body be skipped a
// word at a time rather than a byte at a time, which dominates scanning any
// document whose bulk is string data. A quote preceded by an odd number of
// backslashes is escaped and does not close the string.
func scanJSONString(data []byte, start int) int {
	for i := start + 1; i <= len(data); {
		offset := bytes.IndexByte(data[i:], '"')
		if offset < 0 {
			return len(data)
		}
		quote := i + offset
		backslashes := 0
		for k := quote - 1; k > start && data[k] == '\\'; k-- {
			backslashes++
		}
		if backslashes%2 == 0 {
			return quote + 1
		}
		i = quote + 1
	}
	return len(data)
}

func scanJSONValue(data []byte, start int) int {
	if start >= len(data) {
		return start
	}
	switch data[start] {
	case '"':
		return scanJSONString(data, start)
	case '{', '[':
		depth := 0
		for i := start; i < len(data); i++ {
			switch data[i] {
			case '"':
				i = scanJSONString(data, i) - 1
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return i + 1
				}
			}
		}
		return len(data)
	default:
		for i := start; i < len(data); i++ {
			switch data[i] {
			case ',', '}', ']', ' ', '\t', '\r', '\n':
				return i
			}
		}
		return len(data)
	}
}

// validateJSONNumbers preserves encoding/json's float64 range checks without
// decoding the surrounding document. Syntax has already been checked.
func validateJSONNumbers(data []byte) error {
	for i := 0; i < len(data); {
		switch data[i] {
		case '"':
			i = scanJSONString(data, i)
		case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
			start := i
			for i < len(data) && !isJSONNumberDelimiter(data[i]) {
				i++
			}
			// json.Valid has already ruled out syntax errors. ParseFloat can
			// therefore fail here only when a finite JSON number overflows
			// encoding/json's float64 representation.
			if _, err := strconv.ParseFloat(string(data[start:i]), 64); err != nil {
				return err
			}
		default:
			i++
		}
	}
	return nil
}

func isJSONNumberDelimiter(c byte) bool {
	switch c {
	case ',', '}', ']', ' ', '\t', '\r', '\n':
		return true
	default:
		return false
	}
}

func sliceEqual(a, b []interface{}) bool {
	if len(a) != len(b) {
		return false
	}
	if (a == nil) != (b == nil) {
		return false
	}
	for i := range a {
		if !valueEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}

// mergeObjects applies patch onto doc in-place per RFC 7386:
// null values delete keys, objects merge recursively, all else replaces.
func mergeObjects(doc, patch map[string]interface{}) {
	for key, pv := range patch {
		if pv == nil {
			delete(doc, key)
			continue
		}
		// if both sides are objects, merge recursively
		if pObj, ok := pv.(map[string]interface{}); ok {
			if dObj, ok := doc[key].(map[string]interface{}); ok {
				mergeObjects(dObj, pObj)
				continue
			}
		}
		doc[key] = pv
	}
}

func valueEqual(a, b interface{}) bool {
	if reflect.TypeOf(a) != reflect.TypeOf(b) {
		return false
	}
	switch at := a.(type) {
	case map[string]interface{}:
		bt := b.(map[string]interface{})
		if len(at) != len(bt) {
			return false
		}
		for k, av := range at {
			bv, ok := bt[k]
			if !ok || !valueEqual(av, bv) {
				return false
			}
		}
		return true
	case []interface{}:
		return sliceEqual(at, b.([]interface{}))
	case nil:
		return b == nil
	default:
		return a == b
	}
}
