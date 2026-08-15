package velox

import (
	"errors"
	"strconv"
)

// validateJSONLeaf reports whether data is one valid JSON value whose numbers
// all fit encoding/json's float64 representation. It exists because the two
// checks it replaces — json.Valid followed by validateJSONNumbers — each scan
// the whole input, and buildLeaf runs them on every changed leaf: on a
// churn-heavy push the two passes together were ~20% of CPU. One pass does
// both jobs.
//
// Parity is load-bearing: accepting anything json.Valid rejects would publish
// state encoding/json could not have produced, and rejecting anything it
// accepts would refuse state the previous implementation served. The
// FuzzValidatorParity fuzzer holds this function to exactly
// json.Valid(data) && validateJSONNumbers(data) == nil.
//
// It is iterative rather than recursive because json.Valid has no depth limit,
// so a hostile MarshalJSON emitting a pathologically deep document must not be
// able to blow the stack here when the standard library would have accepted it.
func validateJSONLeaf(data []byte) error {
	i, err := validateValue(data, skipJSONSpace(data, 0))
	if err != nil {
		return err
	}
	if skipJSONSpace(data, i) != len(data) {
		return errInvalidJSON
	}
	return nil
}

var errInvalidJSON = errors.New("invalid JSON state")

// frame is one open container on the explicit validation stack: '{' or '['.
type validateFrame = byte

// validateValue consumes one JSON value starting at i and returns the offset
// just past it. Containers are tracked on an explicit stack.
func validateValue(data []byte, i int) (int, error) {
	var stack []validateFrame
	// expectValue is the entry state; after a complete value the stack decides
	// whether a separator or a closer comes next.
	for {
		if i >= len(data) {
			return 0, errInvalidJSON
		}
		switch c := data[i]; {
		case c == '{':
			i = skipJSONSpace(data, i+1)
			if i < len(data) && data[i] == '}' {
				i++
			} else {
				// expect the first key immediately
				var err error
				if i, err = validateKeyColon(data, i); err != nil {
					return 0, err
				}
				stack = append(stack, '{')
				continue
			}
		case c == '[':
			i = skipJSONSpace(data, i+1)
			if i < len(data) && data[i] == ']' {
				i++
			} else {
				stack = append(stack, '[')
				continue
			}
		case c == '"':
			var err error
			if i, err = validateString(data, i); err != nil {
				return 0, err
			}
		case c == 't':
			if !hasPrefixAt(data, i, "true") {
				return 0, errInvalidJSON
			}
			i += len("true")
		case c == 'f':
			if !hasPrefixAt(data, i, "false") {
				return 0, errInvalidJSON
			}
			i += len("false")
		case c == 'n':
			if !hasPrefixAt(data, i, "null") {
				return 0, errInvalidJSON
			}
			i += len("null")
		case c == '-' || (c >= '0' && c <= '9'):
			var err error
			if i, err = validateNumber(data, i); err != nil {
				return 0, err
			}
		default:
			return 0, errInvalidJSON
		}
		// one value is complete; unwind separators and closers
		for {
			if len(stack) == 0 {
				return i, nil
			}
			i = skipJSONSpace(data, i)
			if i >= len(data) {
				return 0, errInvalidJSON
			}
			open := stack[len(stack)-1]
			switch data[i] {
			case ',':
				i = skipJSONSpace(data, i+1)
				if open == '{' {
					var err error
					if i, err = validateKeyColon(data, i); err != nil {
						return 0, err
					}
				}
			case '}':
				if open != '{' {
					return 0, errInvalidJSON
				}
				stack = stack[:len(stack)-1]
				i++
				continue
			case ']':
				if open != '[' {
					return 0, errInvalidJSON
				}
				stack = stack[:len(stack)-1]
				i++
				continue
			default:
				return 0, errInvalidJSON
			}
			break
		}
	}
}

// validateKeyColon consumes `"key" :` and leaves i at the value that follows.
func validateKeyColon(data []byte, i int) (int, error) {
	if i >= len(data) || data[i] != '"' {
		return 0, errInvalidJSON
	}
	i, err := validateString(data, i)
	if err != nil {
		return 0, err
	}
	i = skipJSONSpace(data, i)
	if i >= len(data) || data[i] != ':' {
		return 0, errInvalidJSON
	}
	return skipJSONSpace(data, i+1), nil
}

// validateString consumes one string starting at its opening quote. Matching
// encoding/json: control bytes below 0x20 are rejected, escapes are limited to
// the RFC set, \u takes exactly four hex digits, and byte content is otherwise
// unconstrained — json.Valid does not require valid UTF-8, so neither does this.
func validateString(data []byte, i int) (int, error) {
	for i++; i < len(data); {
		switch c := data[i]; {
		case c == '"':
			return i + 1, nil
		case c == '\\':
			if i+1 >= len(data) {
				return 0, errInvalidJSON
			}
			switch data[i+1] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				i += 2
			case 'u':
				if i+6 > len(data) || !isHex(data[i+2]) || !isHex(data[i+3]) || !isHex(data[i+4]) || !isHex(data[i+5]) {
					return 0, errInvalidJSON
				}
				i += 6
			default:
				return 0, errInvalidJSON
			}
		case c < 0x20:
			return 0, errInvalidJSON
		default:
			i++
		}
	}
	return 0, errInvalidJSON
}

// validateNumber consumes one number and checks both its grammar and that it
// fits a float64, folding validateJSONNumbers' range check into the same pass.
func validateNumber(data []byte, i int) (int, error) {
	start := i
	if data[i] == '-' {
		i++
	}
	switch {
	case i < len(data) && data[i] == '0':
		i++
	case i < len(data) && data[i] >= '1' && data[i] <= '9':
		for i < len(data) && data[i] >= '0' && data[i] <= '9' {
			i++
		}
	default:
		return 0, errInvalidJSON
	}
	if i < len(data) && data[i] == '.' {
		i++
		if i >= len(data) || data[i] < '0' || data[i] > '9' {
			return 0, errInvalidJSON
		}
		for i < len(data) && data[i] >= '0' && data[i] <= '9' {
			i++
		}
	}
	if i < len(data) && (data[i] == 'e' || data[i] == 'E') {
		i++
		if i < len(data) && (data[i] == '+' || data[i] == '-') {
			i++
		}
		if i >= len(data) || data[i] < '0' || data[i] > '9' {
			return 0, errInvalidJSON
		}
		for i < len(data) && data[i] >= '0' && data[i] <= '9' {
			i++
		}
	}
	// The grammar is already proven, so ParseFloat can fail here only when a
	// finite JSON number overflows float64 — the same rule encoding/json applies
	// when decoding.
	if _, err := strconv.ParseFloat(string(data[start:i]), 64); err != nil {
		return 0, err
	}
	return i, nil
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func hasPrefixAt(data []byte, i int, s string) bool {
	return len(data)-i >= len(s) && string(data[i:i+len(s)]) == s
}
