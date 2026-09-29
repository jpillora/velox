// Exercises server-side v3 resume guards without opening a real SSE stream.
const assert = require("assert");
const {Writable} = require("stream");
const Connection = require("../server/connection");
const createState = require("../server/sync-state").state;
const EventSourceTransport = require("../server/transport-sse");
const selective = require("../server/selective");

async function testV3CurrentRequiresMatchingRoot() {
  let writes = [];
  let state = {
    id: "state-1",
    version: 7,
    rootHash: "a".repeat(32),
    json: '{"value":1}',
    opts: {},
    opsFor: () => null
  };
  let conn = new Connection(state);
  conn.proto = 3;
  // A stale persisted version without its corresponding root must receive a
  // snapshot; treating the version as sufficient would strand this client.
  conn.version = state.version;
  conn.transport = {write: async payload => writes.push(JSON.parse(payload))};

  await conn.push();

  assert.strictEqual(writes.length, 1, "v3 client with no root received no repair");
  assert.deepStrictEqual(writes[0].body, {value: 1});
  assert.strictEqual(writes[0].root, state.rootHash);

  // After that snapshot, version and root together are current.
  await conn.push();
  assert.strictEqual(writes.length, 1, "current v3 client received a duplicate snapshot");

  // A forged future version with a valid current root needs only metadata
  // repair. This keeps the root authoritative without resending the document.
  conn.version = 99;
  await conn.push();
  assert.strictEqual(writes.length, 2, "future version was not corrected");
  assert.strictEqual(writes[1].version, state.version);
  assert.strictEqual(writes[1].base, state.rootHash);
  assert.deepStrictEqual(writes[1].ops, []);
  assert.strictEqual(writes[1].body, undefined);
}

async function testOnlyGETCanOpenSync() {
  let status;
  let body;
  let conn = new Connection({});
  let opened = await conn.setup(
    {method: "POST", headers: {}, query: {}},
    {status: code => {
      status = code;
      return {send: value => { body = value; }};
    }}
  );
  assert.strictEqual(opened, false);
  assert.strictEqual(status, 405);
  assert.strictEqual(body, "Method Not Allowed");
}

async function testInvalidPathRejectedBeforeTransport() {
  const conn = new Connection({id: "state"});
  let status;
  const opened = await conn.setup(
    {method: "GET", headers: {accept: "text/event-stream"}, query: {p: "3", path: "$..bad"}},
    {status: code => { status = code; return {send: () => {}}; }}
  );
  assert.strictEqual(opened, false);
  assert.strictEqual(status, 400);
  assert.strictEqual(conn.transport, undefined);
}

async function testFailedWriteDoesNotWedgeConnection() {
  let writes = 0;
  let closes = 0;
  let state = {
    id: "state-2",
    version: 1,
    rootHash: "",
    json: '{"value":1}',
    opts: {},
    deltaV2: () => null
  };
  let conn = new Connection(state);
  conn.transport = {
    write: async () => {
      writes++;
      if (writes === 1) throw new Error("broken stream");
    },
    close: () => { closes++; }
  };

  await conn.push();
  assert.strictEqual(conn.pushing, false, "failed write left the connection wedged");
  assert.strictEqual(closes, 1, "failed write did not close its transport");

  await conn.push();
  assert.strictEqual(writes, 2, "connection did not attempt a later repair");
}

async function testSSEWriteErrorsPropagate() {
  const response = new Writable({write(_chunk, _encoding, callback) { callback(); }});
  const request = {
    socket: {setKeepAlive() {}, setNoDelay() {}, setTimeout() {}},
    connection: {end() {}}
  };
  const transport = new EventSourceTransport(request, response);
  transport.s = {write: (_event, callback) => callback(new Error("destroyed stream"))};
  await assert.rejects(transport.write({value: 1}), /destroyed stream/);
}

function testUnknownRootsDoNotGrowPatchCache() {
  let state = createState({value: 1});
  for (let i = 0; i < 1000; i++) {
    let token = i.toString(16).padStart(32, "0");
    assert.strictEqual(state.opsFor(token), null);
  }
  assert.strictEqual(state.patchCache.size, 0, "unknown resume tokens were cached");
}

