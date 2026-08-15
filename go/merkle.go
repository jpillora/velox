package velox

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"slices"
)

// DefaultMerkleLeafSize is the default State.MerkleLeafSize. Subtrees encoding
// to fewer bytes than this are stored as opaque leaves rather than being broken
// out into nodes, which keeps node count proportional to the document's spine
// instead of to every scalar in it. Raising it shrinks the tree and coarsens
// patches; lowering it does the reverse.
const DefaultMerkleLeafSize = 512

type mkind uint8

const (
	kindLeaf mkind = iota
	kindObject
	kindArray
)

// mnode is one node of a state's merkle tree.
//
// Nodes are immutable once built, so a node reachable from two roots really is
// shared by both versions: retaining an old root costs only the nodes along the
// paths that have changed since. Interior nodes deliberately hold no view into
// the snapshot they were parsed from, so retaining a root pins no snapshot
// buffer; leaves own a copy of their bytes instead.
type mnode struct {
	hash [16]byte
	kind mkind
	raw  []byte   // leaf only: owned copy of the encoded value
	keys []string // object only, sorted so key order cannot affect the hash
	kids []*mnode // object and array only
}

func (n *mnode) kidByKey(key string) *mnode {
	if n == nil || n.kind != kindObject {
		return nil
	}
	if i, ok := slices.BinarySearch(n.keys, key); ok {
		return n.kids[i]
	}
	return nil
}

// appendJSON reconstructs the node's encoded form. Interior nodes store no
// bytes, so this walks their children; it is only called to emit a whole
// subtree as an operation value, which is bounded by the size of the patch.
func (n *mnode) appendJSON(dst []byte) []byte {
	switch n.kind {
	case kindLeaf:
		return append(dst, n.raw...)
	case kindArray:
		dst = append(dst, '[')
		for i, kid := range n.kids {
			if i > 0 {
				dst = append(dst, ',')
			}
			dst = kid.appendJSON(dst)
		}
		return append(dst, ']')
	default:
		dst = append(dst, '{')
		for i, key := range n.keys {
			if i > 0 {
				dst = append(dst, ',')
			}
			encoded, err := json.Marshal(key)
			if err != nil {
				// Keys come from a scanned JSON document, so they are already
				// valid UTF-8 strings and this cannot fail in practice.
				encoded = []byte(`""`)
			}
			dst = append(dst, encoded...)
			dst = append(dst, ':')
			dst = n.kids[i].appendJSON(dst)
		}
		return append(dst, '}')
	}
}

// rehash derives the node's hash from its children's hashes. The encoding is
// server-internal — clients store root hashes as opaque resume tokens and never
// recompute them — so it is free to be whatever is cheapest here.
func (n *mnode) rehash() {
	digest := sha256.New()
	var scratch [binary.MaxVarintLen64]byte
	writeUvarint := func(v uint64) {
		digest.Write(scratch[:binary.PutUvarint(scratch[:], v)])
	}
	switch n.kind {
	case kindLeaf:
		digest.Write([]byte{0x00})
		digest.Write(n.raw)
	case kindArray:
		digest.Write([]byte{0x01})
		writeUvarint(uint64(len(n.kids)))
		for _, kid := range n.kids {
			digest.Write(kid.hash[:])
		}
	default:
		digest.Write([]byte{0x02})
		writeUvarint(uint64(len(n.keys)))
		for i, key := range n.keys {
			writeUvarint(uint64(len(key)))
			digest.Write([]byte(key))
			digest.Write(n.kids[i].hash[:])
		}
	}
	copy(n.hash[:], digest.Sum(nil))
}

// checkJSONRootObject rejects anything that is not exactly one JSON object.
// The per-leaf checks during the build cover everything inside the document,
// but only the root can have leading or trailing junk around it, and rawObject
// is deliberately lenient about a leading 'n' so that null reaches its caller.
func checkJSONRootObject(data []byte) error {
	start := skipJSONSpace(data, 0)
	if start >= len(data) || data[start] != '{' {
		return errors.New("JSON state must be an object or null")
	}
	if skipJSONSpace(data, scanJSONValue(data, start)) != len(data) {
		return errors.New("invalid JSON state")
	}
	return nil
}

// objectLevel is one decoded object level: sorted keys with duplicates already
// resolved last-wins, matching encoding/json's own decoding behaviour.
type objectLevel struct {
	keys   []string
	values map[string]json.RawMessage
}

func scanObjectLevel(data []byte) (*objectLevel, error) {
	values, err := rawObject(data)
	if err != nil {
		return nil, err
	}
	level := &objectLevel{keys: make([]string, 0, len(values)), values: values}
	for key := range values {
		level.keys = append(level.keys, key)
	}
	slices.Sort(level.keys)
	return level, nil
}

