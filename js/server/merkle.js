//A merkle tree over the state's encoded form, mirroring go/merkle.go.
//
//The two servers deliberately do NOT need to agree on a hash function: hashes
//are server-internal, and clients treat a root hash as an opaque resume token
//they echo back. That keeps cross-language JSON canonicalisation — number
//formatting, -0, 1e21, unicode escaping — out of the protocol entirely.
//
//The tree is built by scanning one JSON.stringify of the document, exactly as
//the Go builder scans one json.Marshal. The previous implementation instead
//re-stringified every subtree at every visited level to compare against the
//node's retained encoding — roughly three full-document serialisations per
//push before the v2 machinery added two more passes of its own.
//
//Node granularity is controlled by a size threshold. A subtree encoding to
//fewer bytes than the threshold becomes an opaque leaf that diffs whole, which
//keeps node count proportional to the document's spine rather than to every
//scalar in it. Only leaves retain bytes, and always as flat copies: interior
//nodes carry just a size, so retaining a version's root costs the changed
//path, not — as the retained per-level encodings used to — a whole document
//per version.
const crypto = require("crypto");
const jsonmergepatch = require("json-merge-patch");

const LEAF = 0;
const OBJECT = 1;
const ARRAY = 2;

function hashOf(kind, parts) {
  const digest = crypto.createHash("sha256");
  digest.update(Buffer.from([kind]));
  for (const part of parts) {
    digest.update(part);
  }
  return digest.digest().subarray(0, 16).toString("hex");
}

//Scanning operates on Buffers rather than strings: byte loads from a Buffer
//JIT-compile to tight monomorphic loops, and equality between spans is one
//native memcmp via Buffer.compare, where the string equivalents allocate
//sliced views and walk UTF-16 units. Leaves decode their span back to a
//string with toString, which is also what guarantees a flat copy — a sliced
//string would pin the whole snapshot for as long as any version referencing
//the leaf survives in the history.
const QUOTE = 34;
const BACKSLASH = 92;
const OPEN_BRACE = 123;
const CLOSE_BRACE = 125;
const OPEN_BRACKET = 91;
const CLOSE_BRACKET = 93;
const COMMA = 44;

function skipSpace(buf, i) {
  while (i < buf.length) {
    const c = buf[i];
    if (c === 32 || c === 9 || c === 10 || c === 13) i++;
    else break;
  }
  return i;
}

//scanString returns the offset just past the string opening at i. A plain
//byte loop beats Buffer.indexOf here: JSON documents hold thousands of short
//strings, and a native call per token costs more than walking the bytes in
//JIT-compiled code — profiling put the indexOf variant's call overhead alone
//above the entire scan.
function scanString(buf, i) {
  for (let j = i + 1; j < buf.length; j++) {
    const c = buf[j];
    if (c === BACKSLASH) j++;
    else if (c === QUOTE) return j + 1;
  }
  return buf.length;
}

//scanValue returns the offset just past the value starting at i. String
//skipping is inlined so the container walk is one tight loop.
function scanValue(buf, i) {
  if (i >= buf.length) return i;
  const c = buf[i];
  if (c === QUOTE) return scanString(buf, i);
  if (c === OPEN_BRACE || c === OPEN_BRACKET) {
    let depth = 0;
    for (let j = i; j < buf.length; j++) {
      const ch = buf[j];
      if (ch === QUOTE) {
        for (j++; j < buf.length; j++) {
          const sc = buf[j];
          if (sc === BACKSLASH) j++;
          else if (sc === QUOTE) break;
        }
      } else if (ch === OPEN_BRACE || ch === OPEN_BRACKET) depth++;
      else if (ch === CLOSE_BRACE || ch === CLOSE_BRACKET) {
        depth--;
        if (depth === 0) return j + 1;
      }
    }
    return buf.length;
  }
  for (let j = i; j < buf.length; j++) {
    const ch = buf[j];
    if (ch === COMMA || ch === CLOSE_BRACE || ch === CLOSE_BRACKET || ch === 32 || ch === 9 || ch === 10 || ch === 13) return j;
  }
  return buf.length;
}

