package velox

import (
	"encoding/json"
	"reflect"
)

// mergePatcher caches the unmarshaled previous state to avoid
// double-unmarshal on every push cycle. Instead of unmarshaling both
// old and new JSON each time, only the new JSON is unmarshaled.
type mergePatcher struct {
	prev map[string]interface{}
}

// patch computes a merge patch from cached previous state to modifiedJSON.
// It returns the patch bytes and updates the cache to modifiedJSON.
func (m *mergePatcher) patch(modifiedJSON []byte) ([]byte, error) {
	var modified map[string]interface{}
	if err := json.Unmarshal(modifiedJSON, &modified); err != nil {
		return nil, err
	}
	var patchBytes []byte
	if m.prev == nil {
		// first call — no previous state, no meaningful diff
		patchBytes = []byte(`{}`)
	} else {
		diff := objectDiff(m.prev, modified)
		if len(diff) == 0 {
			patchBytes = []byte(`{}`)
		} else {
			var err error
			patchBytes, err = json.Marshal(diff)
			if err != nil {
				return nil, err
			}
		}
	}
	m.prev = modified
	return patchBytes, nil
}

// objectDiff returns only the keys that differ between a and b.
// Deleted keys are represented as nil values per RFC 7386.
func objectDiff(a, b map[string]interface{}) map[string]interface{} {
	diff := make(map[string]interface{})
	// added or changed keys
	for key, bv := range b {
		av, ok := a[key]
		if !ok {
			diff[key] = bv
			continue
		}
		if reflect.TypeOf(av) != reflect.TypeOf(bv) {
			diff[key] = bv
			continue
		}
		switch at := av.(type) {
		case map[string]interface{}:
			bt := bv.(map[string]interface{})
			sub := objectDiff(at, bt)
			if len(sub) > 0 {
				diff[key] = sub
			}
		case []interface{}:
			bt := bv.([]interface{})
			if !sliceEqual(at, bt) {
				diff[key] = bv
			}
		case nil:
			if bv != nil {
				diff[key] = bv
			}
		default:
			// string, float64, bool
			if av != bv {
				diff[key] = bv
			}
		}
	}
	// deleted keys
	for key := range a {
		if _, ok := b[key]; !ok {
			diff[key] = nil
		}
	}
	return diff
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
