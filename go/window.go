package velox

import (
	"bytes"
	"encoding/json"
	"errors"
	"unicode/utf8"
)

// fastKeyBytes reports whether a quoted key's body can be taken raw as the
// decoded key: no escapes, no control bytes — which json.Valid rejects inside
// strings — and valid UTF-8, since encoding/json coerces invalid UTF-8 to
// U+FFFD when decoding and raw bytes would disagree with every decoder's view
// of the key. Anything else goes through json.Unmarshal, which validates and
// coerces exactly as the rest of the system does.
func fastKeyBytes(body []byte) bool {
	for _, c := range body {
		if c < 0x20 || c == '\\' {
			return false
		}
	}
	return utf8.Valid(body)
}

// errWindowAbort unwinds a windowed build whose speculation failed to verify.
// A span produced by the containment shortcut is a guess about where a changed
// subtree now ends; the guess is proven only when the recursion bottoms out in
// a leaf whose bytes scan to exactly that end, or a gap scan that lands on it.
// Any failure inside a guessed span must abandon the whole windowed build —
// falling back to a plain scan there would parse a guessed region that can be
// coincidentally valid JSON and silently build the wrong tree.
var errWindowAbort = errors.New("velox: window speculation failed")

// diffWindow describes where two snapshots can differ at all: bytes before
// prefix are identical at identical offsets, and bytes from prevSuffixStart on
// are identical shifted by delta. Everything a build does outside the window
// is offset arithmetic; only inside it does anything get scanned.
type diffWindow struct {
	prefix          int
	prevSuffixStart int
	delta           int
}

func makeWindow(prevBuf, newBuf []byte) diffWindow {
	prefix := commonPrefixLen(prevBuf, newBuf)
	cap := min(len(prevBuf), len(newBuf)) - prefix
	suffix := commonSuffixLen(prevBuf, newBuf, cap)
	return diffWindow{
		prefix:          prefix,
		prevSuffixStart: len(prevBuf) - suffix,
		delta:           len(newBuf) - len(prevBuf),
	}
}

// commonPrefixLen finds the first differing byte via chunked bytes.Equal —
// effectively memcmp speed — dropping to a byte loop only inside the first
// differing chunk.
func commonPrefixLen(a, b []byte) int {
	n := min(len(a), len(b))
	const chunk = 4096
	i := 0
	for i < n {
		e := min(i+chunk, n)
		if !bytes.Equal(a[i:e], b[i:e]) {
			for i < e && a[i] == b[i] {
				i++
			}
			return i
		}
		i = e
	}
	return n
}

// commonSuffixLen is the mirror, capped so prefix and suffix never overlap.
func commonSuffixLen(a, b []byte, cap int) int {
	const chunk = 4096
	s := 0
	for s < cap {
		take := min(chunk, cap-s)
		if !bytes.Equal(a[len(a)-s-take:len(a)-s], b[len(b)-s-take:len(b)-s]) {
			for s < cap && a[len(a)-1-s] == b[len(b)-1-s] {
				s++
			}
			return s
		}
		s += take
	}
	return cap
}

// reusableOutsideWindow reports that the candidate spans lie entirely in the
// window's identical regions, proving byte equality with no comparison at all.
func (w *diffWindow) reusableOutsideWindow(pa, pb, na, nb int) bool {
	if w == nil {
		return false
	}
	if pb <= w.prefix && pa == na && pb == nb {
		return true
	}
	return pa >= w.prevSuffixStart && na == pa+w.delta && nb == pb+w.delta
}

// objEntry is one object-level entry located by absolute offsets: the decoded
// key, its value span [vs, ve), and the quoted key's byte length.
type objEntry struct {
	key    string
	vs, ve int
	klen   int
}