//scanObjectLevel splits one object level into [key, valueStart, valueEnd]
//entries in document order, without descending into the values.
function scanObjectLevel(buf, a) {
  const entries = [];
  let i = skipSpace(buf, a + 1);
  if (buf[i] === CLOSE_BRACE) return entries;
  for (;;) {
    const keyEnd = scanString(buf, i);
    //keys without escapes decode as their own content
    let escaped = false;
    for (let k = i + 1; k < keyEnd - 1; k++) {
      if (buf[k] === BACKSLASH) {
        escaped = true;
        break;
      }
    }
    const key = escaped
      ? JSON.parse(buf.toString("utf8", i, keyEnd))
      : buf.toString("utf8", i + 1, keyEnd - 1);
    const klen = keyEnd - i;
    i = skipSpace(buf, keyEnd);
    const vs = skipSpace(buf, i + 1); //past ':'
    const ve = scanValue(buf, vs);
    entries.push([key, vs, ve, klen]);
    i = skipSpace(buf, ve);
    if (buf[i] === CLOSE_BRACE) return entries;
    i = skipSpace(buf, i + 1); //past ','
  }
}

//scanArrayLevel splits one array level into [start, end] element spans.
function scanArrayLevel(buf, a) {
  const spans = [];
  let i = skipSpace(buf, a + 1);
  if (buf[i] === CLOSE_BRACKET) return spans;
  for (;;) {
    const ve = scanValue(buf, i);
    spans.push([i, ve]);
    i = skipSpace(buf, ve);
    if (buf[i] === CLOSE_BRACKET) return spans;
    i = skipSpace(buf, i + 1); //past ','
  }
}

//prevArraySpans reconstructs the previous level's element spans from the
//nodes' sizes instead of rescanning the previous document: reuse is byte-equal
//only, so every node's size is the exact byte length of its encoding, and
//JSON.stringify emits no whitespace. Anything surprising — hand-written input
//with spaces, say — fails the closing-bracket check and falls back to a scan.
function prevArraySpans(prev, prevBuf, pa, pb) {
  const spans = new Array(prev.kids.length);
  let off = pa + 1;
  for (let i = 0; i < prev.kids.length; i++) {
    const size = prev.kids[i].size;
    spans[i] = [off, off + size];
    off += size + 1;
  }
  if (prev.kids.length === 0) off = pa + 2;
  if (off !== pb) return scanArrayLevel(prevBuf, pa);
  return spans;
}

//toBuf accepts the string form for convenience — tests, mostly — while the
//server hands over Buffers it retains between pushes, so the previous
//document is never re-encoded.
function toBuf(x) {
  return typeof x === "string" ? Buffer.from(x, "utf8") : x;
}

//spanEqual is one native memcmp between the two documents' byte ranges.
function spanEqual(prevBuf, pa, pb, buf, a, b) {
  return pb - pa === b - a && prevBuf.compare(buf, a, b, pa, pb) === 0;
}

//commonPrefixLen finds the first differing byte via native chunked compares,
//dropping to a byte loop only inside the first differing chunk.
function commonPrefixLen(a, b) {
  const n = Math.min(a.length, b.length);
  const CHUNK = 4096;
  let base = 0;
  while (base < n) {
    const end = Math.min(base + CHUNK, n);
    if (a.compare(b, base, end, base, end) !== 0) {
      let i = base;
      while (i < end && a[i] === b[i]) i++;
      return i;
    }
    base = end;
  }
  return n;
}

//commonSuffixLen is the mirror, capped so prefix and suffix never overlap.
function commonSuffixLen(a, b, cap) {
  const CHUNK = 4096;
  let s = 0;
  while (s < cap) {
    const take = Math.min(CHUNK, cap - s);
    if (a.compare(b, b.length - s - take, b.length - s, a.length - s - take, a.length - s) !== 0) {
      while (s < cap && a[a.length - 1 - s] === b[b.length - 1 - s]) s++;
      return s;
    }
    s += take;
  }
  return cap;
}

//window describes where the two documents can differ at all: bytes before
//prefix are identical at identical offsets, and bytes from prevSuffixStart on
//are identical shifted by delta. Everything the build does outside the window
//is arithmetic; only inside it does anything get scanned.
function makeWindow(prevBuf, buf) {
  const prefix = commonPrefixLen(prevBuf, buf);
  const cap = Math.min(prevBuf.length, buf.length) - prefix;
  const suffix = commonSuffixLen(prevBuf, buf, cap);
  return {
    prefix,
    prevSuffixStart: prevBuf.length - suffix,
    delta: buf.length - prevBuf.length
  };
}

