package velox

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestMergePatcherChanges(t *testing.T) {
	tests := []struct {
		name    string
		oldJSON string
		newJSON string
	}{
		{
			name:    "nested add and delete",
			oldJSON: `{"keep":1,"nested":{"drop":true,"same":"yes"}}`,
			newJSON: `{"keep":1,"nested":{"add":"new","same":"yes"}}`,
		},
		{
			name:    "nested leaf",
			oldJSON: `{"outer":{"middle":{"leaf":"before"},"same":[1,2,3]}}`,
			newJSON: `{"outer":{"middle":{"leaf":"after"},"same":[1,2,3]}}`,
		},
		{
			name:    "object to scalar",
			oldJSON: `{"value":{"nested":true}}`,
			newJSON: `{"value":"replacement"}`,
		},
		{
			name:    "scalar to object",
			oldJSON: `{"value":42}`,
			newJSON: `{"value":{"nested":true}}`,
		},
		{
			name:    "object to array",
			oldJSON: `{"value":{"nested":true}}`,
			newJSON: `{"value":[{"nested":true},2]}`,
		},
		{
			name:    "array to object",
			oldJSON: `{"value":[1,2,3]}`,
			newJSON: `{"value":{"nested":true}}`,
		},
		{
			name:    "array whole replacement",
			oldJSON: `{"value":[1,{"leaf":"before"},3]}`,
			newJSON: `{"value":[1,{"leaf":"after"},3]}`,
		},
		{
			name:    "boolean and string",
			oldJSON: `{"enabled":false,"name":"before"}`,
			newJSON: `{"enabled":true,"name":"after"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			patcher := &mergePatcher{}
			seed, err := patcher.patch([]byte(tt.oldJSON))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(seed, []byte(`{}`)) {
				t.Fatalf("seed patch = %s, want {}", seed)
			}

			patch, err := patcher.patch([]byte(tt.newJSON))
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(patch, []byte(`{}`)) {
				t.Fatal("changed documents produced an empty patch")
			}
			assertMergePatchResult(t, tt.oldJSON, tt.newJSON, patch)

			if tt.name == "array whole replacement" {
				assertJSONEqual(t, patch, []byte(`{"value":[1,{"leaf":"after"},3]}`))
			}
		})
	}
}

func TestMergePatcherSemanticEquality(t *testing.T) {
	tests := []struct {
		name    string
		oldJSON string
		newJSON string
	}{
		{
			name:    "object key order and whitespace",
			oldJSON: `{"value":{"a":1,"b":[true,false]}}`,
			newJSON: "{ \n  \"value\" : { \"b\" : [true,false], \"a\" : 1 } \n}",
		},
		{
			name:    "array formatting",
			oldJSON: `{"value":[1,{"a":true},"x"]}`,
			newJSON: `{"value": [ 1.0, { "a" : true }, "x" ]}`,
		},
		{
			name:    "number encoding",
			oldJSON: `{"value":1}`,
			newJSON: `{"value":1.0}`,
		},
		{
			name:    "string escape",
			oldJSON: `{"value":"a"}`,
			newJSON: `{"value":"\u0061"}`,
		},
		{
			name:    "duplicate key last wins",
			oldJSON: `{"value":{"a":1,"a":2}}`,
			newJSON: `{"value":{"a":2}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			patcher := &mergePatcher{}
			if _, err := patcher.patch([]byte(tt.oldJSON)); err != nil {
				t.Fatal(err)
			}
			modified := []byte(tt.newJSON)
			patch, err := patcher.patch(modified)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(patch, []byte(`{}`)) {
				t.Fatalf("patch = %s, want {}", patch)
			}
			if !bytes.Equal(patcher.prev, modified) {
				t.Fatalf("cache = %s, want latest input %s", patcher.prev, modified)
			}
		})
	}
}

func TestMergePatcherNullDeletes(t *testing.T) {
	patcher := &mergePatcher{}
	oldJSON := `{"object":{"leaf":true},"scalar":"value","keep":1}`
	if _, err := patcher.patch([]byte(oldJSON)); err != nil {
		t.Fatal(err)
	}
	patch, err := patcher.patch([]byte(`{"object":null,"scalar":null,"keep":1}`))
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, patch, []byte(`{"object":null,"scalar":null}`))
	assertMergePatchResult(t, oldJSON, `{"keep":1}`, patch)
}