async function testSelectiveProjection() {
  const state = {id: "selection", version: 1, json: '{"chosen":{"value":1},"ignored":"large"}', opts: {}};
  const conn = new Connection(state);
  conn.proto = 3;
  conn.path = "chosen";
  conn.pathParts = selective.parsePath(conn.path);
  const writes = [];
  conn.transport = {write: async data => writes.push(JSON.parse(data))};
  await conn.push();
  assert.deepStrictEqual(writes[0].body, {value: 1});
  assert.strictEqual(writes[0].path, "chosen");
  assert.strictEqual(JSON.stringify(writes[0]).includes("ignored"), false);
  state.version++;
  state.json = '{"chosen":{"value":1},"ignored":"changed"}';
  await conn.push();
  assert.deepStrictEqual(writes[1].ops, []);
  assert.strictEqual(writes[1].base, writes[0].root);
  state.version++;
  state.json = '{"chosen":{"value":"$& $$"},"ignored":"changed"}';
  await conn.push();
  assert.deepStrictEqual(writes[2].body, {value: "$& $$"});
}

function testOptionalRootPrefix() {
  assert.deepStrictEqual(selective.parsePath("chosen.items[0]"), ["chosen", "items", 0]);
  assert.deepStrictEqual(selective.parsePath("$.chosen.items[0]"), ["chosen", "items", 0]);
  assert.deepStrictEqual(selective.parsePath(""), []);
  assert.deepStrictEqual(selective.parsePath('["quoted key"]'), ["quoted key"]);
  assert.strictEqual(selective.project('{"items":{"0":{"x":1}}}', selective.parsePath("items[0]")), "null");
  assert.strictEqual(selective.project('{"items":[{"x":1}]}', selective.parsePath('items["0"]')), "null");
  assert.deepStrictEqual(selective.normalizePaths(['["𐀀"]', '["\uE000"]']).paths,
    ['["\uE000"]', '["𐀀"]']);
}

async function testMultiplePathsProjection() {
  const paths = selective.normalizePaths(["settings.theme", "machines.local", "items[2].id", "settings.theme"]);
  assert.deepStrictEqual(paths.paths, ["items[2].id", "machines.local", "settings.theme"]);
  const json = '{"machines":{"local":{"name":"laptop"},"remote":"skip"},"settings":{"theme":"dark","secret":"skip"},"items":[{"id":0},{"id":1},{"id":2}],"ignored":"skip"}';
  const want = {items: [null, null, {id: 2}], machines: {local: {name: "laptop"}}, settings: {theme: "dark"}};
  assert.deepStrictEqual(JSON.parse(selective.projectMany(json, paths.parts)), want);
  assert.strictEqual(selective.projectMany('{"a":1}', selective.normalizePaths(["missing", "alsoMissing"]).parts), "{}");
  assert.strictEqual(selective.projectMany("null", paths.parts), "null");
  assert.deepStrictEqual(JSON.parse(selective.projectMany(json,
    selective.normalizePaths(["machines.local.name", "machines.local"]).parts)),
    {machines: {local: {name: "laptop"}}});
  assert.throws(() => selective.normalizePaths(["items[65536]"]), /limit/);

  const state = {id: "multi", version: 1, json, opts: {}};
  const conn = new Connection(state);
  conn.proto = 3;
  conn.paths = paths.paths;
  conn.pathSets = paths.parts;
  const writes = [];
  conn.transport = {write: async data => writes.push(JSON.parse(data))};
  await conn.push();
  assert.deepStrictEqual(writes[0].body, want);
  assert.deepStrictEqual(writes[0].paths, paths.paths);
  assert.strictEqual(JSON.stringify(writes[0]).includes("ignored"), false);
  state.version++;
  state.json = json.replace('"skip"}', '"changed"}');
  await conn.push();
  assert.deepStrictEqual(writes[1].ops, []);
  assert.strictEqual(writes[1].base, writes[0].root);
}

(async () => {
  await testV3CurrentRequiresMatchingRoot();
  await testOnlyGETCanOpenSync();
  await testInvalidPathRejectedBeforeTransport();
  await testFailedWriteDoesNotWedgeConnection();
  await testSSEWriteErrorsPropagate();
  testUnknownRootsDoNotGrowPatchCache();
  await testSelectiveProjection();
  testOptionalRootPrefix();
  await testMultiplePathsProjection();
  console.log("server protocol tests passed");
})().catch(err => {
  console.error(err);
  process.exitCode = 1;
});
