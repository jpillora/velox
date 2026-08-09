package velox

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
)

// mergePatcher owns the raw previous state. Object levels are decoded lazily
// while diffing, so byte-identical subtrees do not become interface trees.
type mergePatcher struct {
	prev []byte
}

// patch computes a merge patch from cached previous state to modifiedJSON.
// It returns the patch bytes and updates the cache to modifiedJSON.
func (m *mergePatcher) patch(modifiedJSON []byte) ([]byte, error) {
	if m.prev != nil && bytes.Equal(m.prev, modifiedJSON) {
		return []byte(`{}`), nil
	}

	if !json.Valid(modifiedJSON) {
		return nil, errors.New("invalid JSON state")
	}
	if err := validateJSONNumbers(modifiedJSON); err != nil {
		return nil, err
	}
	modified, err := rawObject(modifiedJSON)
	if err != nil {
		return nil, err
	}

	patchBytes := []byte(`{}`)
	if m.prev != nil {
		previous, err := rawObject(m.prev)
		if err != nil {
			return nil, err
		}
		diff, err := rawObjectDiff(previous, modified)
		if err != nil {
			return nil, err
		}
		if len(diff) != 0 {
			patchBytes, err = json.Marshal(diff)
			if err != nil {
				return nil, err
			}
		}
	}

	// The caller may reuse its marshal buffer. Publish the new cache only after
	// every operation that can fail has succeeded.
	m.prev = bytes.Clone(modifiedJSON)
	return patchBytes, nil
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

func scanJSONString(data []byte, start int) int {
	for i := start + 1; i < len(data); i++ {
		switch data[i] {
		case '\\':
			i++
		case '"':
			return i + 1
		}
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