func TestMergePatcherTopLevelObjectAndNull(t *testing.T) {
	patcher := &mergePatcher{}
	if patch, err := patcher.patch([]byte(`{}`)); err != nil || !bytes.Equal(patch, []byte(`{}`)) {
		t.Fatalf("empty object seed = %s, %v", patch, err)
	}
	if patcher.prev == nil {
		t.Fatal("valid empty object left cache uninitialized")
	}
	patch, err := patcher.patch([]byte(`{"added":true}`))
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, patch, []byte(`{"added":true}`))

	patch, err = patcher.patch([]byte(`null`))
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, patch, []byte(`{"added":null}`))
	if !bytes.Equal(patcher.prev, []byte(`null`)) {
		t.Fatalf("null cache = %s, want null", patcher.prev)
	}

	patch, err = patcher.patch([]byte(`{"after":1}`))
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, patch, []byte(`{"after":1}`))
}

func TestMergePatcherDuplicateKeyChange(t *testing.T) {
	patcher := &mergePatcher{}
	if _, err := patcher.patch([]byte(`{"value":{"a":1,"a":2}}`)); err != nil {
		t.Fatal(err)
	}
	patch, err := patcher.patch([]byte(`{"value":{"a":1,"a":3}}`))
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, patch, []byte(`{"value":{"a":3}}`))
}

func TestMergePatcherOwnsInput(t *testing.T) {
	patcher := &mergePatcher{}
	initial := []byte(`{"value":"A"}`)
	if _, err := patcher.patch(initial); err != nil {
		t.Fatal(err)
	}
	if reflect.ValueOf(patcher.prev).Pointer() == reflect.ValueOf(initial).Pointer() {
		t.Fatal("seed cache aliases caller input")
	}
	copy(initial, `{"value":"X"}`)
	if !bytes.Equal(patcher.prev, []byte(`{"value":"A"}`)) {
		t.Fatalf("seed cache changed with input: %s", patcher.prev)
	}

	modified := []byte(`{"value":"B"}`)
	if _, err := patcher.patch(modified); err != nil {
		t.Fatal(err)
	}
	if reflect.ValueOf(patcher.prev).Pointer() == reflect.ValueOf(modified).Pointer() {
		t.Fatal("updated cache aliases caller input")
	}
	copy(modified, `{"value":"Y"}`)
	if !bytes.Equal(patcher.prev, []byte(`{"value":"B"}`)) {
		t.Fatalf("updated cache changed with input: %s", patcher.prev)
	}
}

func TestMergePatcherInvalidInputRollsBack(t *testing.T) {
	patcher := &mergePatcher{}
	if _, err := patcher.patch([]byte(`{"value":"before"}`)); err != nil {
		t.Fatal(err)
	}
	previous := patcher.prev
	previousPointer := reflect.ValueOf(previous).Pointer()
	previousSnapshot := bytes.Clone(previous)

	for _, invalid := range [][]byte{nil, {}, []byte(`{`), []byte(`[]`), []byte(`1`), []byte(`"value"`)} {
		if patch, err := patcher.patch(invalid); err == nil {
			t.Fatalf("patch(%q) = %s, want error", invalid, patch)
		}
		if got := reflect.ValueOf(patcher.prev).Pointer(); got != previousPointer {
			t.Fatalf("cache pointer after %q = %x, want %x", invalid, got, previousPointer)
		}
		if !bytes.Equal(patcher.prev, previousSnapshot) {
			t.Fatalf("cache after %q = %s, want %s", invalid, patcher.prev, previousSnapshot)
		}
	}

	var unseeded mergePatcher
	if _, err := unseeded.patch(nil); err == nil {
		t.Fatal("nil initial input was accepted by exact-byte shortcut")
	}
}

func TestMergePatcherDeepNesting(t *testing.T) {
	const depth = 300
	oldJSON := strings.Repeat(`{"level":`, depth) + `"before"` + strings.Repeat(`}`, depth)
	newJSON := strings.Repeat(`{"level":`, depth) + `"after"` + strings.Repeat(`}`, depth)

	patcher := &mergePatcher{}
	if _, err := patcher.patch([]byte(oldJSON)); err != nil {
		t.Fatal(err)
	}
	patch, err := patcher.patch([]byte(newJSON))
	if err != nil {
		t.Fatal(err)
	}
	assertMergePatchResult(t, oldJSON, newJSON, patch)
}