// scanObjectEntriesAt splits the object level opening at a into its entries in
// document order, preserving the scanning-era validation: an empty value span
// is a missing value, not a value.
func scanObjectEntriesAt(buf []byte, a int) ([]objEntry, error) {
	var entries []objEntry
	i := skipJSONSpace(buf, a+1)
	if i < len(buf) && buf[i] == '}' {
		return entries, nil
	}
	for {
		if i >= len(buf) || buf[i] != '"' {
			return nil, errors.New("invalid JSON object")
		}
		keyEnd := scanJSONString(buf, i)
		// an unterminated key scans to the end of the buffer; shorter than a
		// closed empty string means there is nothing valid here
		if keyEnd-i < 2 {
			return nil, errors.New("invalid JSON object")
		}
		var key string
		if body := buf[i+1 : keyEnd-1]; fastKeyBytes(body) {
			key = string(body)
		} else if err := json.Unmarshal(buf[i:keyEnd], &key); err != nil {
			return nil, err
		}
		klen := keyEnd - i
		i = skipJSONSpace(buf, keyEnd)
		if i >= len(buf) || buf[i] != ':' {
			return nil, errors.New("invalid JSON object")
		}
		vs := skipJSONSpace(buf, i+1)
		ve := scanJSONValue(buf, vs)
		if ve == vs {
			return nil, errors.New("invalid JSON object")
		}
		entries = append(entries, objEntry{key: key, vs: vs, ve: ve, klen: klen})
		i = skipJSONSpace(buf, ve)
		if i < len(buf) && buf[i] == '}' {
			return entries, nil
		}
		if i >= len(buf) || buf[i] != ',' {
			return nil, errors.New("invalid JSON object")
		}
		i = skipJSONSpace(buf, i+1)
	}
}

// scanArraySpansAt splits the array level opening at a into element spans.
func scanArraySpansAt(buf []byte, a int) ([][2]int, error) {
	spans := [][2]int{}
	i := skipJSONSpace(buf, a+1)
	if i < len(buf) && buf[i] == ']' {
		return spans, nil
	}
	for {
		if i >= len(buf) {
			return nil, errors.New("invalid JSON array")
		}
		end := scanJSONValue(buf, i)
		if end == i {
			return nil, errors.New("invalid JSON array")
		}
		spans = append(spans, [2]int{i, end})
		i = skipJSONSpace(buf, end)
		if i < len(buf) && buf[i] == ']' {
			return spans, nil
		}
		if i >= len(buf) || buf[i] != ',' {
			return nil, errors.New("invalid JSON array")
		}
		i = skipJSONSpace(buf, i+1)
	}
}

// prevArraySpansOf reconstructs the previous level's element spans from the
// nodes' exact sizes instead of rescanning: reuse is byte-equal whenever the
// tree is marked exact, and encoding/json emits no whitespace. Arithmetic that
// fails to land exactly on the closing bracket returns nil and the caller
// scans instead.
func prevArraySpansOf(prev *mnode, pa, pb int) [][2]int {
	spans := make([][2]int, len(prev.kids))
	off := pa + 1
	for i, kid := range prev.kids {
		spans[i] = [2]int{off, off + kid.size}
		off += kid.size + 1
	}
	if len(prev.kids) == 0 {
		off = pa + 2
	}
	if off != pb {
		return nil
	}
	return spans
}

// prevObjectSpansOf reconstructs an object level's entries in document order
// from the node's recorded order and exact sizes. Levels built from documents
// with duplicate keys record no order and return nil, as does any arithmetic
// that fails to land exactly on the closing brace.
func prevObjectSpansOf(prev *mnode, pa, pb int) []objEntry {
	if prev.order == nil {
		return nil
	}
	entries := make([]objEntry, 0, len(prev.order)/2)
	off := pa + 1
	for k := 0; k+1 < len(prev.order); k += 2 {
		idx := int(prev.order[k])
		klen := int(prev.order[k+1])
		if idx >= len(prev.kids) {
			return nil
		}
		vs := off + klen + 1 // past the quoted key and its colon
		ve := vs + prev.kids[idx].size
		entries = append(entries, objEntry{key: prev.keys[idx], vs: vs, ve: ve, klen: klen})
		off = ve + 1
	}
	if len(prev.order) == 0 {
		off = pa + 2
	}
	if off != pb {
		return nil
	}
	return entries
}