//prevObjectSpans reconstructs an object level's child spans by key from the
//node's recorded document order and exact sizes, without rescanning the
//previous document. Nodes built from documents with duplicate keys record no
//order and return null, as does any arithmetic that fails to land exactly on
//the closing brace — the caller then scans.
function prevObjectSpans(prev, prevBuf, pa, pb) {
  if (!prev.order) return null;
  const spans = new Map();
  let off = pa + 1;
  for (let k = 0; k < prev.order.length; k += 2) {
    const idx = prev.order[k];
    const klen = prev.order[k + 1];
    const vs = off + klen + 1; //past the quoted key and its colon
    const ve = vs + prev.kids[idx].size;
    spans.set(prev.keys[idx], [vs, ve, klen]);
    off = ve + 1;
  }
  if (prev.order.length === 0) off = pa + 2;
  if (off !== pb) return null;
  return spans;
}

//buildRoot returns the tree for one encoded document, reusing prev wherever
//the encoding is unchanged. A reused node is shared by both versions, so
//retaining an old root costs only the nodes along the paths that changed.
//The root is always expanded however small it is: a leaf root could only ever
//be replaced wholesale, and no patch can express that.
//rootSpan trims whitespace instead of scanning the document: the root value
//is the entire trimmed buffer whenever it is brace-delimited, which is always
//true of JSON.stringify output. Anything else pays the one full scan.
function rootSpan(buf) {
  const a = skipSpace(buf, 0);
  let b = buf.length;
  while (b > a) {
    const c = buf[b - 1];
    if (c === 32 || c === 9 || c === 10 || c === 13) b--;
    else break;
  }
  if (buf[a] === OPEN_BRACE && buf[b - 1] === CLOSE_BRACE) return [a, b];
  return [a, scanValue(buf, a)];
}

function buildRoot(prev, prevJson, json, leafSize, stats) {
  const buf = toBuf(json);
  const prevBuf = prevJson ? toBuf(prevJson) : null;
  if (prev && prevBuf && prevBuf.equals(buf)) return prev;
  const [a, b] = rootSpan(buf);
  let pa = -1;
  let pb = -1;
  if (prev && prevBuf) {
    [pa, pb] = rootSpan(prevBuf);
    const win = makeWindow(prevBuf, buf);
    try {
      return buildObjectAt(prev, prevBuf, pa, pb, buf, a, b, leafSize, stats, win, false);
    } catch (err) {
      if (err !== windowAbort) throw err;
      //speculation failed somewhere; rebuild with plain scanning
    }
  }
  return buildObjectAt(prev, prevBuf, pa, pb, buf, a, b, leafSize, stats, null, false);
}

//windowAbort unwinds a windowed build whose speculation failed to verify.
//A span produced by the containment shortcut is a guess about where a changed
//subtree now ends; the guess is proven only when the recursion bottoms out in
//a leaf whose bytes scan to exactly that end, or a gap scan that lands on it.
//Any failure inside a guessed span must abandon the whole windowed build —
//falling back to a plain scan there would parse a guessed region that can be
//coincidentally valid JSON and silently build the wrong tree.
const windowAbort = new Error("velox: window speculation failed");

//reusableOutsideWindow reports that the candidate spans lie entirely in the
//window's identical regions, proving byte equality with no comparison at all.
function reusableOutsideWindow(win, pa, pb, a, b) {
  if (!win) return false;
  if (pb <= win.prefix && pa === a && pb === b) return true;
  return pa >= win.prevSuffixStart && a === pa + win.delta && b === pb + win.delta;
}

function buildAt(prev, prevBuf, pa, pb, buf, a, b, leafSize, stats, win, speculative) {
  //unchanged encoding: reuse the node outright
  if (prev && pa >= 0 && (reusableOutsideWindow(win, pa, pb, a, b) || spanEqual(prevBuf, pa, pb, buf, a, b))) {
    return prev;
  }
  const c = buf[a];
  if (c === OPEN_BRACE && b - a >= leafSize) {
    return buildObjectAt(prev, prevBuf, pa, pb, buf, a, b, leafSize, stats, win, speculative);
  }
  if (c === OPEN_BRACKET && b - a >= leafSize) {
    return buildArrayAt(prev, prevBuf, pa, pb, buf, a, b, leafSize, stats, win, speculative);
  }
  //a guessed span is only a leaf if its bytes really are one value ending
  //exactly where the guess says — this scan is the speculation's proof, and
  //it is small because leaves are below the threshold
  if (speculative && scanValue(buf, a) !== b) {
    throw windowAbort;
  }
  //toString both decodes and forces a flat copy, so the leaf never pins the
  //snapshot buffer. size is the encoded byte length, which keeps the span
  //arithmetic in prevArraySpans exact.
  const raw = buf.toString("utf8", a, b);
  stats.created++;
  return {kind: LEAF, raw, size: b - a, hash: hashOf(LEAF, [raw])};
}

