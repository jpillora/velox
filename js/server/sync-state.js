const Connection = require("./connection");
const throttle = require("lodash/throttle");
const compressor = require("compression")();
const jsonmergepatch = require("json-merge-patch");
const merkle = require("./merkle");
const crypto = require("crypto");

//Node granularity for the merkle tree. Mirrors DefaultMerkleLeafSize in Go.
const DEFAULT_LEAF_SIZE = 512;
//How far back a v3 client may resume from, and the node budget that bounds it.
const DEFAULT_HISTORY_WINDOW = 5 * 60 * 1000;
const DEFAULT_HISTORY_MAX_NODES = 1 << 20;

exports.state = function (obj, opts) {
  if (!obj || typeof obj !== "object") {
    throw new Error("velox: can only sync objects");
  }
  //initialise object state
  let state;
  if (typeof obj.$push === "function" && obj.$push.state) {
    state = obj.$push.state;
  } else {
    state = new SyncState(obj, opts);
    state.debug("new");
    obj.$push = state.push;
  }
  return state;
};

//SyncState wraps a single object.
//A single SyncState can have many subscribers (connections).
class SyncState {
  constructor(obj, opts) {
    this.id = crypto.randomBytes(4).toString("hex");
    this.observer = null;
    this.version = 0;
    this.obj = obj;
    this.opts = opts || {};
    if (this.opts.gzip === undefined) {
      this.opts.gzip = true;
    }
    this.subscribers = [];
    this.leafSize = this.opts.merkleLeafSize || DEFAULT_LEAF_SIZE;
    this.historyWindow = this.opts.historyWindow || DEFAULT_HISTORY_WINDOW;
    this.historyMaxNodes = this.opts.historyMaxNodes || DEFAULT_HISTORY_MAX_NODES;
    //recent roots, so a client that lagged, reconnected or reloaded gets a
    //patch instead of the whole document
    this.history = [];
    this.historyNodes = 0;
    this.root = null;
    this.rootHash = "";
    //v3 operation payloads memoised by the base tree they apply to, so N
    //connections sharing a base cost one diff rather than N
    this.patchCache = new Map();
    this.push = throttle(this.push.bind(this), 75);
    this.push.state = this;
    this.push(); //compute first payload
  }

  async handle(req, res) {
    //manually execute compression middleware
    if (this.opts.gzip) {
      await new Promise(resolve => {
        compressor(req, res, resolve);
      });
    }
    //handle for realz
    await this._handle(req, res);
  }

  async _handle(req, res) {
    //connect this request to the sync state
    let conn = new Connection(this);
    //perform sse/websocket handshake
    if (!(await conn.setup(req, res))) {
      return;
    }
    //block here and subscribe to changes
    await conn.wait();
  }

  push() {
    let json = JSON.stringify(this.obj);
    if (this.json === json) {
      return;
    }
    this.version++;
    //compute diff (from 2nd push onwards)
    if (this.prevObj) {
      this.delta = JSON.stringify(
        jsonmergepatch.generate(this.prevObj, this.obj)
      );
    }
    //rebuild the merkle tree; unchanged subtrees are reused by identity
    let stats = {created: 0};
    this.root = merkle.buildRoot(this.root, this.obj, this.leafSize, stats);
    this.rootHash = this.root ? this.root.hash : "";
    this.recordVersion(stats.created);
    this.patchCache.clear();
    this.prevObj = JSON.parse(json); // save previous state
    this.json = json;
    //push to all subscribers
    for (let i = 0; i < this.subscribers.length; i++) {
      let conn = this.subscribers[i];
      conn.push();
    }
  }

  recordVersion(created) {
    if (!this.rootHash) return;
    this.history.push({
      version: this.version,
      root: this.root,
      hash: this.rootHash,
      created: created,
      at: Date.now()
    });
    this.historyNodes += created;
    //drop the oldest once it ages out or the node budget is exceeded; the
    //newest is always kept, since it is the state itself
    let now = Date.now();
    while (this.history.length > 1) {
      let oldest = this.history[0];
      if (now - oldest.at <= this.historyWindow && this.historyNodes <= this.historyMaxNodes) {
        break;
      }
      this.historyNodes -= oldest.created;
      this.history.shift();
    }
  }

  //opsFor returns the serialised v3 operations carrying baseHash's tree to the
  //current one, or null when the base is unknown and the caller must send a
  //full snapshot.
  opsFor(baseHash) {
    if (!baseHash || !this.root || baseHash === this.rootHash) return null;
    if (this.patchCache.has(baseHash)) {
      return this.patchCache.get(baseHash);
    }
    let base = null;
    for (let i = 0; i < this.history.length; i++) {
      if (this.history[i].hash === baseHash) {
        base = this.history[i].root;
        break;
      }
    }
    //a miss is memoised as null so a client stuck on an evicted base is not
    //re-diffed on every push
    let payload = base ? JSON.stringify(merkle.diff(base, this.root)) : null;
    this.patchCache.set(baseHash, payload);
    return payload;
  }

  subscribe(conn) {
    let i = this.subscribers.indexOf(conn);
    if (i >= 0) {
      return;
    }
    this.subscribers.push(conn);
    //push curr state to just this connection
    conn.push();
  }

  unsubscribe(conn) {
    let i = this.subscribers.indexOf(conn);
    if (i >= 0) {
      this.subscribers.splice(i, 1);
    }
  }

  debug() {
    if (this.opts.debug) {
      let args = Array.from(arguments);
      console.log.apply(console, ["sync-state#" + this.id + ":"].concat(args));
    }
  }
}
