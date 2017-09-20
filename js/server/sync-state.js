const Connection = require("./connection");
const throttle = require("lodash/throttle");
const jsonpatch = require("fast-json-patch");
const crypto = require("crypto");

exports.state = function(obj) {
  if (!obj || typeof obj !== "object") {
    throw new Error("velox: can only sync objects");
  }
  //initialise object state
  let state;
  if (typeof obj.$push === "function" && obj.$push.state) {
    state = obj.$push.state;
  } else {
    state = new SyncState(obj);
    obj.$push = state.push;
  }
  return state;
};

//SyncState wraps a single object.
//A single SyncState can have many subscribers (connections).
class SyncState {
  constructor(obj) {
    this.id = crypto.randomBytes(4).toString("hex");
    this.observer = null;
    this.version = 0;
    this.obj = obj;
    this.subscribers = [];
    this.push = throttle(this.push.bind(this), 75);
    this.push(); //compute first payload
  }

  async handle(req, res) {
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
    //compute payload
    if (this.observer) {
      let patch = jsonpatch.generate(this.observer).filter(function(p) {
        return p.path !== "/$push";
      });
      if (patch.length > 0) {
        this.version++;
      }
      this.delta = JSON.stringify(patch);
    } else {
      this.version++;
    }
    this.json = JSON.stringify(this.obj);
    this.observer = jsonpatch.observe(this.obj);
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
}