//scanUnitsBetween parses the children lying strictly between two derived
//anchors — the last child known to sit in the common prefix and the first
//known to sit in the common suffix. u sits on the separator after the prefix
//child (or on the level's first content byte), v on the separator before the
//suffix child (or on the level's closing bracket). Any structural surprise
//returns null and the caller rescans the whole level.
function scanUnitsBetween(buf, u, v, hasBefore, hasAfter, parseUnit) {
  //whatever ends the gap must be the suffix separator or a closing bracket;
  //scanValue balances bracket types interchangeably, so without this check a
  //{...] mismatch could pass as an empty level
  const closes = () =>
    hasAfter ? buf[v] === COMMA : buf[v] === CLOSE_BRACE || buf[v] === CLOSE_BRACKET;
  const units = [];
  let pos = u;
  if (hasBefore) {
    if (pos === v) {
      //u and v coincide: the shared separator between the prefix and suffix
      //children, or the closing bracket — no window children at all
      return closes() ? units : null;
    }
    if (buf[pos] !== COMMA) return null;
    pos++;
    if (pos >= v) return null; //",," or ",]" — not valid JSON
  } else if (pos === v) {
    //nothing before the suffix separator: valid only when the level genuinely
    //ends here, else the byte at v is a dangling separator
    return hasAfter || !closes() ? null : units;
  }
  for (;;) {
    const unit = parseUnit(buf, pos);
    if (!unit) return null;
    const e = unit[0];
    if (e <= pos || e > v) return null;
    units.push(unit);
    if (e === v) {
      //the last window child runs flush into the suffix separator or the
      //level's closing bracket
      if (hasAfter && buf[v] !== COMMA) return null;
      if (!hasAfter && buf[v] !== CLOSE_BRACKET && buf[v] !== CLOSE_BRACE) return null;
      return units;
    }
    if (buf[e] !== COMMA) return null;
    pos = e + 1;
    if (pos >= v) return null;
  }
}

function parseArrayUnit(buf, pos) {
  const e = scanValue(buf, pos);
  return e > pos ? [e, pos, e] : null;
}

function parseObjectUnit(buf, pos) {
  if (buf[pos] !== QUOTE) return null;
  const keyEnd = scanString(buf, pos);
  let escaped = false;
  for (let k = pos + 1; k < keyEnd - 1; k++) {
    if (buf[k] === BACKSLASH) {
      escaped = true;
      break;
    }
  }
  const key = escaped
    ? JSON.parse(buf.toString("utf8", pos, keyEnd))
    : buf.toString("utf8", pos + 1, keyEnd - 1);
  if (buf[keyEnd] !== 58) return null; //':'
  const vs = keyEnd + 1;
  const ve = scanValue(buf, vs);
  if (ve <= vs) return null;
  return [ve, key, vs, ve, keyEnd - pos];
}

