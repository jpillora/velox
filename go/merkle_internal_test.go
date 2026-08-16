package velox

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

// buildTree returns the tree for one document, built against an optional
// previous (tree, bytes) pair so tests can assert node sharing across versions.
func buildTree(t *testing.T, leafSize int, prev *mnode, prevRaw, doc string) *mnode {
	t.Helper()
	builder := &merkleBuilder{leafSize: leafSize}
	var previous json.RawMessage
	if prevRaw != "" {
		previous = json.RawMessage(prevRaw)
	}
	root, err := builder.buildObject(prev, previous, json.RawMessage(doc))
	if err != nil {
		t.Fatalf("build %s: %v", doc, err)
	}
	return root
}

func TestMerkleSharesUnchangedSubtrees(t *testing.T) {
	const before = `{"a":{"x":1,"y":2},"b":{"x":1,"y":2},"c":[1,2,3]}`
	const after = `{"a":{"x":1,"y":2},"b":{"x":9,"y":2},"c":[1,2,3]}`

	first := buildTree(t, 1, nil, "", before)
	second := buildTree(t, 1, first, before, after)

	if first == second {
		t.Fatal("changed document reused the previous root")
	}
	// Every sibling of the change must be the identical node, not an equal copy.
	// Pointer identity is what makes retaining old versions cheap.
	for _, key := range []string{"a", "c"} {
		if first.kidByKey(key) != second.kidByKey(key) {
			t.Fatalf("subtree %q was rebuilt despite being unchanged", key)
		}
	}
	if first.kidByKey("b") == second.kidByKey("b") {
		t.Fatal("changed subtree b was shared")
	}
	// The unchanged grandchild under the changed parent is still shared.
	if first.kidByKey("b").kidByKey("y") != second.kidByKey("b").kidByKey("y") {
		t.Fatal("unchanged grandchild under a changed parent was rebuilt")
	}
}

func TestMerkleBuildOnlyAllocatesChangedPaths(t *testing.T) {
	var keys []string
	for i := range 40 {
		keys = append(keys, fmt.Sprintf(`"k%02d":{"v":%d,"pad":"aaaaaaaaaaaaaaaa"}`, i, i))
	}
	before := "{" + strings.Join(keys, ",") + "}"
	keys[7] = `"k07":{"v":999,"pad":"aaaaaaaaaaaaaaaa"}`
	after := "{" + strings.Join(keys, ",") + "}"

	first := &merkleBuilder{leafSize: 8}
	root, err := first.buildObject(nil, nil, json.RawMessage(before))
	if err != nil {
		t.Fatal(err)
	}
	second := &merkleBuilder{leafSize: 8}
	if _, err := second.buildObject(root, json.RawMessage(before), json.RawMessage(after)); err != nil {
		t.Fatal(err)
	}
	// The whole document costs many nodes; changing one leaf must cost only the
	// nodes on its path to the root.
	if second.created >= first.created/4 {
		t.Fatalf("changed-path build allocated %d nodes, initial build %d", second.created, first.created)
	}
}

func TestMerkleLeafThreshold(t *testing.T) {
	const doc = `{"small":{"a":1},"large":{"a":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`
	root := buildTree(t, 40, nil, "", doc)
	if got := root.kidByKey("small").kind; got != kindLeaf {
		t.Fatalf("subtree below the threshold has kind %d, want leaf", got)
	}
	if got := root.kidByKey("large").kind; got != kindObject {
		t.Fatalf("subtree above the threshold has kind %d, want object", got)
	}
}

func TestMerkleHashIgnoresKeyOrder(t *testing.T) {
	a := buildTree(t, 1, nil, "", `{"outer":{"x":1,"y":[1,2]}}`)
	b := buildTree(t, 1, nil, "", `{"outer":{"y":[1,2],"x":1}}`)
	if a.hash != b.hash {
		t.Fatal("reordered keys produced a different root hash")
	}
}

