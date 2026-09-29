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

// Arrays are only ever changed by assignment and truncation; a delete against
// an index would leave a hole, so it must be refused rather than honoured.
{
  const applyOps = require("../client/ops");
  const doc = {log: [1, 2, 3]};
  assert.throws(() => applyOps(doc, [["d", ["log", 1]]]), /array index/);
  assert.deepStrictEqual(doc.log, [1, 2, 3], "a rejected delete still modified the array");
  console.log("  ok   rejects a delete against an array index");
}

// Snapshot merging and v3 paths must never follow JavaScript's inherited
// __proto__/constructor properties. A server is normally trusted, but an
// injected/replayed stream must not be able to pollute every object in the
// page or overwrite caller-owned $ fields.
{
  const applyOps = require("../client/ops");
  const merge = require("../client/merge");
  const doc = {$local: "mine"};
  const snapshot = JSON.parse('{"__proto__":{"polluted":true},"constructor":{"prototype":{"polluted":true}},"$local":"remote"}');
  merge(doc, snapshot);
  assert.strictEqual({}.polluted, undefined, "snapshot polluted Object.prototype");
  assert.strictEqual(doc.$local, "mine", "snapshot overwrote a caller-owned $ key");
  assert.ok(Object.prototype.hasOwnProperty.call(doc, "__proto__"), "__proto__ was not kept as data");
  assert.throws(
    () => applyOps({}, [["s", ["constructor", "prototype", "polluted"], true]]),
    /path escapes/,
    "operation traversed inherited constructor"
  );
  applyOps(doc, [
    ["s", ["$local"], "remote"],
    ["d", ["$local"]],
    ["s", ["$nested", "value"], "remote"]
  ]);
  assert.strictEqual(doc.$local, "mine", "operation overwrote a caller-owned $ key");
  assert.strictEqual(doc.$nested, undefined, "operation created a caller-owned $ key");
  applyOps(doc, [["s", ["__proto__", "safe"], 1]]);
  assert.strictEqual(doc.__proto__.safe, 1, "own __proto__ data could not be updated safely");
  assert.strictEqual({}.safe, undefined, "operation polluted Object.prototype");
  assert.throws(
    () => applyOps(doc, [["s", ["missing"]]]),
    /invalid set/,
    "truncated set operation was accepted"
  );
  console.log("  ok   confines hostile keys and ignores caller-owned $ properties");
}

// Version numbers are a monotonic sequence for one state ID. Replayed SSE
// frames must not roll a client back, and a new ID must reset that sequence so
// its initial snapshot is accepted even at a lower version.
{
  const orderedDoc = {};
  const ordered = velox.sse(URL, orderedDoc, {retry: false});
  deliver({id: "ordered-a", version: 1, proto: 3, root: "ordered-root-a", body: {value: 1}});
  deliver({version: 2, root: "ordered-root-b", base: "ordered-root-a", ops: [["s", ["value"], 2]]});
  assert.strictEqual(orderedDoc.value, 2);
  // Both an old snapshot and a duplicate patch are harmlessly ignored.
  deliver({version: 1, proto: 3, root: "ordered-root-a", body: {value: 999}});
  deliver({version: 2, root: "ordered-root-b", base: "ordered-root-a", ops: [["s", ["value"], 999]]});
  assert.strictEqual(orderedDoc.value, 2, "replayed update rolled state back");
  assert.strictEqual(ordered.version, 2, "replayed update rolled version back");
  // A current root plus no operations is the one valid backwards version
  // transition: it repairs a forged/future persisted version without changing
  // the document.
  deliver({version: 1, root: "ordered-root-b", base: "ordered-root-b", ops: []});
  assert.strictEqual(orderedDoc.value, 2, "version correction changed the document");
  assert.strictEqual(ordered.version, 1, "same-root version correction was ignored");
  // New identities legitimately start their version sequence again.
  deliver({id: "ordered-b", version: 1, proto: 3, root: "ordered-root-c", body: {fresh: true}});
  assert.deepStrictEqual(orderedDoc, {fresh: true});
  assert.strictEqual(ordered.version, 1);
  ordered.disconnect();
  console.log("  ok   rejects stale/replayed frames while accepting a new state identity");
}