function buildObjectAt(prev, prevBuf, pa, pb, buf, a, b, leafSize, stats, win, speculative) {
  const prevIsObject = !!prev && prev.kind === OBJECT;
  let prevSpans = null;
  if (prevIsObject && pa >= 0 && prevBuf[pa] === OPEN_BRACE) {
    prevSpans = prevObjectSpans(prev, prevBuf, pa, pb);
    if (!prevSpans) {
      prevSpans = new Map();
      for (const [key, vs, ve, klen] of scanObjectLevel(prevBuf, pa)) {
        prevSpans.set(key, [vs, ve, klen]); //duplicates resolve last-wins
      }
    }
  }
  //derive this level's entries from the window: entries outside it keep their
  //prev offsets (or shift by delta), and only the gap between is scanned. A
  //previous level with duplicate keys recorded no order and cannot derive —
  //its collapsed map no longer mirrors the document's physical layout.
  let entries = null;
  let specKey = null;
  if (win && prevSpans && prev.order) {
    const derived = deriveObjectEntries(prev, prevSpans, buf, a, b, win);
    if (derived) {
      entries = derived.entries;
      specKey = derived.specKey;
    }
  }
  if (!entries) {
    //a plain scan inside a guessed span could accept a wrong region, so a
    //failed derivation under speculation abandons the windowed build instead
    if (speculative) throw windowAbort;
    entries = scanObjectLevel(buf, a);
  }
  //duplicates resolve last-wins, matching every JSON decoder here
  const byName = new Map();
  for (const e of entries) byName.set(e[0], e);
  const keys = [...byName.keys()].sort();
  const kids = new Array(keys.length);
  const parts = [];
  let shared = prevIsObject && prev.keys.length === keys.length;
  for (let i = 0; i < keys.length; i++) {
    const key = keys[i];
    const e = byName.get(key);
    const prevKid = prevIsObject ? prev.byKey[key] : null;
    const ps = prevSpans && prevSpans.get(key);
    const kid = buildAt(prevKid || null, prevBuf, ps ? ps[0] : -1, ps ? ps[1] : -1, buf, e[1], e[2], leafSize, stats, win, key === specKey);
    kids[i] = kid;
    parts.push(key, kid.hash);
    if (shared && (prev.keys[i] !== key || prev.kids[i] !== kid)) shared = false;
  }
  if (shared) return prev;
  const byKey = Object.create(null);
  const index = new Map();
  for (let i = 0; i < keys.length; i++) {
    byKey[keys[i]] = kids[i];
    index.set(keys[i], i);
  }
  //document order and exact key lengths, so the next build can reconstruct
  //this level's spans without scanning; duplicate keys record nothing
  let order = null;
  if (entries.length === keys.length) {
    order = new Array(entries.length * 2);
    for (let i = 0; i < entries.length; i++) {
      order[i * 2] = index.get(entries[i][0]);
      order[i * 2 + 1] = entries[i][3];
    }
  }
  stats.created++;
  return {kind: OBJECT, size: b - a, keys, kids, byKey, order, hash: hashOf(OBJECT, parts)};
}

//deriveObjectEntries classifies the previous level's entries against the
//window. An entry whose bytes and trailing separator sit before the common
//prefix ends is unchanged at the same offsets; one whose leading separator
//sits inside the common suffix is unchanged shifted by delta; the gap between
//is scanned. Entries are returned in document order.
function deriveObjectEntries(prev, prevSpans, buf, a, b, win) {
  const P = win.prefix;
  const T = win.prevSuffixStart;
  const D = win.delta;
  const list = [...prevSpans.entries()]; //doc order: the Map was built that way
  const n = list.length;
  let before = 0;
  while (before < n && list[before][1][1] + 1 <= P) before++;
  let after = n;
  while (after > before) {
    const span = list[after - 1][1];
    const keyStart = span[0] - span[2] - 1;
    if (keyStart - 1 >= T) after--;
    else break;
  }
  const entries = [];
  for (let i = 0; i < before; i++) {
    const [key, span] = list[i];
    entries.push([key, span[0], span[1], span[2]]);
  }
  let specKey = null;
  if (after - before === 1 && list[before][1][0] < P && T < list[before][1][1]) {
    //one entry's value strictly contains the whole window: its key sits in
    //the prefix and its end in the suffix, so its new span is an arithmetic
    //guess and this level needs no scanning — the recursion both narrows the
    //window and, by bottoming out in a verified leaf or gap scan, proves the
    //guess. Strict bounds: boundary edits take the scanned path.
    const [key, span] = list[before];
    const e0 = span[1] + D;
    if (e0 <= span[0] || e0 > b - 1) return null;
    entries.push([key, span[0], e0, span[2]]);
    specKey = key;
  } else {
    const u = before > 0 ? list[before - 1][1][1] : a + 1;
    const v = after < n ? list[after][1][0] - list[after][1][2] - 1 - 1 + D : b - 1;
    if (v < u || v > b - 1) return null;
    const units = scanUnitsBetween(buf, u, v, before > 0, after < n, parseObjectUnit);
    if (!units) return null;
    for (const unit of units) {
      entries.push([unit[1], unit[2], unit[3], unit[4]]);
    }
  }
  for (let i = after; i < n; i++) {
    const [key, span] = list[i];
    entries.push([key, span[0] + D, span[1] + D, span[2]]);
  }
  return {entries, specKey};
}