// gapObjectUnits parses the entries lying strictly between two derived anchors.
// u sits on the separator after the last prefix entry (or the level's first
// content byte), v on the separator before the first suffix entry (or the
// level's closing brace). Any structural surprise reports failure and the
// caller abandons the derivation.
func gapObjectUnits(buf []byte, u, v int, hasBefore, hasAfter bool) ([]objEntry, bool) {
	// whatever ends the gap must be the suffix separator or this level's own
	// closing brace; scanJSONValue balances bracket types interchangeably, so
	// without this check a {...] mismatch could pass as an empty level
	closes := func() bool {
		if v >= len(buf) {
			return false
		}
		if hasAfter {
			return buf[v] == ','
		}
		return buf[v] == '}'
	}
	units := []objEntry{}
	pos := u
	if hasBefore {
		if pos == v {
			return units, closes() // shared separator or closing brace: empty window
		}
		if pos >= len(buf) || buf[pos] != ',' {
			return nil, false
		}
		pos++
		if pos >= v {
			return nil, false
		}
	} else if pos == v {
		// nothing before the suffix separator: valid only when the level is
		// genuinely ending here, else the separator at v is dangling
		if hasAfter {
			return nil, false
		}
		return units, closes()
	}
	for {
		if pos >= len(buf) || buf[pos] != '"' {
			return nil, false
		}
		keyEnd := scanJSONString(buf, pos)
		if keyEnd <= pos+1 || keyEnd >= v {
			return nil, false
		}
		var key string
		if body := buf[pos+1 : keyEnd-1]; fastKeyBytes(body) {
			key = string(body)
		} else if json.Unmarshal(buf[pos:keyEnd], &key) != nil {
			return nil, false
		}
		if buf[keyEnd] != ':' {
			return nil, false
		}
		vs := keyEnd + 1
		ve := scanJSONValue(buf, vs)
		if ve <= vs || ve > v {
			return nil, false
		}
		units = append(units, objEntry{key: key, vs: vs, ve: ve, klen: keyEnd - pos})
		if ve == v {
			if hasAfter && buf[v] != ',' {
				return nil, false
			}
			if !hasAfter && buf[v] != '}' {
				return nil, false
			}
			return units, true
		}
		if buf[ve] != ',' {
			return nil, false
		}
		pos = ve + 1
		if pos >= v {
			return nil, false
		}
	}
}

// gapArraySpans is gapObjectUnits for array elements.
func gapArraySpans(buf []byte, u, v int, hasBefore, hasAfter bool) ([][2]int, bool) {
	closes := func() bool {
		if v >= len(buf) {
			return false
		}
		if hasAfter {
			return buf[v] == ','
		}
		return buf[v] == ']'
	}
	spans := [][2]int{}
	pos := u
	if hasBefore {
		if pos == v {
			return spans, closes()
		}
		if pos >= len(buf) || buf[pos] != ',' {
			return nil, false
		}
		pos++
		if pos >= v {
			return nil, false
		}
	} else if pos == v {
		if hasAfter {
			return nil, false
		}
		return spans, closes()
	}
	for {
		end := scanJSONValue(buf, pos)
		if end <= pos || end > v {
			return nil, false
		}
		spans = append(spans, [2]int{pos, end})
		if end == v {
			if hasAfter && buf[v] != ',' {
				return nil, false
			}
			if !hasAfter && buf[v] != ']' {
				return nil, false
			}
			return spans, true
		}
		if buf[end] != ',' {
			return nil, false
		}
		pos = end + 1
		if pos >= v {
			return nil, false
		}
	}
}