// A Go State may become null. Its SSE representation omits body entirely, so
// make that clear the client document rather than treating it as malformed or
// leaving stale fields visible. body:null is the equivalent explicit form.
{
  const clearDoc = {$local: "keep"};
  const clearing = velox.sse(URL, clearDoc, {retry: false});
  deliver({id: "clear-a", version: 1, proto: 3, root: "clear-root", body: {value: 1}});
  deliver({version: 2});
  assert.deepStrictEqual(clearDoc, {$local: "keep"}, "omitted-body clear left synced fields behind");
  assert.strictEqual(clearing.root, "", "clear retained a resume token");
  assert.strictEqual(clearing.version, 2, "clear did not advance the version");
  deliver({id: "clear-b", version: 1, body: {value: 2}});
  deliver({version: 2, body: null});
  assert.deepStrictEqual(clearDoc, {$local: "keep"}, "body:null clear left synced fields behind");
  clearing.disconnect();
  console.log("  ok   applies explicit and omitted-body state clears");
}

// A v2 server that predates protocol 3 must still work.
let legacyDoc = {};
let v3 = velox.sse(URL, legacyDoc, {retry: false});
deliver({id: "old", version: 1, body: {a: 1, b: 2}});
deliver({version: 2, delta: true, body: {a: 9}});
assert.deepStrictEqual(legacyDoc, {a: 9, b: 2});
console.log("  ok   still understands a v2 server");
v3.disconnect();

// A selective client receives only the chosen array and rejects a server that
// ignores the path request. The root remains scoped to that selection.
{
  const selected = [];
  const client = velox.sse(URL, selected, {path: "users[0].items", retry: false});
  assert.ok(FakeEventSource.last.url.includes("path="));
  deliver({id: "selection", version: 1, proto: 3, path: "users[0].items",
    root: "selected-a", body: [1, 2]});
  assert.deepStrictEqual(selected, [1, 2]);
  deliver({version: 2, path: "users[0].items", root: "selected-a",
    base: "selected-a", ops: []});
  assert.deepStrictEqual(selected, [1, 2]);
  assert.strictEqual(client.version, 2);
  deliver({version: 3, path: "users[0].items", root: "selected-b", body: [3]});
  assert.deepStrictEqual(selected, [3]);
  deliver({version: 4, path: "users[0].items", root: "selected-null", body: null});
  assert.deepStrictEqual(selected, []);
  assert.strictEqual(client.root, "selected-null");
  deliver({version: 5, root: "wrong", body: [9]});
  assert.deepStrictEqual(selected, []);
  assert.strictEqual(client.version, 4);
  assert.strictEqual(client.retrying, false);
  client.disconnect();
  console.log("  ok   selective sync keeps only the selected array and checks path acknowledgement");
}

// Multiple paths preserve their positions in a sparse local document.
{
  const local = {};
  const client = velox.sse(URL, local, {paths: ["settings.theme", "machines.local"], retry: false});
  assert.ok(FakeEventSource.last.url.includes("paths="));
  deliver({id: "multi", version: 1, proto: 3, paths: ["machines.local", "settings.theme"],
    root: "multi-a", body: {machines: {local: {name: "laptop"}}, settings: {theme: "dark"}}});
  assert.deepStrictEqual(local, {machines: {local: {name: "laptop"}}, settings: {theme: "dark"}});
  deliver({version: 2, paths: ["machines.local", "settings.theme"],
    root: "multi-a", base: "multi-a", ops: []});
  assert.strictEqual(client.version, 2);
  deliver({version: 3, paths: ["machines.local", "settings.theme"],
    root: "multi-b", body: {settings: {theme: "light"}}});
  assert.deepStrictEqual(local, {settings: {theme: "light"}});
  deliver({version: 4, path: "machines.local", root: "wrong", body: {name: "wrong"}});
  assert.deepStrictEqual(local, {settings: {theme: "light"}});
  assert.strictEqual(client.retrying, false);
  client.disconnect();
  console.log("  ok   syncs multiple paths as a sparse document");
}

console.log("\nall client cases passed");
process.exit(0);