function buildArrayAt(prev, prevBuf, pa, pb, buf, a, b, leafSize, stats, win, speculative) {
  const prevIsArray = !!prev && prev.kind === ARRAY;
  let prevSpans = null;
  if (prevIsArray && pa >= 0 && prevBuf[pa] === OPEN_BRACKET) {
    prevSpans = prevArraySpans(prev, prevBuf, pa, pb);
  }
  //derive this level's spans and their pairing to previous children from the
  //window; fall back to a full scan (positional pairing) when it cannot apply
  let spans = null;
  let pairing = null;
  let specIndex = -1;
  if (win && prevSpans) {
    const derived = deriveArraySpans(prevSpans, buf, a, b, win);
    if (derived) {
      spans = derived.spans;
      pairing = derived.pairing;
      specIndex = derived.specIndex;
    }
  }
  if (!spans) {
    //as in buildObjectAt: never plain-scan inside a guessed span
    if (speculative) throw windowAbort;
    spans = scanArrayLevel(buf, a);
  }
  const kids = new Array(spans.length);
  const parts = [];
  let shared = prevIsArray && prev.kids.length === spans.length;
  for (let i = 0; i < spans.length; i++) {
    const pi = pairing ? pairing[i] : i;
    const prevKid = prevIsArray && pi >= 0 && pi < prev.kids.length ? prev.kids[pi] : null;
    const ps = prevSpans && pi >= 0 && pi < prevSpans.length ? prevSpans[pi] : null;
    const kid = buildAt(prevKid, prevBuf, ps ? ps[0] : -1, ps ? ps[1] : -1, buf, spans[i][0], spans[i][1], leafSize, stats, win, i === specIndex);
    kids[i] = kid;
    parts.push(kid.hash);
    if (shared && prev.kids[i] !== kid) shared = false;
  }
  if (shared) return prev;
  stats.created++;
  return {kind: ARRAY, size: b - a, kids, hash: hashOf(ARRAY, parts)};
}

//deriveArraySpans is deriveObjectEntries for arrays: elements outside the
//window keep (or shift) their spans, the gap is scanned, and each new index
//is paired with the previous element it should diff against — identity for
//the prefix, shifted for the suffix, leading-positional inside the window.
function deriveArraySpans(prevSpans, buf, a, b, win) {
  const P = win.prefix;
  const T = win.prevSuffixStart;
  const D = win.delta;
  const n = prevSpans.length;
  let before = 0;
  while (before < n && prevSpans[before][1] + 1 <= P) before++;
  let after = n;
  while (after > before && prevSpans[after - 1][0] - 1 >= T) after--;
  const spans = [];
  const pairing = [];
  for (let i = 0; i < before; i++) {
    spans.push(prevSpans[i]);
    pairing.push(i);
  }
  let specIndex = -1;
  if (after - before === 1 && prevSpans[before][0] < P && T < prevSpans[before][1]) {
    //one element strictly contains the whole window: its start sits in the
    //prefix and its end in the suffix, so its new span is an arithmetic guess
    //and this level needs no scanning — the recursion both narrows the window
    //and, by bottoming out in a verified leaf or gap scan, proves the guess.
    //Strictness matters: an edit touching the element's first or last byte is
    //ambiguous between changing the element and changing its neighbourhood,
    //and must take the scanned path.
    const s0 = prevSpans[before][0];
    const e0 = prevSpans[before][1] + D;
    if (e0 <= s0 || e0 > b - 1) return null;
    specIndex = spans.length;
    spans.push([s0, e0]);
    pairing.push(before);
  } else {
    const u = before > 0 ? prevSpans[before - 1][1] : a + 1;
    const v = after < n ? prevSpans[after][0] - 1 + D : b - 1;
    if (v < u || v > b - 1) return null;
    const units = scanUnitsBetween(buf, u, v, before > 0, after < n, parseArrayUnit);
    if (!units) return null;
    for (let i = 0; i < units.length; i++) {
      spans.push([units[i][1], units[i][2]]);
      const pi = before + i;
      pairing.push(pi < after ? pi : -1);
    }
  }
  for (let i = after; i < n; i++) {
    spans.push([prevSpans[i][0] + D, prevSpans[i][1] + D]);
    pairing.push(i);
  }
  return {spans, pairing, specIndex};
}