// scanArrayLevel splits one array level into its elements without decoding them.
func scanArrayLevel(data []byte) ([]json.RawMessage, error) {
	i := skipJSONSpace(data, 0)
	if i >= len(data) || data[i] != '[' {
		return nil, errors.New("invalid JSON array")
	}
	i = skipJSONSpace(data, i+1)
	if i < len(data) && data[i] == ']' {
		return []json.RawMessage{}, nil
	}
	var elements []json.RawMessage
	for {
		if i >= len(data) {
			return nil, errors.New("invalid JSON array")
		}
		end := scanJSONValue(data, i)
		elements = append(elements, data[i:end])
		i = skipJSONSpace(data, end)
		if i < len(data) && data[i] == ']' {
			return elements, nil
		}
		if i >= len(data) || data[i] != ',' {
			return nil, errors.New("invalid JSON array")
		}
		i = skipJSONSpace(data, i+1)
	}
}

// merkleBuilder builds a new tree against the previous one. Building is the
// diff: a subtree whose bytes are unchanged returns the previous node outright,
// costing no allocation and no hashing, and is thereafter shared by both
// versions.
type merkleBuilder struct {
	leafSize int
	created  int // nodes allocated this build, used as a history eviction credit
}

func (b *merkleBuilder) build(prev *mnode, prevRaw, newRaw json.RawMessage) (*mnode, error) {
	if prev != nil && prevRaw != nil && bytes.Equal(prevRaw, newRaw) {
		return prev, nil
	}
	if len(newRaw) >= b.leafSize {
		switch firstJSONByte(newRaw) {
		case '{':
			return b.buildObject(prev, prevRaw, newRaw)
		case '[':
			return b.buildArray(prev, prevRaw, newRaw)
		}
	}
	return b.buildLeaf(prev, newRaw)
}

// buildLeaf validates only the bytes it is handed. Bytes that matched the
// previous snapshot never reach here, so number-range validation covers the
// whole document across pushes while only ever scanning what changed.
func (b *merkleBuilder) buildLeaf(prev *mnode, newRaw json.RawMessage) (*mnode, error) {
	if !json.Valid(newRaw) {
		return nil, errors.New("invalid JSON state")
	}
	if err := validateJSONNumbers(newRaw); err != nil {
		return nil, err
	}
	if prev != nil && prev.kind == kindLeaf {
		equal, err := rawSemanticEqual(prev.raw, newRaw)
		if err != nil {
			return nil, err
		}
		if equal {
			return prev, nil
		}
	}
	node := &mnode{kind: kindLeaf, raw: bytes.Clone(newRaw)}
	node.rehash()
	b.created++
	return node, nil
}

func (b *merkleBuilder) buildObject(prev *mnode, prevRaw, newRaw json.RawMessage) (*mnode, error) {
	level, err := scanObjectLevel(newRaw)
	if err != nil {
		return nil, err
	}
	prevIsObject := prev != nil && prev.kind == kindObject
	var prevLevel *objectLevel
	if prevIsObject && prevRaw != nil {
		if prevLevel, err = scanObjectLevel(prevRaw); err != nil {
			return nil, err
		}
	}

	node := &mnode{kind: kindObject, keys: level.keys, kids: make([]*mnode, len(level.keys))}
	shared := prevIsObject && len(prev.keys) == len(level.keys)
	for i, key := range level.keys {
		var prevKidRaw json.RawMessage
		if prevLevel != nil {
			prevKidRaw = prevLevel.values[key]
		}
		kid, err := b.build(prev.kidByKey(key), prevKidRaw, level.values[key])
		if err != nil {
			return nil, err
		}
		node.kids[i] = kid
		if shared && (prev.keys[i] != key || prev.kids[i] != kid) {
			shared = false
		}
	}
	if shared {
		return prev, nil
	}
	node.rehash()
	b.created++
	return node, nil
}

func (b *merkleBuilder) buildArray(prev *mnode, prevRaw, newRaw json.RawMessage) (*mnode, error) {
	elements, err := scanArrayLevel(newRaw)
	if err != nil {
		return nil, err
	}
	prevIsArray := prev != nil && prev.kind == kindArray
	var prevElements []json.RawMessage
	if prevIsArray && prevRaw != nil {
		if prevElements, err = scanArrayLevel(prevRaw); err != nil {
			return nil, err
		}
	}

	node := &mnode{kind: kindArray, kids: make([]*mnode, len(elements))}
	shared := prevIsArray && len(prev.kids) == len(elements)
	for i, element := range elements {
		var prevKid *mnode
		var prevKidRaw json.RawMessage
		if prevIsArray && i < len(prev.kids) {
			prevKid = prev.kids[i]
		}
		if i < len(prevElements) {
			prevKidRaw = prevElements[i]
		}
		kid, err := b.build(prevKid, prevKidRaw, element)
		if err != nil {
			return nil, err
		}
		node.kids[i] = kid
		if shared && prev.kids[i] != kid {
			shared = false
		}
	}
	if shared {
		return prev, nil
	}
	node.rehash()
	b.created++
	return node, nil
}

// rawSemanticEqual preserves the equality the merge patcher has always used:
// encoding/json's float64 comparison, so 1 and 1.0 (and -0 and 0.0) are equal,
// and key order, whitespace, string escaping and duplicate keys are ignored.
func rawSemanticEqual(a, b json.RawMessage) (bool, error) {
	if bytes.Equal(a, b) {
		return true, nil
	}
	var av, bv interface{}
	if err := json.Unmarshal(a, &av); err != nil {
		return false, err
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		return false, err
	}
	return valueEqual(av, bv), nil
}
