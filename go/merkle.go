package velox

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"unicode/utf8"
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
	// size is the encoded length of the bytes this node was built from. A node
	// reused for a semantically-equal but differently-encoded subtree keeps its
	// own length, so this is within a cosmetic difference of the current
	// encoding — which is all the differ's send-the-smaller decision needs. It
	// also sizes the buffer when a subtree is re-encoded as an operation value.
	size int
	raw  []byte   // leaf only: owned copy of the encoded value
	keys []string // object only, sorted so key order cannot affect the hash
	kids []*mnode // object and array only
	// order records an object level's document order as (sorted-index,
	// quoted-key-byte-length) pairs, which is what lets the next build
	// reconstruct this level's spans without rescanning the snapshot. It is
	// nil when duplicate keys were seen, whose collapsed map no longer mirrors
	// the document's physical layout. The hash deliberately ignores it.
	order []uint32
}

// separators counts the commas joining n children.
func separators(n int) int {
	if n <= 1 {
		return 0
	}
	return n - 1
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

// merkleBuilder builds a new tree against the previous one, working in
// absolute offsets over the two snapshots' buffers. Building is the diff: a
// subtree whose bytes are unchanged returns the previous node outright,
// costing no allocation and no hashing, and is thereafter shared by both
// versions. With a window set, subtrees provably outside the changed byte
// range reuse by offset arithmetic alone.
type merkleBuilder struct {
	leafSize int
	created  int // nodes allocated this build, used as a history eviction credit
	prevBuf  []byte
	newBuf   []byte
	win      *diffWindow // nil scans everything
	// inexact records that a leaf was reused on semantic rather than byte
	// equality, leaving its recorded size out of step with the snapshot; span
	// arithmetic is then off the table for every later build over this tree.
	inexact bool
}

// build constructs the node for newBuf[na:nb) against prev at prevBuf[pa:pb),
// pa < 0 meaning no previous bytes. speculative marks a span that is an
// arithmetic guess: it must be proven, never plain-scanned.
func (bld *merkleBuilder) build(prev *mnode, pa, pb, na, nb int, speculative bool) (*mnode, error) {
	if prev != nil && pa >= 0 &&
		(bld.win.reusableOutsideWindow(pa, pb, na, nb) ||
			(pb-pa == nb-na && bytes.Equal(bld.prevBuf[pa:pb], bld.newBuf[na:nb]))) {
		return prev, nil
	}
	if nb-na >= bld.leafSize {
		switch bld.newBuf[na] {
		case '{':
			return bld.buildObject(prev, pa, pb, na, nb, speculative)
		case '[':
			return bld.buildArray(prev, pa, pb, na, nb, speculative)
		}
	}
	return bld.buildLeaf(prev, na, nb, speculative)
}

// buildLeaf validates only the bytes it is handed. Bytes that matched the
// previous snapshot never reach here, so number-range validation covers the
// whole document across pushes while only ever scanning what changed.
func (bld *merkleBuilder) buildLeaf(prev *mnode, na, nb int, speculative bool) (*mnode, error) {
	// a guessed span is only a leaf if its bytes really are one value ending
	// exactly where the guess says — this scan is the speculation's proof, and
	// it is small because leaves sit below the threshold
	if speculative && scanJSONValue(bld.newBuf, na) != nb {
		return nil, errWindowAbort
	}
	newRaw := json.RawMessage(bld.newBuf[na:nb])
	if err := validateJSONLeaf(newRaw); err != nil {
		if speculative {
			return nil, errWindowAbort
		}
		return nil, err
	}
	if prev != nil && prev.kind == kindLeaf {
		equal, err := rawSemanticEqual(prev.raw, newRaw)
		if err != nil {
			if speculative {
				return nil, errWindowAbort
			}
			return nil, err
		}
		if equal {
			if !bytes.Equal(prev.raw, newRaw) {
				bld.inexact = true
			}
			return prev, nil
		}
	}
	node := &mnode{kind: kindLeaf, raw: bytes.Clone(newRaw), size: nb - na}
	node.rehash()
	bld.created++
	return node, nil
}

func (bld *merkleBuilder) buildObject(prev *mnode, pa, pb, na, nb int, speculative bool) (*mnode, error) {
	prevIsObject := prev != nil && prev.kind == kindObject
	// the previous level's entries reconstruct arithmetically from the node's
	// recorded order and sizes; scanning the previous snapshot is the fallback
	var prevList []objEntry
	if prevIsObject && pa >= 0 && bld.prevBuf[pa] == '{' {
		prevList = prevObjectSpansOf(prev, pa, pb)
		if prevList == nil {
			var err error
			if prevList, err = scanObjectEntriesAt(bld.prevBuf, pa); err != nil {
				return nil, err
			}
		}
	}
	var entries []objEntry
	specKey, hasSpec := "", false
	if bld.win != nil && prevList != nil && prev.order != nil {
		entries, specKey, hasSpec, _ = deriveObjectEntries(bld.newBuf, prevList, na, nb, bld.win)
	}
	if entries == nil {
		// a plain scan inside a guessed span could accept a wrong region, so a
		// failed derivation under speculation abandons the windowed build
		if speculative {
			return nil, errWindowAbort
		}
		var err error
		if entries, err = scanObjectEntriesAt(bld.newBuf, na); err != nil {
			return nil, err
		}
	}
	// duplicates resolve last-wins, matching encoding/json; the value being
	// dropped never reaches a leaf, so its well-formedness is checked here
	byName := make(map[string]objEntry, len(entries))
	dups := false
	for _, e := range entries {
		if old, ok := byName[e.key]; ok {
			dups = true
			if !json.Valid(bld.newBuf[old.vs:old.ve]) {
				return nil, errors.New("invalid JSON object")
			}
		}
		byName[e.key] = e
	}
	var prevSpanByKey map[string][2]int
	if prevList != nil {
		prevSpanByKey = make(map[string][2]int, len(prevList))
		for _, e := range prevList {
			prevSpanByKey[e.key] = [2]int{e.vs, e.ve}
		}
	}
	keys := make([]string, 0, len(byName))
	for key := range byName {
		keys = append(keys, key)
	}
	slices.Sort(keys)

	node := &mnode{kind: kindObject, keys: keys, kids: make([]*mnode, len(keys)), size: nb - na}
	shared := prevIsObject && len(prev.keys) == len(keys)
	for i, key := range keys {
		e := byName[key]
		pvs, pve := -1, -1
		if ps, ok := prevSpanByKey[key]; ok {
			pvs, pve = ps[0], ps[1]
		}
		kid, err := bld.build(prev.kidByKey(key), pvs, pve, e.vs, e.ve, hasSpec && key == specKey)
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
	if !dups {
		node.order = make([]uint32, 0, len(entries)*2)
		for _, e := range entries {
			idx, _ := slices.BinarySearch(keys, e.key)
			node.order = append(node.order, uint32(idx), uint32(e.klen))
		}
	}
	node.rehash()
	bld.created++
	return node, nil
}

func (bld *merkleBuilder) buildArray(prev *mnode, pa, pb, na, nb int, speculative bool) (*mnode, error) {
	prevIsArray := prev != nil && prev.kind == kindArray
	var prevSpans [][2]int
	if prevIsArray && pa >= 0 && bld.prevBuf[pa] == '[' {
		prevSpans = prevArraySpansOf(prev, pa, pb)
		if prevSpans == nil {
			var err error
			if prevSpans, err = scanArraySpansAt(bld.prevBuf, pa); err != nil {
				return nil, err
			}
		}
	}
	var spans [][2]int
	var pairing []int
	specIndex := -1
	if bld.win != nil && prevSpans != nil {
		spans, pairing, specIndex, _ = deriveArraySpans(bld.newBuf, prevSpans, na, nb, bld.win)
	}
	if spans == nil {
		if speculative {
			return nil, errWindowAbort
		}
		var err error
		if spans, err = scanArraySpansAt(bld.newBuf, na); err != nil {
			return nil, err
		}
	}
	node := &mnode{kind: kindArray, kids: make([]*mnode, len(spans)), size: nb - na}
	shared := prevIsArray && len(prev.kids) == len(spans)
	for i, span := range spans {
		pi := i
		if pairing != nil {
			pi = pairing[i]
		}
		var prevKid *mnode
		pvs, pve := -1, -1
		if prevIsArray && pi >= 0 && pi < len(prev.kids) {
			prevKid = prev.kids[pi]
		}
		if prevSpans != nil && pi >= 0 && pi < len(prevSpans) {
			pvs, pve = prevSpans[pi][0], prevSpans[pi][1]
		}
		kid, err := bld.build(prevKid, pvs, pve, span[0], span[1], i == specIndex)
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
	bld.created++
	return node, nil
}

// rawSemanticEqual preserves the equality the merge patcher has always used:
// encoding/json's float64 comparison, so 1 and 1.0 (and -0 and 0.0) are equal,
// and key order, whitespace, string escaping and duplicate keys are ignored.
//
// It is the hottest comparison on a churn-heavy push — it runs for every
// changed leaf, in the build and again in each protocol's diff walk, and
// decoding both sides into interface trees was 42% of CPU on a push changing
// a fifth of the document. The raw walk below decides the common cases —
// scalars, and containers whose key sequences line up, which is everything
// encoding/json itself emits — without decoding. Only when it cannot decide
// (reordered keys, duplicate keys overriding an unequal pair, escaped keys)
// does it fall back to the interface comparison, which remains the semantic
// reference.
func rawSemanticEqual(a, b json.RawMessage) (bool, error) {
	if bytes.Equal(a, b) {
		return true, nil
	}
	if equal, decided := rawEqualFast(a, b); decided {
		return equal, nil
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

// rawEqualFast answers rawSemanticEqual without decoding, or reports that it
// cannot. Both inputs are leaves that were validated when their trees were
// built, so scanner offsets can be trusted; anything surprising returns
// undecided rather than guessing.
func rawEqualFast(a, b []byte) (equal, decided bool) {
	ia, ib := skipJSONSpace(a, 0), skipJSONSpace(b, 0)
	if ia >= len(a) || ib >= len(b) {
		return false, false
	}
	ea, eb := scanJSONValue(a, ia), scanJSONValue(b, ib)
	if skipJSONSpace(a, ea) != len(a) || skipJSONSpace(b, eb) != len(b) {
		return false, false
	}
	return rawValueEqualFast(a[ia:ea], b[ib:eb])
}

// rawValueEqualFast compares two tightly-scanned value spans.
func rawValueEqualFast(a, b []byte) (equal, decided bool) {
	if bytes.Equal(a, b) {
		return true, true
	}
	ca, cb := a[0], b[0]
	switch {
	case ca == '{' && cb == '{':
		return rawObjectEqualFast(a, b)
	case ca == '[' && cb == '[':
		return rawArrayEqualFast(a, b)
	case ca == '"' && cb == '"':
		// An escape-free, valid-UTF-8 JSON string is its content's unique
		// encoding, so differing bytes mean differing strings. Escapes let two
		// encodings spell one value, and encoding/json coerces invalid UTF-8
		// to U+FFFD — the parity fuzzer caught "\x9a" and "\xff" decoding
		// equal — so both cases decode just the strings instead.
		if bytes.IndexByte(a, '\\') < 0 && bytes.IndexByte(b, '\\') < 0 &&
			utf8.Valid(a) && utf8.Valid(b) {
			return false, true
		}
		var sa, sb string
		if json.Unmarshal(a, &sa) != nil || json.Unmarshal(b, &sb) != nil {
			return false, false
		}
		return sa == sb, true
	case isNumberStart(ca) && isNumberStart(cb):
		fa, errA := strconv.ParseFloat(string(a), 64)
		fb, errB := strconv.ParseFloat(string(b), 64)
		if errA != nil || errB != nil {
			return false, false
		}
		return fa == fb, true
	default:
		// The matching-class pairs are all handled above, so this is either a
		// cross-class pair — unequal by type — or involves a literal. Both are
		// decided as long as both spans are recognisable; malformed input stays
		// on the fallback path and errors the way it always has.
		if classOfJSON(a) == 0 || classOfJSON(b) == 0 {
			return false, false
		}
		return false, true
	}
}

// classOfJSON buckets a value span by type; 0 means unrecognised.
func classOfJSON(v []byte) byte {
	switch c := v[0]; {
	case c == '{', c == '[', c == '"':
		return c
	case isNumberStart(c):
		return '0'
	case isJSONLiteral(v):
		// true and false share the bool class; null is its own
		if c == 'n' {
			return 'n'
		}
		return 't'
	default:
		return 0
	}
}

func isJSONLiteral(v []byte) bool {
	switch string(v) {
	case "true", "false", "null":
		return true
	default:
		return false
	}
}

func isNumberStart(c byte) bool {
	return c == '-' || (c >= '0' && c <= '9')
}

// rawObjectEqualFast walks two object spans in lockstep. It decides only when
// the key sequences match pairwise; reordered or re-escaped keys fall back.
// Duplicate keys resolve last-wins, so an unequal pair whose key occurs again
// later is overridden and skipped rather than judged.
func rawObjectEqualFast(a, b []byte) (equal, decided bool) {
	ia, ib := skipJSONSpace(a, 1), skipJSONSpace(b, 1)
	emptyA := ia < len(a) && a[ia] == '}'
	emptyB := ib < len(b) && b[ib] == '}'
	if emptyA || emptyB {
		return emptyA && emptyB, true
	}
	for {
		keyEndA, keyEndB := scanJSONString(a, ia), scanJSONString(b, ib)
		if !bytes.Equal(a[ia:keyEndA], b[ib:keyEndB]) {
			return false, false
		}
		key := a[ia:keyEndA]
		ia = skipJSONSpace(a, keyEndA)
		ib = skipJSONSpace(b, keyEndB)
		if ia >= len(a) || a[ia] != ':' || ib >= len(b) || b[ib] != ':' {
			return false, false
		}
		va := skipJSONSpace(a, ia+1)
		vb := skipJSONSpace(b, ib+1)
		vaEnd, vbEnd := scanJSONValue(a, va), scanJSONValue(b, vb)
		if vaEnd == va || vbEnd == vb {
			return false, false
		}
		eq, dec := rawValueEqualFast(a[va:vaEnd], b[vb:vbEnd])
		if !dec {
			return false, false
		}
		ia = skipJSONSpace(a, vaEnd)
		ib = skipJSONSpace(b, vbEnd)
		// An unequal pair only decides the objects if it is the key's last
		// occurrence on both sides; a later duplicate on either side overrides.
		if !eq && !objectKeyRepeats(a, ia, key) && !objectKeyRepeats(b, ib, key) {
			return false, true
		}
		moreA := ia < len(a) && a[ia] == ','
		moreB := ib < len(b) && b[ib] == ','
		doneA := ia < len(a) && a[ia] == '}'
		doneB := ib < len(b) && b[ib] == '}'
		switch {
		case moreA && moreB:
			ia = skipJSONSpace(a, ia+1)
			ib = skipJSONSpace(b, ib+1)
		case doneA && doneB:
			return true, true
		default:
			// Key counts differ. Duplicates could still make the maps equal,
			// so this is a fallback, not a verdict.
			return false, false
		}
	}
}

// objectKeyRepeats reports whether key occurs again in the object body from
// offset i, which sits on the separator after a just-compared pair.
func objectKeyRepeats(data []byte, i int, key []byte) bool {
	for i < len(data) && data[i] == ',' {
		i = skipJSONSpace(data, i+1)
		keyEnd := scanJSONString(data, i)
		if bytes.Equal(data[i:keyEnd], key) {
			return true
		}
		i = skipJSONSpace(data, keyEnd)
		if i >= len(data) || data[i] != ':' {
			return false
		}
		i = skipJSONSpace(data, i+1)
		end := scanJSONValue(data, i)
		if end == i {
			return false
		}
		i = skipJSONSpace(data, end)
	}
	return false
}

// rawArrayEqualFast compares two array spans element by element. Arrays have
// no duplicate-key subtlety, so a decided inequality is final, and a length
// mismatch is an inequality outright.
func rawArrayEqualFast(a, b []byte) (equal, decided bool) {
	ia, ib := skipJSONSpace(a, 1), skipJSONSpace(b, 1)
	emptyA := ia < len(a) && a[ia] == ']'
	emptyB := ib < len(b) && b[ib] == ']'
	if emptyA || emptyB {
		return emptyA && emptyB, true
	}
	for {
		vaEnd, vbEnd := scanJSONValue(a, ia), scanJSONValue(b, ib)
		if vaEnd == ia || vbEnd == ib {
			return false, false
		}
		eq, dec := rawValueEqualFast(a[ia:vaEnd], b[ib:vbEnd])
		if !dec {
			return false, false
		}
		if !eq {
			return false, true
		}
		ia = skipJSONSpace(a, vaEnd)
		ib = skipJSONSpace(b, vbEnd)
		moreA := ia < len(a) && a[ia] == ','
		moreB := ib < len(b) && b[ib] == ','
		doneA := ia < len(a) && a[ia] == ']'
		doneB := ib < len(b) && b[ib] == ']'
		switch {
		case moreA && moreB:
			ia = skipJSONSpace(a, ia+1)
			ib = skipJSONSpace(b, ib+1)
		case doneA && doneB:
			return true, true
		default:
			return false, true
		}
	}
}