//encodeNode reconstructs a subtree's encoded form from leaf raws. It is only
//called to emit a subtree as an operation value, bounded by the patch size.
function encodeNode(n) {
  if (n.kind === LEAF) return n.raw;
  if (n.kind === ARRAY) {
    let out = "[";
    for (let i = 0; i < n.kids.length; i++) {
      if (i > 0) out += ",";
      out += encodeNode(n.kids[i]);
    }
    return out + "]";
  }
  let out = "{";
  for (let i = 0; i < n.keys.length; i++) {
    if (i > 0) out += ",";
    out += JSON.stringify(n.keys[i]) + ":" + encodeNode(n.kids[i]);
  }
  return out + "}";
}

function valueOf(n) {
  return JSON.parse(encodeNode(n));
}

//Internally an operation's value is the raw encoded string of the subtree it
//carries — emission never parses or re-encodes a document fragment. The wire
//form is produced by splicing those raws (serializeOps), and mergePatchFromOps
//assembles the v2 patch string the same way.
function opSize(op) {
  let total = 8 + JSON.stringify(op[1]).length;
  if (op[0] === "s") total += 1 + op[2].length;
  else if (op[0] === "n") total += 4;
  else if (op[0] === "x") total += 8 + (op.length > 4 ? op[4].length : 0);
  return total;
}

function serializeOps(ops) {
  let out = "[";
  for (let i = 0; i < ops.length; i++) {
    const op = ops[i];
    if (i > 0) out += ",";
    out += '["' + op[0] + '",' + JSON.stringify(op[1]);
    if (op[0] === "s") out += "," + op[2];
    else if (op[0] === "n") out += "," + op[2];
    else if (op[0] === "x") {
      out += "," + op[2] + "," + op[3];
      if (op.length > 4) out += "," + op[4];
    }
    out += "]";
  }
  return out + "]";
}

//diff emits the operations turning tree a into tree b. Cost is proportional to
//what changed: a subtree shared by both trees is dismissed on identity.
//
//arrayOps selects the granularity. With it set (protocol v3), arrays diff per
//index with splices for length changes. With it clear, any change inside an
//array collapses to a single whole-array assignment — all an RFC 7386 merge
//patch can express — which is how the v2 projection is derived from the same
//trees rather than by a second document-wide diff.
function diff(a, b, arrayOps) {
  return JSON.parse(serializeOps(diffOps(a, b, arrayOps)));
}

//collapse replaces the operations emitted for one subtree with a single
//assignment of it, whenever describing the change has grown more expensive than
//sending the thing itself. The root is exempt: an assignment there would have
//an empty path, and no applier can replace the document it was handed.
function collapse(a, b, path, ops, savepoint) {
  if (path.length === 0 || ops.length === savepoint) return;
  let emitted = 0;
  for (let i = savepoint; i < ops.length; i++) {
    emitted += opSize(ops[i]);
  }
  if (emitted <= b.size) return;
  ops.length = savepoint;
  ops.push(["s", path.slice(), encodeNode(b)]);
}

//replacement assigns b over a. A v3 "s" is a true assignment, so it just
//carries b. An RFC 7386 merge patch cannot replace an object wholesale —
//placing one there merges it, stranding the keys a had and b does not — so for
//the v2 projection a changed object leaf carries a merge fragment instead.
function replacement(a, b, path, ops, arrayOps) {
  const braw = encodeNode(b);
  if (arrayOps || !a) {
    ops.push(["s", path.slice(), braw]);
    return;
  }
  //v2 object-over-object replacement carries a merge fragment instead; only
  //this path decodes, and only the leaf-sized values involved
  if (braw[0] === "{") {
    const araw = encodeNode(a);
    if (araw[0] === "{") {
      const fragment = jsonmergepatch.generate(JSON.parse(araw), JSON.parse(braw));
      if (fragment !== undefined) {
        ops.push(["s", path.slice(), JSON.stringify(fragment)]);
      }
      return;
    }
  }
  ops.push(["s", path.slice(), braw]);
}

