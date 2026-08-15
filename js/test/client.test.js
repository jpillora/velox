//Exercises the browser client's protocol v3 handling and its persistence,
//by standing in a fake EventSource and a fake storage backend.
//
//Run with: node js/test/client.test.js
const assert = require("assert");

//velox.js resolves its globals at require time, so these have to be in place
//first. WebSocket only needs to exist: send() does an instanceof against it.
class FakeEventSource {
  constructor(url) {
    this.url = url;
    this.readyState = 1;
    FakeEventSource.last = this;
  }
  close() {
    this.readyState = 2;
  }
}
FakeEventSource.prototype.OPEN = 1;
FakeEventSource.prototype.CLOSED = 2;

global.EventSource = FakeEventSource;
global.WebSocket = class FakeWebSocket {};

const velox = require("../client/velox");

function memoryStorage() {
  const data = new Map();
  return {
    data,
    getItem: k => (data.has(k) ? data.get(k) : null),
    setItem: (k, v) => data.set(k, v),
    removeItem: k => data.delete(k)
  };
}

function deliver(update) {
  FakeEventSource.last.onmessage({data: JSON.stringify(update)});
}

const URL = "http://example.com/velox";
const storage = memoryStorage();

// ---------------------------------------------------------------
// First session: full snapshot, then v3 operations.
// ---------------------------------------------------------------
let doc = {};
let v = velox.sse(URL, doc, {persist: true, storage, retry: false});

assert.ok(
  /[?&]p=3(&|$)/.test(FakeEventSource.last.url),
  "client did not advertise protocol 3: " + FakeEventSource.last.url
);
console.log("  ok   advertises protocol 3");

deliver({
  id: "state-1",
  version: 1,
  proto: 3,
  root: "rootA",
  body: {counter: 0, stable: {a: 1}, log: ["x"]}
});
assert.deepStrictEqual(doc, {counter: 0, stable: {a: 1}, log: ["x"]});
assert.strictEqual(v.root, "rootA");
console.log("  ok   applies a full snapshot and records the resume token");

deliver({
  version: 2,
  root: "rootB",
  base: "rootA",
  ops: [
    ["s", ["counter"], 7],
    ["s", ["log", 1], "y"],
    ["d", ["stable", "a"]]
  ]
});
assert.deepStrictEqual(doc, {counter: 7, stable: {}, log: ["x", "y"]});
assert.strictEqual(v.root, "rootB");
console.log("  ok   applies v3 operations, including append and delete");

deliver({version: 3, root: "rootC", base: "rootB", ops: [["n", ["log"], 1]]});
assert.deepStrictEqual(doc.log, ["x"]);
console.log("  ok   applies an array truncate");

// Properties the caller owns must survive: operations address one child each,
// so nothing walks over them.
doc.$local = "mine";
deliver({version: 4, root: "rootD", base: "rootC", ops: [["s", ["counter"], 8]]});
assert.strictEqual(doc.$local, "mine", "a caller-owned property was clobbered");
console.log("  ok   leaves caller-owned $ properties alone");

// ---------------------------------------------------------------
// Persistence and resume.
// ---------------------------------------------------------------
v.store.flush();
assert.ok(storage.data.size === 1, "nothing was persisted");
const saved = JSON.parse(storage.data.values().next().value);
assert.strictEqual(saved.id, "state-1");
assert.strictEqual(saved.root, "rootD");
assert.strictEqual(saved.version, 4);
assert.deepStrictEqual(saved.state.counter, 8);
console.log("  ok   persists id, version, resume token and state");

v.disconnect();

// A reload: a fresh document and client, restored from storage.
let reloaded = {};
let v2 = velox.sse(URL, reloaded, {persist: true, storage, retry: false});
assert.strictEqual(reloaded.counter, 8, "state was not restored from storage");
assert.strictEqual(v2.root, "rootD");
const resumeURL = FakeEventSource.last.url;
assert.ok(/[?&]h=rootD(&|$)/.test(resumeURL), "resume token not sent: " + resumeURL);
assert.ok(/[?&]id=state-1(&|$)/.test(resumeURL), "state id not sent: " + resumeURL);
assert.ok(/[?&]v=4(&|$)/.test(resumeURL), "version not sent: " + resumeURL);
console.log("  ok   reload restores state and resumes from the stored token");