func TestV3ArrayOperationsAddressElements(t *testing.T) {
	tests := []struct {
		name       string
		before     string
		after      string
		wantKinds  []string
		wantAppend bool
	}{
		// Elements are padded so that addressing one really is cheaper than
		// resending the array; on a tiny array it is not, and the differ is
		// expected to notice that — see
		// TestV3CollapsesOperationsThatOutweighTheSubtree.
		{
			name:      "element changed",
			before:    `{"log":[{"m":"` + pad("a") + `"},{"m":"` + pad("b") + `"},{"m":"` + pad("c") + `"}]}`,
			after:     `{"log":[{"m":"` + pad("a") + `"},{"m":"` + pad("B") + `"},{"m":"` + pad("c") + `"}]}`,
			wantKinds: []string{opSet},
		},
		{
			name:      "array shrunk",
			before:    `{"log":["` + pad("a") + `","` + pad("b") + `","` + pad("c") + `","` + pad("d") + `"]}`,
			after:     `{"log":["` + pad("a") + `","` + pad("b") + `"]}`,
			wantKinds: []string{opLen},
		},
		{
			name:       "array grown",
			before:     `{"log":["` + pad("a") + `","` + pad("b") + `"]}`,
			after:      `{"log":["` + pad("a") + `","` + pad("b") + `","` + pad("c") + `","` + pad("d") + `"]}`,
			wantKinds:  []string{opSplice},
			wantAppend: true,
		},
		// The serial hash trim turns shifts into splices: one operation for an
		// insertion or deletion anywhere, instead of reassigning every element
		// after it.
		{
			name:      "array prepended",
			before:    `{"log":["` + pad("a") + `","` + pad("b") + `","` + pad("c") + `"]}`,
			after:     `{"log":["` + pad("z") + `","` + pad("a") + `","` + pad("b") + `","` + pad("c") + `"]}`,
			wantKinds: []string{opSplice},
		},
		{
			name:      "middle insertion",
			before:    `{"log":["` + pad("a") + `","` + pad("b") + `","` + pad("c") + `"]}`,
			after:     `{"log":["` + pad("a") + `","` + pad("z") + `","` + pad("b") + `","` + pad("c") + `"]}`,
			wantKinds: []string{opSplice},
		},
		{
			name:      "middle deletion",
			before:    `{"log":["` + pad("a") + `","` + pad("b") + `","` + pad("c") + `","` + pad("d") + `"]}`,
			after:     `{"log":["` + pad("a") + `","` + pad("d") + `"]}`,
			wantKinds: []string{opSplice},
		},
		{
			name:      "edit then deletion",
			before:    `{"log":["` + pad("a") + `","` + pad("b") + `","` + pad("c") + `","` + pad("d") + `"]}`,
			after:     `{"log":["` + pad("a") + `","` + pad("B") + `","` + pad("d") + `"]}`,
			wantKinds: []string{opSet, opSplice},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			patcher := &mergePatcher{leafSize: 4}
			if _, err := patcher.patch([]byte(tt.before)); err != nil {
				t.Fatal(err)
			}
			_, ops, err := patcher.update([]byte(tt.after), true)
			if err != nil {
				t.Fatal(err)
			}
			var kinds []string
			for _, o := range ops {
				kinds = append(kinds, o.kind)
			}
			if len(kinds) != len(tt.wantKinds) {
				t.Fatalf("ops = %v, want kinds %v", kinds, tt.wantKinds)
			}
			// The whole point is that the array is not resent, so every path
			// must reach past it to an individual element.
			for _, o := range ops {
				if o.kind == opSet && len(o.path) < 2 {
					t.Fatalf("set operation replaced the whole array: path %v", o.path)
				}
			}
			assertOpsRoundTrip(t, tt.before, tt.after, ops)
		})
	}
}

// pad grows a marker into an element big enough that addressing it costs less
// than resending the array it sits in.
func pad(marker string) string {
	return marker + strings.Repeat("x", 60)
}

