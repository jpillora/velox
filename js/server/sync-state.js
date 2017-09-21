const Connection = require("./connection");
const throttle = require("lodash/throttle");
const compressor = require("compression")();
const jsonpatch = require("fast-json-patch");
const crypto = require("crypto");

exports.state = function(obj, opts) {
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
    if (!await conn.setup(req, res)) {
      return;
    }
    //block here and subscribe to changes
    await conn.wait();
  }

  push() {
    let currObj = jsonpatch.deepClone(this.obj);
    let json = JSON.stringify(currObj);
    if (this.json === json) {
      return;
    }
    this.version++;
    //compute diff
    if (this.prevObj) {
      this.delta = JSON.stringify(jsonpatch.compare(this.prevObj, currObj));
    }
    this.json = json;
    this.prevObj = currObj;
    //push to all subscribers
    for (let i = 0; i < this.subscribers.length; i++) {
      let conn = this.subscribers[i];
      conn.push();
    }
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
