//A merkle tree over the live document, mirroring go/merkle.go.
//
//The two servers deliberately do NOT need to agree on a hash function: hashes
//are server-internal, and clients treat a root hash as an opaque resume token
//they echo back. That keeps cross-language JSON canonicalisation — number
//formatting, -0, 1e21, unicode escaping — out of the protocol entirely.
//
//Node granularity is controlled by a size threshold. A subtree encoding to
//fewer bytes than the threshold becomes an opaque leaf that diffs whole, which
//keeps node count proportional to the document's spine rather than to every
//scalar in it.
const crypto = require("crypto");

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

//build returns the node for value, reusing prev wherever the encoding is
//unchanged. A reused node is shared by both versions, so retaining an old root
//costs only the nodes along the paths that changed.
function build(prev, value, leafSize, stats) {
  return node(prev, value, leafSize, stats, false);
}

//buildRoot is build for the document root, which is always expanded however
//small it is. A leaf root could only ever be replaced wholesale, and no patch
//can express that: the operation would have an empty path, and an applier
//cannot reassign the caller's own object reference.
function buildRoot(prev, value, leafSize, stats) {
  return node(prev, value, leafSize, stats, true);
}

function node(prev, value, leafSize, stats, expand) {
  const encoded = JSON.stringify(value);
  //undefined and functions do not survive JSON; treat them as absent
  if (encoded === undefined) return null;

  if (prev && prev.raw === encoded) {
    return prev;
  }

  const composite = value !== null && typeof value === "object";
  if (!composite || (!expand && encoded.length < leafSize)) {
    const leaf = {kind: LEAF, raw: encoded, hash: hashOf(LEAF, [encoded])};
    stats.created++;
    return leaf;
  }

  if (Array.isArray(value)) {
    const kids = [];
    const parts = [];
    let shared = prev && prev.kind === ARRAY && prev.kids.length === value.length;
    for (let i = 0; i < value.length; i++) {
      const previous = prev && prev.kind === ARRAY ? prev.kids[i] : null;
      const kid = build(previous, value[i], leafSize, stats);
      kids.push(kid);
      parts.push(kid.hash);
      if (shared && prev.kids[i] !== kid) shared = false;
    }
    if (shared) return prev;
    stats.created++;
    return {kind: ARRAY, raw: encoded, kids, hash: hashOf(ARRAY, parts)};
  }

  //keys are sorted so that key order cannot affect the hash
  const keys = Object.keys(value).sort();
  const kids = [];
  const parts = [];
  let shared = prev && prev.kind === OBJECT && prev.keys.length === keys.length;
  for (let i = 0; i < keys.length; i++) {
    const key = keys[i];
    const previous = prev && prev.kind === OBJECT ? prev.byKey[key] : null;
    const kid = build(previous, value[key], leafSize, stats);
    kids.push(kid);
    parts.push(key, kid.hash);
    if (shared && (prev.keys[i] !== key || prev.kids[i] !== kid)) shared = false;
  }
  if (shared) return prev;
  const byKey = Object.create(null);
  for (let i = 0; i < keys.length; i++) byKey[keys[i]] = kids[i];
  stats.created++;
  return {kind: OBJECT, raw: encoded, keys, kids, byKey, hash: hashOf(OBJECT, parts)};
}

//diff emits the operations turning tree a into tree b. Cost is proportional to
//what changed: a subtree shared by both trees is dismissed on identity.
function diff(a, b) {
  const ops = [];
  walk(a, b, [], ops);
  return ops;
}

//collapse replaces the operations emitted for one subtree with a single
//assignment of it, whenever describing the change has grown more expensive than
//sending the thing itself. Inserting at the head of a long array shifts every
//element, so without this the differ would emit one assignment per element —
//more bytes than the array. Applying the rule at every node bounds the worst
//case at no worse than the whole-subtree replacement v2 would have sent.
//
//The root is exempt: an assignment there would have an empty path, and no
//applier can replace the document it was handed.
function collapse(a, b, path, ops, savepoint) {
  if (path.length === 0 || ops.length === savepoint) return;
  let emitted = 0;
  for (let i = savepoint; i < ops.length; i++) {
    emitted += JSON.stringify(ops[i]).length;
  }
  //raw is this subtree's exact encoding, so the comparison is exact
  if (emitted <= b.raw.length) return;
  ops.length = savepoint;
  ops.push(["s", path.slice(), JSON.parse(b.raw)]);
}

function walk(a, b, path, ops) {
  if (a === b || a.hash === b.hash) return;
  if (a.kind !== b.kind || a.kind === LEAF) {
    ops.push(["s", path.slice(), JSON.parse(b.raw)]);
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
        ops.push(["s", path.slice(), JSON.parse(b.kids[i].raw)]);
      } else {
        walk(previous, b.kids[i], path, ops);
      }
      path.pop();
    }
    collapse(a, b, path, ops, savepoint);
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
    walk(a.kids[i], b.kids[i], path, ops);
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
    const values = [];
    for (let i = prefix + pairs; i < prefix + midB; i++) {
      values.push(JSON.parse(b.kids[i].raw));
    }
    ops.push(["x", path.slice(), prefix + pairs, 0, values]);
  }
  collapse(a, b, path, ops, savepoint);
}

//same is the serial-trim equality: identity means the subtree was shared when
//b was built, and hash equality covers equal bytes rebuilt.
function same(a, b) {
  return a === b || a.hash === b.hash;
}

module.exports = {build, buildRoot, diff, LEAF, OBJECT, ARRAY};
