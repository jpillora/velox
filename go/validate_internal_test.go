package velox

import (
	"encoding/json"
	"testing"
)

// FuzzValidatorParity pins validateJSONLeaf to exactly the two checks it
// replaced. Any input the pair accepts must be accepted, and any input the
// pair rejects must be rejected — otherwise the single-pass validator changes
// which states velox is willing to publish.
func FuzzValidatorParity(f *testing.F) {
	seeds := []string{
		`{"a":1}`, `null`, `[1,2,3]`, `"s"`, `true`, `false`, `-0.5e+10`,
		`{"a":`, `{"a":1`, `{"a":1}}`, `{"a":1} junk`, `{"a":01}`, `{"a":1,}`,
		`{"a":tru}`, `{"a":"unterminated}`, `{"a":1e1000}`, `{"a":-1e-1000}`,
		`1.`, `.5`, `-`, `01`, `1e`, `1e+`, `"\u12"`, `"\q"`, `"ሴ5"`,
		"\"raw\tcontrol\"", `{"k":"\ud800"}`, `[[[[1]]]]`, `{}`, `[]`, ` { } `,
		`{"a" : [ 1 , {"b" : null} ] }`, `9007199254740993`, `1e309`,
	}
	for _, seed := range seeds {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		fast := validateJSONLeaf(data) == nil
		slow := json.Valid(data) && validateJSONNumbers(data) == nil
		if fast != slow {
			t.Fatalf("validateJSONLeaf(%q) accepted=%v, json.Valid+numbers accepted=%v", data, fast, slow)
		}
	})
}

// FuzzRawEqualParity pins the no-decode comparator to the interface-tree
// comparison it short-circuits. Whenever the fast path decides, its answer
// must be the answer valueEqual would have given.
func FuzzRawEqualParity(f *testing.F) {
	seeds := [][2]string{
		{`1`, `1.0`},
		{`-0`, `0`},
		{`1e2`, `100`},
		{`"a"`, `"a"`},
		{`{"a":1,"b":2}`, `{"b":2,"a":1}`},
		{`{"a":1,"a":2}`, `{"a":2}`},
		{`{"a":1,"a":2}`, `{"a":9,"a":2}`},
		{`{"a":{"b":[1,2]}}`, `{"a":{"b":[1,2.0]}}`},
		{`[1,2,3]`, `[1,2]`},
		{`[]`, `[ ]`},
		{`{}`, `{ }`},
		{`true`, `false`},
		{`null`, `0`},
		{`{"k":"v"}`, `{"k":"w"}`},
		{`{"x":1,"y":2}`, `{"x":1}`},
		{` {"a":1} `, `{"a":1}`},
	}
	for _, seed := range seeds {
		f.Add(seed[0], seed[1])
	}
	f.Fuzz(func(t *testing.T, a, b string) {
		if validateJSONLeaf([]byte(a)) != nil || validateJSONLeaf([]byte(b)) != nil {
			t.Skip()
		}
		equal, decided := rawEqualFast([]byte(a), []byte(b))
		if !decided {
			return
		}
		var av, bv any
		if json.Unmarshal([]byte(a), &av) != nil || json.Unmarshal([]byte(b), &bv) != nil {
			t.Skip()
		}
		if want := valueEqual(av, bv); equal != want {
			t.Fatalf("rawEqualFast(%q, %q) = %v, valueEqual = %v", a, b, equal, want)
		}
	})
}

func TestRawEqualFastDecidesCommonCases(t *testing.T) {
	cases := []struct {
		a, b    string
		equal   bool
		decided bool
	}{
		{`1`, `1.0`, true, true},
		{`-0`, `0`, true, true},
		{`5`, `6`, false, true},
		{`"a"`, `"b"`, false, true},
		{`"a"`, `1`, false, true},
		{`true`, `false`, false, true},
		{`null`, `{}`, false, true},
		{`{"a":1}`, `{"a":2}`, false, true},
		{`{"a":1,"b":2}`, `{"a":1,"b":2.0}`, true, true},
		{`[1,2,3]`, `[1,2,4]`, false, true},
		{`[1,2,3]`, `[1,2]`, false, true},
		{`{"a":1,"b":2}`, `{"b":2,"a":1}`, false, false}, // reorder: fallback
		{`{"a":1,"a":2}`, `{"a":9,"a":2}`, true, true},   // dup overridden pair
		{`{"x":1,"y":2}`, `{"x":1}`, false, false},       // count mismatch: fallback
		{`"a"`, `"a"`, true, true},
	}
	for _, c := range cases {
		equal, decided := rawEqualFast([]byte(c.a), []byte(c.b))
		if decided != c.decided || (decided && equal != c.equal) {
			t.Errorf("rawEqualFast(%q, %q) = (%v, %v), want (%v, %v)", c.a, c.b, equal, decided, c.equal, c.decided)
		}
	}
}

func TestValidateJSONLeafMatchesReference(t *testing.T) {
	valid := []string{
		`{"a":1}`, `null`, `true`, `false`, `0`, `-0.5`, `1e10`, `1E+2`, `[]`,
		`{}`, `"escaped \" \\ \/ \b \f \n \r \t é"`, `[1,[2,[3]]]`,
		` { "a" : [ true , null ] } `, `"\ud800"`, `1.25e-3`,
	}
	for _, v := range valid {
		if err := validateJSONLeaf([]byte(v)); err != nil {
			t.Errorf("validateJSONLeaf(%q) = %v, want nil", v, err)
		}
	}
	invalid := []string{
		``, `{`, `}`, `{"a"}`, `{"a":}`, `{"a":1,}`, `{,}`, `[1,]`, `[,1]`,
		`01`, `1.`, `.5`, `-`, `+1`, `1e`, `1e+`, `tru`, `nul`, `falsey`,
		`"unterminated`, `"\q"`, `"\u12"`, "\"a\tb\"", `{"a":1}{"b":2}`,
		`{"a":1} x`, `1e1000`, `[1 2]`, `{"a" 1}`, `{"a":1 "b":2}`,
	}
	for _, v := range invalid {
		if err := validateJSONLeaf([]byte(v)); err == nil {
			t.Errorf("validateJSONLeaf(%q) = nil, want error", v)
		}
	}
}
