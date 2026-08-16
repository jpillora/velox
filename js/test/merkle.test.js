//Checks the Node server's merkle tree and diff.
//
//It reuses the Go-generated fixtures, but not by comparing operations byte for
//byte: the two servers hash independently and may legitimately choose different
//granularity. What must hold is the semantic contract — whatever operations the
//Node server emits, applying them with the browser applier reproduces the new
//document exactly.
//
//Run with: node js/test/merkle.test.js
const assert = require("assert");
const fs = require("fs");
const path = require("path");
const merkle = require("../server/merkle");
const applyOps = require("../client/ops");

const fixture = path.join(__dirname, "..", "..", "go", "testdata", "ops", "cases.json");
const cases = JSON.parse(fs.readFileSync(fixture, "utf8"));

let failures = 0;
let totalOps = 0;

//buildPair builds consecutive trees the way the server does: from the
//documents' encoded forms, with the second build reusing the first.
function buildPair(before, after, leafSize, stats) {
  const beforeJson = JSON.stringify(before);
  const afterJson = JSON.stringify(after);
  const first = merkle.buildRoot(null, "", beforeJson, leafSize, stats);
  const second = merkle.buildRoot(first, beforeJson, afterJson, leafSize, stats);
  return [first, second];
}

function check(name, before, after, leafSize) {
  const stats = {created: 0};
  const [first, second] = buildPair(before, after, leafSize, stats);
  const ops = merkle.diff(first, second);
  totalOps += ops.length;

  const target = JSON.parse(JSON.stringify(before));
  try {
    applyOps(target, ops);
    assert.deepStrictEqual(target, after);
  } catch (err) {
    failures++;
    console.error("  FAIL " + name + " (leafSize " + leafSize + ")");
    console.error("       ops:  " + JSON.stringify(ops));
    console.error("       got:  " + JSON.stringify(target));
    console.error("       want: " + JSON.stringify(after));
    return;
  }
  console.log("  ok   " + name + " (leafSize " + leafSize + ", " + ops.length + " ops)");
}

for (const leafSize of [1, 512]) {
  for (const c of cases) {
    check(c.name, c.before, c.after, leafSize);
  }
}

//Structural sharing is the property that makes retaining old versions cheap, so
//assert it directly rather than trusting it.
{
  const before = {a: {x: 1, y: 2}, b: {x: 1, y: 2}, c: [1, 2, 3]};
  const after = {a: {x: 1, y: 2}, b: {x: 9, y: 2}, c: [1, 2, 3]};
  const stats = {created: 0};
  const [first, second] = buildPair(before, after, 1, stats);
  assert.notStrictEqual(first, second, "changed document reused the root");
  assert.strictEqual(first.byKey.a, second.byKey.a, "unchanged subtree a was rebuilt");
  assert.strictEqual(first.byKey.c, second.byKey.c, "unchanged subtree c was rebuilt");
  assert.notStrictEqual(first.byKey.b, second.byKey.b, "changed subtree b was shared");
  assert.strictEqual(
    first.byKey.b.byKey.y,
    second.byKey.b.byKey.y,
    "unchanged grandchild under a changed parent was rebuilt"
  );
  console.log("  ok   structural sharing");
}

//A prepend shifts every element, which used to be positional operations' worst
//case. The serial hash trim must express it as one splice carrying only the
//inserted value.
{
  const elements = [];
  for (let i = 0; i < 200; i++) elements.push(i);
  const before = {log: elements.slice()};
  const after = {log: [999].concat(elements)};
  const stats = {created: 0};
  const [first, second] = buildPair(before, after, 1, stats);
  const ops = merkle.diff(first, second);
  assert.strictEqual(ops.length, 1, "a prepend produced " + ops.length + " operations");
  assert.deepStrictEqual(ops[0], ["x", ["log"], 0, 0, [999]], "a prepend was not one head splice");
  const target = JSON.parse(JSON.stringify(before));
  applyOps(target, ops);
  assert.deepStrictEqual(target, after);
  console.log("  ok   a prepend costs one splice");
}

//A cross-shift defeats the trim and pairs every element wrongly, so a naive
//differ emits one assignment per element. The differ must send the array
//instead, bounding v3 at no worse than a whole-array replacement.
{
  const elements = [];
  for (let i = 0; i < 200; i++) elements.push(i);
  const before = {log: elements.slice()};
  const after = {log: elements.slice().reverse()};
  const stats = {created: 0};
  const [first, second] = buildPair(before, after, 1, stats);
  const ops = merkle.diff(first, second);
  assert.strictEqual(ops.length, 1, "a reversal produced " + ops.length + " operations");
  assert.strictEqual(ops[0][0], "s");
  assert.deepStrictEqual(ops[0][1], ["log"], "collapse did not target the array");
  const target = JSON.parse(JSON.stringify(before));
  applyOps(target, ops);
  assert.deepStrictEqual(target, after);
  console.log("  ok   collapses operations that outweigh their subtree");
}

//Middle deletions and insertions must splice rather than reassign the tail.
//Elements are padded so addressing one really is cheaper than resending the
//array; on tiny elements the collapse rule would (correctly) take over.
{
  const pad = m => m + "x".repeat(60);
  const before = {log: [pad("a"), pad("b"), pad("c"), pad("d")]};
  const after = {log: [pad("a"), pad("B"), pad("d")]};
  const stats = {created: 0};
  const [first, second] = buildPair(before, after, 1, stats);
  const ops = merkle.diff(first, second);
  const target = JSON.parse(JSON.stringify(before));
  applyOps(target, ops);
  assert.deepStrictEqual(target, after);
  assert.strictEqual(ops.length, 2, "edit plus deletion produced " + JSON.stringify(ops));
  assert.strictEqual(ops[0][0], "s", "the edited element was not assigned in place");
  assert.strictEqual(ops[1][0], "x", "the deletion was not a splice");
  console.log("  ok   splices inner deletions and insertions");
}

