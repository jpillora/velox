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

function check(name, before, after, leafSize) {
  const stats = {created: 0};
  const first = merkle.buildRoot(null, before, leafSize, stats);
  const second = merkle.buildRoot(first, after, leafSize, stats);
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
  const first = merkle.buildRoot(null, before, 1, stats);
  const second = merkle.buildRoot(first, after, 1, stats);
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

//Key order must not affect the hash, since it does not affect the value.
{
  const stats = {created: 0};
  const a = merkle.buildRoot(null, {outer: {x: 1, y: [1, 2]}}, 1, stats);
  const b = merkle.buildRoot(null, {outer: {y: [1, 2], x: 1}}, 1, stats);
  assert.strictEqual(a.hash, b.hash, "reordered keys produced a different hash");
  console.log("  ok   hash ignores key order");
}

//An unchanged document must reuse the whole tree, which is what makes a no-op
//push free.
{
  const doc = {a: {b: [1, 2, {c: "d"}]}, e: "f"};
  const stats = {created: 0};
  const first = merkle.buildRoot(null, doc, 1, stats);
  const before = stats.created;
  const second = merkle.buildRoot(first, JSON.parse(JSON.stringify(doc)), 1, stats);
  assert.strictEqual(first, second, "an unchanged document rebuilt its tree");
  assert.strictEqual(stats.created, before, "an unchanged document allocated nodes");
  assert.deepStrictEqual(merkle.diff(first, second), [], "an unchanged document produced operations");
  console.log("  ok   unchanged document reuses the whole tree");
}

if (failures) {
  console.error("\n" + failures + " merkle cases failed");
  process.exit(1);
}
console.log("\nall merkle cases passed (" + totalOps + " operations applied)");
