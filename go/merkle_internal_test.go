package velox

import (
	"encoding/json"
	"fmt"
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
		{
			name:      "element changed",
			before:    `{"log":[{"m":"a"},{"m":"b"},{"m":"c"}]}`,
			after:     `{"log":[{"m":"a"},{"m":"B"},{"m":"c"}]}`,
			wantKinds: []string{opSet},
		},
		{
			name:      "array shrunk",
			before:    `{"log":[1,2,3,4]}`,
			after:     `{"log":[1,2]}`,
			wantKinds: []string{opLen},
		},
		{
			name:       "array grown",
			before:     `{"log":[1,2]}`,
			after:      `{"log":[1,2,3,4]}`,
			wantKinds:  []string{opSet, opSet},
			wantAppend: true,
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
