package velox

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// parseSyncPath accepts a small JSONPath subset: key.child, ["quoted key"],
// and [0]. A leading $. is optional; an empty path names the whole document.
func parseSyncPath(path string) ([]any, error) {
	if path == "" {
		return nil, nil
	}
	if len(path) > 1024 {
		return nil, fmt.Errorf("velox: invalid selective sync path %q", path)
	}
	if path[0] != '$' {
		if path[0] == '.' || path[0] == '[' {
			path = "$" + path
		} else {
			path = "$." + path
		}
	}
	if len(path) < 3 {
		return nil, fmt.Errorf("velox: invalid selective sync path %q", path)
	}
	var parts []any
	for i := 1; i < len(path); {
		switch path[i] {
		case '.':
			start := i + 1
			i = start
			for i < len(path) && ((path[i] >= 'a' && path[i] <= 'z') || (path[i] >= 'A' && path[i] <= 'Z') || (path[i] >= '0' && path[i] <= '9') || path[i] == '_') {
				i++
			}
			if i == start || (path[start] >= '0' && path[start] <= '9') {
				return nil, fmt.Errorf("velox: invalid selective sync path %q", path)
			}
			parts = append(parts, path[start:i])
		case '[':
			i++
			if i < len(path) && path[i] == '"' {
				start := i
				i++
				for i < len(path) {
					if path[i] == '\\' {
						i += 2
						continue
					}
					if path[i] == '"' {
						break
					}
					i++
				}
				if i >= len(path)-1 || path[i+1] != ']' {
					return nil, fmt.Errorf("velox: invalid selective sync path %q", path)
				}
				var key string
				if err := json.Unmarshal([]byte(path[start:i+1]), &key); err != nil {
					return nil, fmt.Errorf("velox: invalid selective sync path: %w", err)
				}
				parts = append(parts, key)
				i += 2
			} else {
				start := i
				for i < len(path) && path[i] >= '0' && path[i] <= '9' {
					i++
				}
				if start == i || i >= len(path) || path[i] != ']' || (i-start > 1 && path[start] == '0') {
					return nil, fmt.Errorf("velox: invalid selective sync path %q", path)
				}
				index, err := strconv.Atoi(path[start:i])
				if err != nil {
					return nil, fmt.Errorf("velox: invalid selective sync path: %w", err)
				}
				parts = append(parts, index)
				i++
			}
		default:
			return nil, fmt.Errorf("velox: invalid selective sync path %q", path)
		}
	}
	if len(parts) == 0 || strings.ContainsRune(path, 0) {
		return nil, fmt.Errorf("velox: invalid selective sync path %q", path)
	}
	return parts, nil
}

// parseSyncPaths puts path sets in a stable order for wire acknowledgements
// and resume hashes. Empty sets select the whole document.
func parseSyncPaths(paths []string) ([]string, [][]any, error) {
	if len(paths) > 32 {
		return nil, nil, fmt.Errorf("velox: too many selective sync paths (maximum 32)")
	}
	ordered := slices.Clone(paths)
	slices.Sort(ordered)
	parts := make([][]any, 0, len(ordered))
	unique := make([]string, 0, len(ordered))
	for _, path := range ordered {
		if path == "" {
			return nil, nil, fmt.Errorf("velox: an empty path cannot be combined with other paths")
		}
		if len(unique) > 0 && unique[len(unique)-1] == path {
			continue
		}
		parsed, err := parseSyncPath(path)
		if err != nil {
			return nil, nil, err
		}
		for _, part := range parsed {
			if index, ok := part.(int); ok && index > 65535 {
				return nil, nil, fmt.Errorf("velox: array index %d exceeds multi-path limit 65535", index)
			}
		}
		unique, parts = append(unique, path), append(parts, parsed)
	}
	return unique, parts, nil
}

// lookupSyncBody follows one path without materialising a full document.
// The found flag distinguishes a missing path from a JSON null value.
func lookupSyncBody(body []byte, parts []any) (json.RawMessage, bool, error) {
	value := json.RawMessage(body)
	for _, part := range parts {
		value = bytes.TrimSpace(value)
		if len(value) == 0 || string(value) == "null" {
			return nil, false, nil
		}
		switch key := part.(type) {
		case string:
			if value[0] != '{' {
				return nil, false, nil
			}
			var object map[string]json.RawMessage
			if err := json.Unmarshal(value, &object); err != nil {
				return nil, false, err
			}
			var found bool
			value, found = object[key]
			if !found {
				return nil, false, nil
			}
		case int:
			if value[0] != '[' {
				return nil, false, nil
			}
			var array []json.RawMessage
			if err := json.Unmarshal(value, &array); err != nil {
				return nil, false, err
			}
			if key >= len(array) {
				return nil, false, nil
			}
			value = array[key]
		}
	}
	if len(value) == 0 {
		return nil, false, nil
	}
	return value, true, nil
}

// selectSyncBody only decodes one level at a time, and never builds a full
// interface{} tree. A missing path is represented by null.
func selectSyncBody(body []byte, parts []any) (json.RawMessage, error) {
	value, found, err := lookupSyncBody(body, parts)
	if err != nil {
		return nil, err
	}
	if !found {
		return json.RawMessage(`null`), nil
	}
	return value, nil
}

func projectedContainer(part any) any {
	if _, ok := part.(int); ok {
		return []any{}
	}
	return map[string]any{}
}

func insertProjected(node any, parts []any, value json.RawMessage) any {
	if len(parts) == 0 {
		return value
	}
	switch key := parts[0].(type) {
	case string:
		object := node.(map[string]any)
		if len(parts) == 1 {
			object[key] = value
			return object
		}
		child, found := object[key]
		if _, isLeaf := child.(json.RawMessage); isLeaf {
			return object
		}
		if !found {
			child = projectedContainer(parts[1])
		}
		object[key] = insertProjected(child, parts[1:], value)
		return object
	case int:
		array := node.([]any)
		if key >= len(array) {
			array = append(array, make([]any, key-len(array)+1)...)
		}
		if len(parts) == 1 {
			array[key] = value
			return array
		}
		child := array[key]
		if _, isLeaf := child.(json.RawMessage); isLeaf {
			return array
		}
		if child == nil {
			child = projectedContainer(parts[1])
		}
		array[key] = insertProjected(child, parts[1:], value)
		return array
	}
	return node
}

// projectSyncBody reconstructs a sparse document from the selected paths.
// Ancestor selections win over descendants, regardless of input order.
func projectSyncBody(body []byte, paths [][]any) (json.RawMessage, error) {
	var root any = map[string]any{}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return json.RawMessage(`null`), nil
	}
	if trimmed[0] == '[' {
		root = []any{}
	}
	for _, parts := range paths {
		value, found, err := lookupSyncBody(body, parts)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		// lookupSyncBody only finds paths compatible with the source root,
		// so the projected root always has the same container type.
		root = insertProjected(root, parts, value)
	}
	projected, err := json.Marshal(root)
	return json.RawMessage(projected), err
}

func selectiveRoot(path string, body []byte) string {
	sum := sha256.Sum256(append(append([]byte(path), 0), body...))
	return hex.EncodeToString(sum[:16])
}