func TestMergePatcherScannerTorture(t *testing.T) {
	tests := []struct {
		name    string
		oldJSON string
		newJSON string
	}{
		{
			name: "strings and escaped keys",
			oldJSON: `{"quote\"\\key":"braces { } brackets [ ] comma , quote \" slash \\",` +
				`"unicode":"\uD834\uDD1E 雪","nested":{"value":"before, } ]"}}`,
			newJSON: `{"unicode":"𝄞 雪","quote\"\\key":"braces { } brackets [ ] comma , quote \" slash \\",` +
				`"nested":{"value":"after, { ["}}`,
		},
		{
			name:    "numbers and empty containers",
			oldJSON: `{"numbers":[-0,1.25e+3,1e-1000],"empty":{"object":{},"array":[]},"duplicate":{"x":0,"x":1}}`,
			newJSON: `{"numbers":[0.0,1250,0],"empty":{"array":[],"object":{}},"duplicate":{"x":1},"added":{}}`,
		},
		{
			name:    "container type changes",
			oldJSON: `{"a":[],"b":{},"c":[{},[],{"text":"},]"}]}`,
			newJSON: `{"a":{},"b":[],"c":[{},[],{"text":"},] changed"}]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			patcher := &mergePatcher{}
			if _, err := patcher.patch([]byte(tt.oldJSON)); err != nil {
				t.Fatal(err)
			}
			patch, err := patcher.patch([]byte(tt.newJSON))
			if err != nil {
				t.Fatal(err)
			}
			assertMergePatchResult(t, tt.oldJSON, tt.newJSON, patch)
		})
	}
}

func TestMergePatcherNumberRangeValidation(t *testing.T) {
	valid := []string{
		`{"value":1.7976931348623157e308}`,
		`{"value":-1.7976931348623157e308}`,
		`{"value":1234567890123456789012345678901234567890}`,
		`{"value":1e-1000}`,
		`{"strings":["1e1000","-1e1000","1.7976931348623159e308"]}`,
	}
	for _, input := range valid {
		var patcher mergePatcher
		if _, err := patcher.patch([]byte(input)); err != nil {
			t.Errorf("valid input %s: %v", input, err)
		}
	}

	invalid := []string{
		`{"value":1e1000}`,
		`{"value":-1e1000}`,
		`{"value":1.7976931348623159e308}`,
	}
	for _, input := range invalid {
		var patcher mergePatcher
		if patch, err := patcher.patch([]byte(input)); err == nil {
			t.Errorf("overflow input %s produced %s, want error", input, patch)
		}
		if patcher.prev != nil {
			t.Errorf("overflow input %s initialized cache to %s", input, patcher.prev)
		}
	}
}

func TestMergePatcherNumberRangeRollback(t *testing.T) {
	patcher := &mergePatcher{}
	if _, err := patcher.patch([]byte(`{"keep":{"value":1},"same":true}`)); err != nil {
		t.Fatal(err)
	}
	previous := patcher.prev
	previousPointer := reflect.ValueOf(previous).Pointer()

	for _, input := range []string{
		`{"keep":{"value":1e1000},"same":true}`,
		`{"keep":{"value":1},"same":true,"added":-1e1000}`,
	} {
		if _, err := patcher.patch([]byte(input)); err == nil {
			t.Errorf("overflow update %s succeeded", input)
		}
		if got := reflect.ValueOf(patcher.prev).Pointer(); got != previousPointer {
			t.Errorf("overflow update replaced cache pointer: got %x, want %x", got, previousPointer)
		}
		if !bytes.Equal(patcher.prev, previous) {
			t.Errorf("overflow update changed cache: got %s, want %s", patcher.prev, previous)
		}
	}
}

func assertMergePatchResult(t *testing.T, oldJSON, wantJSON string, patchJSON []byte) {
	t.Helper()
	var oldValue, patchValue, wantValue interface{}
	if err := json.Unmarshal([]byte(oldJSON), &oldValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(patchJSON, &patchValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(wantJSON), &wantValue); err != nil {
		t.Fatal(err)
	}
	gotValue := referenceApplyMergePatch(oldValue, patchValue)
	if !reflect.DeepEqual(gotValue, wantValue) {
		gotBytes, _ := json.Marshal(gotValue)
		t.Fatalf("applied patch = %s, want %s (patch %s)", gotBytes, wantJSON, patchJSON)
	}
}

func assertJSONEqual(t *testing.T, got, want []byte) {
	t.Helper()
	var gotValue, wantValue interface{}
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("invalid got JSON %q: %v", got, err)
	}
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("invalid want JSON %q: %v", want, err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("JSON = %s, want %s", got, want)
	}
}

// referenceApplyMergePatch is deliberately independent of production merge
// helpers so tests can detect errors in either patch generation or application.
func referenceApplyMergePatch(target, patch interface{}) interface{} {
	patchObject, ok := patch.(map[string]interface{})
	if !ok {
		return patch
	}
	targetObject, ok := target.(map[string]interface{})
	if !ok {
		targetObject = make(map[string]interface{})
	}
	for key, patchValue := range patchObject {
		if patchValue == nil {
			delete(targetObject, key)
			continue
		}
		targetObject[key] = referenceApplyMergePatch(targetObject[key], patchValue)
	}
	return targetObject
}
