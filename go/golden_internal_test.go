package velox

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// goldenCase is one before/after pair together with the operations the Go
// encoder produced for it.
type goldenCase struct {
	Name   string          `json:"name"`
	Before json.RawMessage `json:"before"`
	After  json.RawMessage `json:"after"`
	Ops    json.RawMessage `json:"ops"`
}

// TestV3GoldenOps regenerates the fixture that js/test/ops.test.js replays. The
// Go applier and the JavaScript one are separate implementations of the same
// spec, so without a shared fixture they can drift apart silently; this is what
// ties them together.
func TestV3GoldenOps(t *testing.T) {
	cases := []struct {
		name          string
		before, after string
	}{
		{"scalar changed", `{"a":1,"b":"x"}`, `{"a":2,"b":"x"}`},
		{"key added", `{"a":1}`, `{"a":1,"b":{"c":[1,2]}}`},
		{"key deleted", `{"a":1,"b":2}`, `{"a":1}`},
		{"nested leaf", `{"o":{"p":{"q":"before","r":1}}}`, `{"o":{"p":{"q":"after","r":1}}}`},
		{"type changed", `{"v":{"n":1}}`, `{"v":"scalar"}`},
		{"array element", `{"log":[{"m":"a"},{"m":"b"}]}`, `{"log":[{"m":"a"},{"m":"B"}]}`},
		{"array shrunk", `{"log":[1,2,3,4,5]}`, `{"log":[1,2]}`},
		{"array grown", `{"log":[1,2]}`, `{"log":[1,2,3,4]}`},
		{"array resized and changed", `{"log":[1,2,3,4]}`, `{"log":[9,2]}`},
		{"array prepended", `{"log":[1,2,3]}`, `{"log":[9,1,2,3]}`},
		{"array middle insertion", `{"log":[1,2,3]}`, `{"log":[1,8,9,2,3]}`},
		{"array middle deletion", `{"log":[1,2,3,4]}`, `{"log":[1,4]}`},
		{"array edited and spliced", `{"log":[1,2,3,4]}`, `{"log":[1,9,4]}`},
		{"nested arrays", `{"m":[[1,2],[3,4]]}`, `{"m":[[1,2],[3,5]]}`},
		{"deep add and delete", `{"t":{"keep":1,"drop":{"x":1}}}`, `{"t":{"keep":1,"add":[true,null]}}`},
		{"numeric object keys", `{"m":{"0":"a","1":"b"}}`, `{"m":{"0":"a","1":"B"}}`},
		{"empty containers", `{"a":{},"b":[]}`, `{"a":{"x":1},"b":[1]}`},
		{"unicode and escapes", `{"s":"before \" \\ 雪"}`, `{"s":"after \" \\ 𝄞"}`},
	}

	// Fully expanded. At the production default these short documents would all
	// fall below the threshold and collapse into single opaque leaves, so the
	// per-index and truncate operations would never reach the other applier.
	const leafSize = 1
	golden := make([]goldenCase, 0, len(cases))
	for _, tt := range cases {
		patcher := &mergePatcher{leafSize: leafSize}
		if _, err := patcher.patch([]byte(tt.before)); err != nil {
			t.Fatalf("%s: seed: %v", tt.name, err)
		}
		_, ops, err := patcher.update([]byte(tt.after), true)
		if err != nil {
			t.Fatalf("%s: update: %v", tt.name, err)
		}
		assertOpsRoundTrip(t, tt.before, tt.after, ops)

		encoded, err := json.Marshal(ops)
		if err != nil {
			t.Fatalf("%s: encode: %v", tt.name, err)
		}
		golden = append(golden, goldenCase{
			Name:   tt.name,
			Before: json.RawMessage(tt.before),
			After:  json.RawMessage(tt.after),
			Ops:    encoded,
		})
	}

	encoded, err := json.MarshalIndent(golden, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')

	path := filepath.Join("testdata", "ops", "cases.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	existing, err := os.ReadFile(path)
	if err == nil && bytes.Equal(existing, encoded) {
		return
	}
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		t.Fatal(err)
	}
	if err == nil {
		t.Logf("regenerated %s; re-run js/test/ops.test.js", path)
	}
}