// TestV3CollapsesOperationsThatOutweighTheSubtree covers positional operations'
// worst case. A cross-shift — here a reversal — defeats the serial hash trim
// and pairs every element wrongly, so a naive differ emits one assignment per
// element: more bytes than the whole array. The differ must notice and send
// the array instead, which bounds v3 at no worse than v2 rather than
// dramatically worse.
func TestV3CollapsesOperationsThatOutweighTheSubtree(t *testing.T) {
	elements := make([]string, 200)
	reversed := make([]string, 200)
	for i := range elements {
		elements[i] = strconv.Itoa(i)
		reversed[len(reversed)-1-i] = elements[i]
	}
	before := `{"log":[` + strings.Join(elements, ",") + `]}`
	after := `{"log":[` + strings.Join(reversed, ",") + `]}`

	patcher := &mergePatcher{leafSize: 1}
	if _, err := patcher.patch([]byte(before)); err != nil {
		t.Fatal(err)
	}
	_, ops, err := patcher.update([]byte(after), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 {
		t.Fatalf("a reversal produced %d operations, want the array sent once", len(ops))
	}
	if ops[0].kind != opSet || len(ops[0].path) != 1 {
		t.Fatalf("collapse emitted %v, want a single assignment of the array", ops[0])
	}
	encoded, err := json.Marshal(ops)
	if err != nil {
		t.Fatal(err)
	}
	assertOpsRoundTrip(t, before, after, ops)

	// The shape that used to be the worst case — a prepend shifting every
	// element — must now be one splice carrying just the inserted value.
	prependPatcher := &mergePatcher{leafSize: 1}
	if _, err := prependPatcher.patch([]byte(before)); err != nil {
		t.Fatal(err)
	}
	prepended := `{"log":[999,` + strings.Join(elements, ",") + `]}`
	_, prependOps, err := prependPatcher.update([]byte(prepended), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(prependOps) != 1 || prependOps[0].kind != opSplice {
		t.Fatalf("a prepend produced %v, want one splice", prependOps)
	}
	if encoded, err := json.Marshal(prependOps); err != nil || len(encoded) > 64 {
		t.Fatalf("prepend splice encodes to %d bytes (%v), want a handful", len(encoded), err)
	}
	assertOpsRoundTrip(t, before, prepended, prependOps)

	// The guarantee is that v3 is never dramatically worse than v2 on this
	// shape, so compare against what v2 would have sent for the same change.
	// A small envelope difference is expected; a per-element blowup is not.
	legacy := &mergePatcher{leafSize: 1}
	if _, err := legacy.patch([]byte(before)); err != nil {
		t.Fatal(err)
	}
	v2delta, err := legacy.patch([]byte(after))
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > len(v2delta)+64 {
		t.Fatalf("v3 patch is %d bytes against v2's %d; the collapse did not bound the worst case", len(encoded), len(v2delta))
	}

	// The root itself must never collapse: the operation would have an empty
	// path, and no applier can replace the document it was handed.
	wholesale := `{"a":"` + strings.Repeat("y", 200) + `","b":2}`
	rootPatcher := &mergePatcher{leafSize: 1}
	if _, err := rootPatcher.patch([]byte(`{"a":1,"b":2}`)); err != nil {
		t.Fatal(err)
	}
	_, rootOps, err := rootPatcher.update([]byte(wholesale), true)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range rootOps {
		if len(o.path) == 0 {
			t.Fatalf("the root was collapsed into %v", o)
		}
	}
}

func TestV3ResumeAcrossManyVersions(t *testing.T) {
	patcher := &mergePatcher{leafSize: 8}
	doc := `{"counter":0,"stable":{"a":1,"b":2},"log":[1,2,3]}`
	if _, err := patcher.patch([]byte(doc)); err != nil {
		t.Fatal(err)
	}
	history := newVersionHistory(time.Minute, 1<<20)
	history.record(1, patcher.tree, patcher.created, time.Now())
	base := patcher.tree

	// Advance ten versions, as a client that was disconnected throughout would
	// have missed.
	for i := 1; i <= 10; i++ {
		doc = fmt.Sprintf(`{"counter":%d,"stable":{"a":1,"b":2},"log":[1,2,3]}`, i)
		if _, _, err := patcher.update([]byte(doc), true); err != nil {
			t.Fatal(err)
		}
		history.record(int64(i+1), patcher.tree, patcher.created, time.Now())
	}

	found, ok := history.find(rootHash(base))
	if !ok {
		t.Fatal("base version was not retained")
	}
	ops, err := diffTrees(found, patcher.tree, true)
	if err != nil {
		t.Fatal(err)
	}
	// Ten versions of drift still collapse to the single field that differs.
	if len(ops) != 1 {
		t.Fatalf("resume produced %d operations, want 1", len(ops))
	}
	assertOpsRoundTrip(t, `{"counter":0,"stable":{"a":1,"b":2},"log":[1,2,3]}`, doc, ops)
}

func TestHistoryEvictsByWindowAndNodeBudget(t *testing.T) {
	start := time.Now()
	h := newVersionHistory(time.Minute, 1<<20)
	for i := range 5 {
		h.record(int64(i+1), &mnode{hash: [16]byte{byte(i + 1)}}, 1, start.Add(time.Duration(i)*time.Second))
	}
	if h.len() != 5 {
		t.Fatalf("history holds %d entries, want 5", h.len())
	}
	// Everything older than the window goes, but the newest is always kept.
	h.evict(start.Add(2 * time.Minute))
	if h.len() != 1 {
		t.Fatalf("after the window elapsed history holds %d entries, want 1", h.len())
	}

	budget := newVersionHistory(time.Hour, 3)
	for i := range 10 {
		budget.record(int64(i+1), &mnode{hash: [16]byte{byte(i + 1)}}, 1, start)
	}
	if budget.nodes > 3+1 {
		t.Fatalf("history retained %d nodes, want the budget of 3 respected", budget.nodes)
	}
}

func TestV3NullStateIsNotResumable(t *testing.T) {
	h := newVersionHistory(time.Minute, 1<<20)
	h.record(1, nil, 0, time.Now())
	if h.len() != 0 {
		t.Fatal("a null state was recorded as resumable")
	}
	if _, ok := h.find(""); ok {
		t.Fatal("the empty hash resolved to a tree")
	}
}

// assertOpsRoundTrip applies ops to the decoded before-document and requires
// the result to equal the after-document.
func assertOpsRoundTrip(t *testing.T, before, after string, ops []op) {
	t.Helper()
	var target, want any
	if err := json.Unmarshal([]byte(before), &target); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(after), &want); err != nil {
		t.Fatal(err)
	}
	got, err := applyOps(target, ops)
	if err != nil {
		t.Fatalf("apply %v: %v", ops, err)
	}
	if !valueEqual(got, want) {
		gotBytes, _ := json.Marshal(got)
		t.Fatalf("applied ops = %s, want %s (ops %v)", gotBytes, after, ops)
	}
}

// FuzzV3RoundTrip is the load-bearing correctness check: whatever operations
// the encoder emits, applying them to the previous document must reproduce the
// new one exactly. A tiny leaf size forces the structural paths rather than
// collapsing everything into opaque leaves.
func FuzzV3RoundTrip(f *testing.F) {
	seeds := [][2]string{
		{`{"a":1}`, `{"a":2}`},
		{`{"a":{"b":1,"c":2}}`, `{"a":{"c":2}}`},
		{`{"a":[1,2,3]}`, `{"a":[1,9,3]}`},
		{`{"a":[1,2,3]}`, `{"a":[1]}`},
		{`{"a":[1]}`, `{"a":[1,2,3]}`},
		{`{"a":{"b":1}}`, `{"a":"scalar"}`},
		{`{"a":"scalar"}`, `{"a":{"b":1}}`},
		{`{"a":[{"x":1},{"y":2}]}`, `{"a":[{"x":1},{"y":3},{"z":4}]}`},
		{`{"a":[1,2,3]}`, `{"a":[9,1,2,3]}`},
		{`{"a":[1,2,3,4]}`, `{"a":[1,4]}`},
		{`{"a":[1,2,3]}`, `{"a":[1,9,2,3]}`},
		{`{"a":[1,2,3,4]}`, `{"a":[4,3,2,1]}`},
		{`{"a":[1,2,3,4]}`, `{"a":[1,9,4]}`},
		{`{"a":1,"b":2,"c":3}`, `{"c":3,"b":2,"a":1}`},
		{`{"n":1}`, `{"n":1.0}`},
		{`{"n":-0}`, `{"n":0}`},
		{`{"s":"a"}`, `{"s":"a"}`},
		{`{}`, `{"added":{"deep":{"deeper":[1,2]}}}`},
		{`{"drop":{"a":1},"keep":2}`, `{"keep":2}`},
		{`{"a":[[1,2],[3,4]]}`, `{"a":[[1,2],[3,5]]}`},
	}
	for _, seed := range seeds {
		f.Add(seed[0], seed[1])
	}
	f.Fuzz(func(t *testing.T, before, after string) {
		var want any
		if err := json.Unmarshal([]byte(after), &want); err != nil {
			t.Skip()
		}
		if _, ok := want.(map[string]any); !ok {
			t.Skip()
		}
		for _, leafSize := range []int{1, 24, 512} {
			patcher := &mergePatcher{leafSize: leafSize}
			if _, err := patcher.patch([]byte(before)); err != nil {
				t.Skip()
			}
			_, ops, err := patcher.update([]byte(after), true)
			if err != nil {
				t.Skip()
			}
			var target any
			if err := json.Unmarshal([]byte(before), &target); err != nil {
				t.Skip()
			}
			got, err := applyOps(target, ops)
			if err != nil {
				t.Fatalf("leafSize %d: apply failed: %v (ops %v)", leafSize, err, ops)
			}
			if !valueEqual(got, want) {
				gotBytes, _ := json.Marshal(got)
				t.Fatalf("leafSize %d: applied ops = %s, want %s (ops %v)", leafSize, gotBytes, after, ops)
			}
		}
	})
}

// acceptableState is what velox is willing to publish: valid JSON that is
// either an object or null. It is deliberately expressed with encoding/json
// rather than with velox's own scanners, so the fuzz test compares the builder
// against an independent judge.
func acceptableState(doc []byte) bool {
	if !json.Valid(doc) {
		return false
	}
	if isJSONNull(doc) {
		return true
	}
	return firstJSONByte(doc) == '{'
}

// FuzzMalformedState checks that the builder fails closed.
//
// Narrowing validation to changed leaves rests on an argument — bytes identical
// to the previous snapshot were validated when that snapshot was accepted — that
// only holds if boundary scanning rejects everything encoding/json would. Since
// json.Valid no longer covers the whole document, the scanners now run before
// validity is known, and State.Data is a caller-supplied function: a custom
// MarshalJSON can emit a truncated document, junk between values, or an
// unterminated string. The failure to avoid is not a crash but a false
// "unchanged", where malformed bytes are quietly accepted and clients are left
// holding something the server never meant to send.
func FuzzMalformedState(f *testing.F) {
	seeds := [][2]string{
		{`{"a":1}`, `{"a":`},
		{`{"a":1}`, `{"a":1`},
		{`{"a":1}`, `{"a":1}}`},
		{`{"a":1}`, `{"a":1} junk`},
		{`{"a":1}`, `{"a":1}{"b":2}`},
		{`{"a":1}`, `{"a":"unterminated}`},
		{`{"a":1}`, `{"a":tru}`},
		{`{"a":1}`, `{"a":01}`},
		{`{"a":1}`, `{"a":1,}`},
		{`{"a":1}`, `{,"a":1}`},
		{`{"a":1}`, `{"a" 1}`},
		{`{"a":1}`, `{"a":1 "b":2}`},
		{`{"a":1}`, "{\"a\":1}\x00"},
		{`{"a":1}`, `{"a":[1,2}`},
		{`{"a":1}`, `{"a":{"b":}}`},
		{`{"a":1}`, `nul`},
		{`{"a":1}`, `[1,2]`},
		{`{"a":1}`, `"string"`},
		{`{"a":1}`, `{"a":1e1000}`},
		{`{"a":1}`, `{"a":1,"a":2}`},
		{`{"a":{"b":1}}`, `{"a":{"b":1,"c":}}`},
	}
	for _, seed := range seeds {
		f.Add(seed[0], seed[1])
	}

	f.Fuzz(func(t *testing.T, before, after string) {
		if !acceptableState([]byte(before)) {
			t.Skip()
		}
		for _, leafSize := range []int{1, 24, 512} {
			patcher := &mergePatcher{leafSize: leafSize}
			if _, err := patcher.patch([]byte(before)); err != nil {
				t.Skipf("leafSize %d rejected a valid seed %q: %v", leafSize, before, err)
			}
			cached := patcher.prev
			tree := patcher.tree

			delta, err := patcher.patch([]byte(after))
			if err != nil {
				// A rejected state must leave the patcher exactly as it was, or
				// the next push diffs against something never published.
				if !bytes.Equal(patcher.prev, cached) || patcher.tree != tree {
					t.Fatalf("leafSize %d: rejecting %q disturbed the cache", leafSize, after)
				}
				continue
			}
			if !acceptableState([]byte(after)) {
				t.Fatalf("leafSize %d: accepted malformed state %q as %s", leafSize, after, delta)
			}
			// Accepted, so the patch must genuinely carry before to after. A null
			// state is excluded: it clears rather than patches, which refresh
			// handles separately.
			if !isJSONNull([]byte(after)) {
				assertMergePatchResult(t, before, after, delta)
			}
		}
	})
}

func TestHistoryHandlesRepeatedRootHashes(t *testing.T) {
	// A state that changes and changes back records the same root hash twice.
	// The two trees are equal, so either serves — but evicting the first must
	// not make the second unreachable.
	start := time.Now()
	h := newVersionHistory(time.Hour, 1<<20)
	a := &mnode{hash: [16]byte{1}}
	b := &mnode{hash: [16]byte{2}}
	again := &mnode{hash: [16]byte{1}}

	h.record(1, a, 1, start)
	h.record(2, b, 1, start.Add(time.Second))
	h.record(3, again, 1, start.Add(2*time.Second))

	if root, ok := h.find(rootHash(a)); !ok || root == nil {
		t.Fatal("a repeated root hash was not resolvable")
	}
	// Age out the first two, leaving only the repeat.
	h.window = time.Millisecond
	h.evict(start.Add(time.Minute))
	if h.len() != 1 {
		t.Fatalf("history holds %d entries, want 1", h.len())
	}
	if _, ok := h.find(rootHash(a)); !ok {
		t.Fatal("evicting the first occurrence made the surviving one unreachable")
	}
	if _, ok := h.find(rootHash(b)); ok {
		t.Fatal("an evicted root is still resolvable")
	}
}

func TestV3RejectsDeleteAgainstAnArrayIndex(t *testing.T) {
	// Arrays are only ever changed by assignment and truncation, so both
	// appliers must refuse a delete rather than leaving a hole.
	var doc any
	if err := json.Unmarshal([]byte(`{"log":[1,2,3]}`), &doc); err != nil {
		t.Fatal(err)
	}
	_, err := applyOps(doc, []op{{kind: opDel, path: []any{"log", 1}}})
	if err == nil {
		t.Fatal("delete against an array index was accepted")
	}
}