//The root must never collapse: the operation would have an empty path.
{
  const stats = {created: 0};
  const [first, second] = buildPair({a: 1, b: 2}, {a: "y".repeat(200), b: 2}, 1, stats);
  for (const op of merkle.diff(first, second)) {
    assert.notStrictEqual(op[1].length, 0, "the root was collapsed into " + JSON.stringify(op));
  }
  console.log("  ok   never collapses the root");
}

//Key order must not affect the hash, since it does not affect the value.
{
  const stats = {created: 0};
  const a = merkle.buildRoot(null, "", '{"outer":{"x":1,"y":[1,2]}}', 1, stats);
  const b = merkle.buildRoot(null, "", '{"outer":{"y":[1,2],"x":1}}', 1, stats);
  assert.strictEqual(a.hash, b.hash, "reordered keys produced a different hash");
  console.log("  ok   hash ignores key order");
}

//An unchanged document must reuse the whole tree, which is what makes a no-op
//push free.
{
  const doc = {a: {b: [1, 2, {c: "d"}]}, e: "f"};
  const docJson = JSON.stringify(doc);
  const stats = {created: 0};
  const first = merkle.buildRoot(null, "", docJson, 1, stats);
  const before = stats.created;
  const second = merkle.buildRoot(first, docJson, JSON.stringify(JSON.parse(docJson)), 1, stats);
  assert.strictEqual(first, second, "an unchanged document rebuilt its tree");
  assert.strictEqual(stats.created, before, "an unchanged document allocated nodes");
  assert.deepStrictEqual(merkle.diff(first, second), [], "an unchanged document produced operations");
  console.log("  ok   unchanged document reuses the whole tree");
}

//The windowed build derives spans arithmetically instead of scanning, so hold
//it to the from-scratch build across long random mutation chains: same hash,
//no operations between them, and operations against the previous version that
//reproduce the document exactly.
{
  let seed = 0x2f6e2b1;
  const rand = () => {
    //xorshift; deterministic so failures reproduce
    seed ^= seed << 13; seed ^= seed >>> 17; seed ^= seed << 5;
    return (seed >>> 0) / 0xffffffff;
  };
  const randValue = depth => {
    const r = rand();
    if (depth > 2 || r < 0.35) {
      const s = rand();
      if (s < 0.3) return Math.floor(rand() * 1000);
      if (s < 0.5) return rand() < 0.5;
      if (s < 0.6) return null;
      if (s < 0.8) return "s" + Math.floor(rand() * 100) + (rand() < 0.1 ? "\\\"é߿" : "");
      return rand() * 100;
    }
    if (r < 0.7) {
      const arr = [];
      const n = Math.floor(rand() * 6);
      for (let i = 0; i < n; i++) arr.push(randValue(depth + 1));
      return arr;
    }
    const obj = {};
    const n = Math.floor(rand() * 6);
    for (let i = 0; i < n; i++) obj["k" + Math.floor(rand() * 8)] = randValue(depth + 1);
    return obj;
  };
  const mutate = doc => {
    const keys = Object.keys(doc);
    const r = rand();
    if (r < 0.2 || keys.length === 0) {
      doc["k" + Math.floor(rand() * 8)] = randValue(1);
      return;
    }
    const key = keys[Math.floor(rand() * keys.length)];
    if (r < 0.3) {
      delete doc[key];
      return;
    }
    let target = doc[key];
    if (Array.isArray(target) && target.length > 0 && r < 0.75) {
      const i = Math.floor(rand() * target.length);
      const w = rand();
      if (w < 0.3) target.splice(i, 0, randValue(2));
      else if (w < 0.5) target.splice(i, 1);
      else target[i] = randValue(2);
      return;
    }
    doc[key] = randValue(1);
  };

  for (const leafSize of [1, 24, 512]) {
    const doc = {};
    for (let i = 0; i < 6; i++) doc["k" + i] = randValue(0);
    let json = JSON.stringify(doc);
    let root = merkle.buildRoot(null, "", json, leafSize, {created: 0});
    for (let step = 0; step < 300; step++) {
      const prevDoc = JSON.parse(json);
      mutate(doc);
      const next = JSON.stringify(doc);
      const prevRoot = root;
      root = merkle.buildRoot(prevRoot, json, next, leafSize, {created: 0});
      //the incremental tree must be indistinguishable from a fresh one
      const scratch = merkle.buildRoot(null, "", next, leafSize, {created: 0});
      assert.strictEqual(
        root.hash, scratch.hash,
        "step " + step + " (leafSize " + leafSize + "): incremental hash diverged from scratch\n" +
        "  prev: " + json + "\n  next: " + next
      );
      assert.deepStrictEqual(
        merkle.diff(scratch, root, true), [],
        "step " + step + ": incremental and scratch trees diff"
      );
      //and the emitted operations must carry the previous document to it
      const ops = merkle.diff(prevRoot, root, true);
      const applied = applyOps(prevDoc, ops);
      assert.deepStrictEqual(applied, JSON.parse(next),
        "step " + step + " (leafSize " + leafSize + "): ops did not reproduce the document\n" +
        "  prev: " + json + "\n  next: " + next + "\n  ops: " + JSON.stringify(ops));
      json = next;
    }
  }
  console.log("  ok   windowed build matches scratch across 900 random mutations");
}

if (failures) {
  console.error("\n" + failures + " merkle cases failed");
  process.exit(1);
}
console.log("\nall merkle cases passed (" + totalOps + " operations applied)");