// The server answers with operations spanning what was missed.
deliver({version: 9, root: "rootZ", base: "rootD", ops: [["s", ["counter"], 42]]});
assert.strictEqual(reloaded.counter, 42);
console.log("  ok   resumes with operations instead of a snapshot");

// ---------------------------------------------------------------
// The persisted blob must not tear: metadata and state are read together.
// ---------------------------------------------------------------
deliver({version: 10, root: "root10", base: "rootZ", ops: [["s", ["counter"], 100]]});
// More updates land before the debounce fires, which is the normal case.
deliver({version: 11, root: "root11", base: "root10", ops: [["s", ["counter"], 101]]});
v2.store.flush();
const blob = JSON.parse(storage.data.values().next().value);
assert.strictEqual(blob.state.counter, 101, "stored state was not the latest");
assert.strictEqual(blob.version, 11, "stored version did not match the stored state");
assert.strictEqual(blob.root, "root11", "stored token did not match the stored state");
console.log("  ok   persisted blob cannot tear across the debounce");

// ---------------------------------------------------------------
// Divergence recovery.
// ---------------------------------------------------------------
let errors = [];
v2.onerror = e => errors.push(e);

// Operations against a base we do not hold must never be applied: opaque
// hashes mean this is the only check available, and applying them could
// succeed against the wrong document.
deliver({version: 12, root: "rootX", base: "not-the-held-root", ops: [["s", ["counter"], 999]]});
assert.strictEqual(errors.length, 1, "a base mismatch was accepted");
assert.notStrictEqual(reloaded.counter, 999, "operations from a foreign base were applied");
assert.strictEqual(v2.root, "", "resume token survived a base mismatch");
assert.strictEqual(v2.version, 0, "version survived a base mismatch");
assert.deepStrictEqual(reloaded, {}, "diverged document was not discarded");
assert.strictEqual(storage.data.size, 0, "diverged state was left in storage");
// A resync must reconnect, and must not ask to resume.
assert.ok(!/[?&]h=/.test(FakeEventSource.last.url), "resync still sent a resume token: " + FakeEventSource.last.url);
console.log("  ok   base mismatch discards the document and reconnects for a snapshot");

// A failing operation is the other divergence route.
deliver({id: "state-2", version: 1, proto: 3, root: "fresh", body: {counter: 1}});
errors.length = 0;
deliver({version: 2, root: "next", base: "fresh", ops: [["s", ["missing", "deep"], 1]]});
assert.strictEqual(errors.length, 1, "a bad operation did not raise an error");
assert.strictEqual(v2.root, "", "resume token survived a failed operation");
assert.deepStrictEqual(reloaded, {}, "partially applied document was kept");
console.log("  ok   a failed operation discards the document and reconnects");

// Caller-owned properties survive a resync; they are not part of the document.
deliver({id: "state-2", version: 1, proto: 3, root: "r1", body: {counter: 1}});
reloaded.$mine = "kept";
deliver({version: 2, root: "r2", base: "wrong", ops: [["s", ["counter"], 2]]});
assert.strictEqual(reloaded.$mine, "kept", "resync discarded a caller-owned property");
console.log("  ok   resync leaves caller-owned $ properties alone");

v2.disconnect();

// A v2 server that predates protocol 3 must still work.
let legacyDoc = {};
let v3 = velox.sse(URL, legacyDoc, {retry: false});
deliver({id: "old", version: 1, body: {a: 1, b: 2}});
deliver({version: 2, delta: true, body: {a: 9}});
assert.deepStrictEqual(legacyDoc, {a: 9, b: 2});
console.log("  ok   still understands a v2 server");
v3.disconnect();

console.log("\nall client cases passed");
process.exit(0);
