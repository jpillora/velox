// Exercises server-side v3 resume guards without opening a real SSE stream.
const assert = require("assert");
const {Writable} = require("stream");
const Connection = require("../server/connection");
const createState = require("../server/sync-state").state;
const EventSourceTransport = require("../server/transport-sse");

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

(async () => {
  await testV3CurrentRequiresMatchingRoot();
  await testOnlyGETCanOpenSync();
  await testFailedWriteDoesNotWedgeConnection();
  await testSSEWriteErrorsPropagate();
  testUnknownRootsDoNotGrowPatchCache();
  console.log("server protocol tests passed");
})().catch(err => {
  console.error(err);
  process.exitCode = 1;
});