function walk(a, b, path, ops, arrayOps) {
  if (a === b || a.hash === b.hash) return;
  if (a.kind !== b.kind || a.kind === LEAF) {
    replacement(a, b, path, ops, arrayOps);
    return;
  }
  const savepoint = ops.length;
  if (a.kind === OBJECT) {
    for (const key of a.keys) {
      if (!(key in b.byKey)) {
        ops.push(["d", path.concat(key)]);
      }
    }
    for (let i = 0; i < b.keys.length; i++) {
      const key = b.keys[i];
      const previous = a.byKey[key];
      path.push(key);
      if (previous === undefined) {
        ops.push(["s", path.slice(), encodeNode(b.kids[i])]);
      } else {
        walk(previous, b.kids[i], path, ops, arrayOps);
      }
      path.pop();
    }
    if (arrayOps) collapse(a, b, path, ops, savepoint);
    return;
  }
  //Without per-index operations any change collapses to one assignment of the
  //whole array, so probe only until something proves changed.
  if (!arrayOps) {
    let changed = a.kids.length !== b.kids.length;
    for (let i = 0; !changed && i < b.kids.length; i++) {
      walk(a.kids[i], b.kids[i], path, ops, arrayOps);
      changed = ops.length > savepoint;
    }
    if (changed) {
      ops.length = savepoint;
      ops.push(["s", path.slice(), encodeNode(b)]);
    }
    return;
  }
  //Serially compare hashes from both ends. Whatever survives the trim is the
  //window that actually moved: in-place edits keep the two middles the same
  //length and diff pairwise, while a length change becomes one splice — so an
  //insertion or deletion anywhere costs one operation instead of shifting
  //every element after it into a fresh assignment.
  let prefix = 0;
  while (prefix < a.kids.length && prefix < b.kids.length && same(a.kids[prefix], b.kids[prefix])) {
    prefix++;
  }
  let suffix = 0;
  while (
    suffix < a.kids.length - prefix && suffix < b.kids.length - prefix &&
    same(a.kids[a.kids.length - 1 - suffix], b.kids[b.kids.length - 1 - suffix])
  ) {
    suffix++;
  }
  const midA = a.kids.length - prefix - suffix;
  const midB = b.kids.length - prefix - suffix;
  //Pair the leading middles so an edited element still diffs in place, then
  //express the leftover length difference as one splice. A true cross-shift
  //pairs wrongly; collapse bounds that case at a whole-array assignment.
  const pairs = Math.min(midA, midB);
  for (let i = prefix; i < prefix + pairs; i++) {
    path.push(i);
    walk(a.kids[i], b.kids[i], path, ops, arrayOps);
    path.pop();
  }
  if (midA > midB) {
    if (suffix === 0) {
      //a pure tail truncation has a dedicated, smaller operation
      ops.push(["n", path.slice(), b.kids.length]);
    } else {
      ops.push(["x", path.slice(), prefix + pairs, midA - midB]);
    }
  } else if (midB > midA) {
    let values = "[";
    for (let i = prefix + pairs; i < prefix + midB; i++) {
      if (i > prefix + pairs) values += ",";
      values += encodeNode(b.kids[i]);
    }
    values += "]";
    ops.push(["x", path.slice(), prefix + pairs, 0, values]);
  }
  collapse(a, b, path, ops, savepoint);
}

//same is the serial-trim equality: identity means the subtree was shared when
//b was built, and hash equality covers equal bytes rebuilt.
function same(a, b) {
  return a === b || a.hash === b.hash;
}

//mergePatchFromOps projects operations back into an RFC 7386 merge patch for
//protocol v2 clients. It is only ever handed operations produced with arrayOps
//clear, so every path element is an object key.
function mergePatchFromOps(ops) {
  const root = {};
  for (const [kind, path, value] of ops) {
    let node = root;
    for (let i = 0; i < path.length - 1; i++) {
      const key = path[i];
      if (node[key] === null || typeof node[key] !== "object" || Array.isArray(node[key])) {
        node[key] = {};
      }
      node = node[key];
    }
    node[path[path.length - 1]] = kind === "d" ? null : value;
  }
  return root;
}

//diff returns wire-shaped operations (values decoded), which is what tests
//apply directly; the servers use diffOps + serializeOps to splice raw values
//without ever decoding them.
function diffOps(a, b, arrayOps) {
  if (arrayOps === undefined) arrayOps = true;
  const ops = [];
  walk(a, b, [], ops, arrayOps);
  return ops;
}

module.exports = {buildRoot, diff, diffOps, serializeOps, mergePatchFromOps, LEAF, OBJECT, ARRAY};