// deriveObjectEntries classifies the previous level's entries against the
// window. An entry whose bytes and trailing separator sit inside the common
// prefix is unchanged at the same offsets; one whose leading separator sits
// inside the common suffix is unchanged shifted by delta; the gap between is
// scanned in buf. An entry strictly containing the whole window becomes a
// guessed span — verified by the recursion — flagged through specKey. Entries
// return in document order; ok=false means the caller must scan the level.
func deriveObjectEntries(buf []byte, list []objEntry, na, nb int, w *diffWindow) (entries []objEntry, specKey string, hasSpec, ok bool) {
	p, t, d := w.prefix, w.prevSuffixStart, w.delta
	n := len(list)
	before := 0
	for before < n && list[before].ve+1 <= p {
		before++
	}
	after := n
	for after > before {
		e := list[after-1]
		keyStart := e.vs - e.klen - 1
		if keyStart-1 >= t {
			after--
		} else {
			break
		}
	}
	entries = make([]objEntry, 0, n+4)
	entries = append(entries, list[:before]...)
	if after-before == 1 && list[before].vs < p && t < list[before].ve {
		// one entry's value strictly contains the whole window: its key sits
		// in the prefix and its end in the suffix, so its new span is an
		// arithmetic guess this level need not scan — the recursion both
		// narrows the window and, by bottoming out in a verified leaf or gap
		// scan, proves the guess. Strict bounds: boundary edits scan instead.
		e := list[before]
		e.ve += d
		if e.ve <= e.vs || e.ve > nb-1 {
			return nil, "", false, false
		}
		entries = append(entries, e)
		specKey = e.key
		hasSpec = true
	} else {
		u := na + 1
		if before > 0 {
			u = list[before-1].ve
		}
		v := nb - 1
		if after < n {
			e := list[after]
			v = e.vs - e.klen - 1 - 1 + d
		}
		if v < u || v > nb-1 {
			return nil, "", false, false
		}
		units, unitsOK := gapObjectUnits(buf, u, v, before > 0, after < n)
		if !unitsOK {
			return nil, "", false, false
		}
		entries = append(entries, units...)
	}
	for i := after; i < n; i++ {
		e := list[i]
		e.vs += d
		e.ve += d
		entries = append(entries, e)
	}
	return entries, specKey, hasSpec, true
}

// deriveArraySpans is deriveObjectEntries for arrays: elements outside the
// window keep (or shift) their spans, the gap is scanned, and each new index
// is paired with the previous element it should diff against — identity for
// the prefix, shifted for the suffix, leading-positional inside the window.
func deriveArraySpans(buf []byte, prevSpans [][2]int, na, nb int, w *diffWindow) (spans [][2]int, pairing []int, specIndex int, ok bool) {
	p, t, d := w.prefix, w.prevSuffixStart, w.delta
	n := len(prevSpans)
	before := 0
	for before < n && prevSpans[before][1]+1 <= p {
		before++
	}
	after := n
	for after > before && prevSpans[after-1][0]-1 >= t {
		after--
	}
	spans = make([][2]int, 0, n+4)
	pairing = make([]int, 0, n+4)
	specIndex = -1
	for i := 0; i < before; i++ {
		spans = append(spans, prevSpans[i])
		pairing = append(pairing, i)
	}
	if after-before == 1 && prevSpans[before][0] < p && t < prevSpans[before][1] {
		// one element strictly contains the whole window: an arithmetic guess,
		// proven by the recursion. Strict bounds: boundary edits scan instead.
		s0 := prevSpans[before][0]
		e0 := prevSpans[before][1] + d
		if e0 <= s0 || e0 > nb-1 {
			return nil, nil, -1, false
		}
		specIndex = len(spans)
		spans = append(spans, [2]int{s0, e0})
		pairing = append(pairing, before)
	} else {
		u := na + 1
		if before > 0 {
			u = prevSpans[before-1][1]
		}
		v := nb - 1
		if after < n {
			v = prevSpans[after][0] - 1 + d
		}
		if v < u || v > nb-1 {
			return nil, nil, -1, false
		}
		units, unitsOK := gapArraySpans(buf, u, v, before > 0, after < n)
		if !unitsOK {
			return nil, nil, -1, false
		}
		for i, unit := range units {
			spans = append(spans, unit)
			if pi := before + i; pi < after {
				pairing = append(pairing, pi)
			} else {
				pairing = append(pairing, -1)
			}
		}
	}
	for i := after; i < n; i++ {
		spans = append(spans, [2]int{prevSpans[i][0] + d, prevSpans[i][1] + d})
		pairing = append(pairing, i)
	}
	return spans, pairing, specIndex, true
}

// rootSpanOf trims whitespace instead of scanning: the root value is the
// entire trimmed buffer whenever it is brace-delimited, which is always true
// of encoding/json output. Anything else reports false and the caller takes
// the scanning path, whose validation also rejects trailing junk.
func rootSpanOf(buf []byte) (int, int, bool) {
	a := skipJSONSpace(buf, 0)
	b := len(buf)
	for b > a && isJSONSpace(buf[b-1]) {
		b--
	}
	if a < b && buf[a] == '{' && buf[b-1] == '}' {
		return a, b, true
	}
	return 0, 0, false
}
