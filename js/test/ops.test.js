//Replays the operation fixtures the Go encoder produces (go/testdata/ops)
//through the browser applier. The two appliers are independent implementations
//of the same spec, so this is what stops them drifting apart.
//
//Run with: node js/test/ops.test.js
const assert = require("assert");
const fs = require("fs");
const path = require("path");
const applyOps = require("../client/ops");

const fixture = path.join(__dirname, "..", "..", "go", "testdata", "ops", "cases.json");
const cases = JSON.parse(fs.readFileSync(fixture, "utf8"));

let failures = 0;
for (const c of cases) {
  const target = JSON.parse(JSON.stringify(c.before));
  try {
    applyOps(target, c.ops);
    assert.deepStrictEqual(target, c.after);
    console.log("  ok   " + c.name + "  (" + c.ops.length + " ops)");
  } catch (err) {
    failures++;
    console.error("  FAIL " + c.name);
    console.error("       ops:  " + JSON.stringify(c.ops));
    console.error("       got:  " + JSON.stringify(target));
    console.error("       want: " + JSON.stringify(c.after));
  }
}

//Guard against the fixture silently emptying out and the suite passing anyway.
assert.ok(cases.length >= 10, "expected at least 10 golden cases, got " + cases.length);

if (failures) {
  console.error("\n" + failures + "/" + cases.length + " golden op cases failed");
  process.exit(1);
}
console.log("\n" + cases.length + "/" + cases.length + " golden op cases passed");
