const jsonpatch = require("json-merge-patch");
const merge = require("./merge");
const applyOps = require("./ops");
const createStore = require("./storage");
const selection = require("./selective-path");
const parseUrl = require("url-parse");
const Backoff = require("backo");

const PROTO_VERISON = "v3";
//PROTO is what is advertised on the wire. A server that predates it ignores the
//parameter and replies with v2, which this client still understands, so old and
//new pairings both keep working.
const PROTO = 3;
const PING_IN_INTERVAL = 45 * 1000;
const PING_OUT_INTERVAL = 25 * 1000;
const SLEEP_CHECK = 5 * 1000;
const SLEEP_THRESHOLD = 30 * 1000;
const MAX_RETRY_DELAY = 10 * 1000;
// Resume tokens are opaque, but the built-in servers use short fixed-size
// hashes. Keep a hostile peer from turning a received token into an unbounded
// reconnect URL or persisted localStorage entry.
const MAX_RESUME_TOKEN_LENGTH = 256;
const IS_BROWSER = typeof window === "object";
const IS_NODE = typeof global === "object";
const WS = Symbol("WS");
const SSE = Symbol("SSE");
const root = IS_BROWSER ? window : IS_NODE ? global : null;
if (!root) {
  throw "where am i...";
}

function clearSyncedProperties(obj) {
  if (Array.isArray(obj)) obj.length = 0;
  // A null State is represented by an empty update on the Go wire. Keep
  // application-owned $ fields just as resync() does, but remove every field
  // that could have come from the synchronised document.
  for (const key of Object.keys(obj)) {
    if (key[0] !== "$") delete obj[key];
  }
}

//helpers
let events = ["message", "error", "open", "close"];
let connections = []; //track open connections

//velox class - represents a single websocket (Conn on the server-side)
class Velox {
  constructor(type, url, obj, opts) {
    switch (type) {
      case WS:
        if (!root.WebSocket) throw "This client does not support WebSockets";
        this.ws = true;
        break;
      case SSE:
        this.sse = true;
        break;
      default:
        throw "Type must be velox.WS or velox.SSE";
    }
    if (!obj || typeof obj !== "object") {
      throw "Invalid object";
    }
    this.obj = obj;
    this.opts = opts || {};
    this.path = this.opts.path || "";
    this.paths = selection.normalizePaths(this.opts.paths || []).paths;
    if (this.path && this.paths.length) throw new Error("velox: set path or paths, not both");
    if (this.path) selection.parsePath(this.path);
    this.backoff = new Backoff(this.opts.backoff || { min: 100, max: 20000 });
    if (this.opts.retry === undefined) {
      this.opts.retry = true;
    }
    if (!url) {
      url = "/velox";
    }
    this.url = url;
    this.id = "";
    this.version = 0;
    this.root = "";
    this.store = createStore(this.opts, url, root);
    this.restore();
    this.onpatch = function (op) {
      /*noop*/
    };
    this.onupdate = function () {
      /*noop*/
    };
    this.onerror = function () {
      /*noop*/
    };
    this.onconnect = function () {
      /*noop*/
    };
    this.ondisconnect = function () {
      /*noop*/
    };
    this.onchange = function () {
      /*noop*/
    };
    this.connected = false;
    this.connect();
  }
  connect() {
    if (connections.indexOf(this) === -1) {
      connections.push(this);
    }
    if ("Promise" in root) {
      this.waited = null;
      this.waiter = new Promise(w => {
        this.waited = w;
      });
    }
    this.retrying = true;
    this.retry();
  }
  retry() {
    clearTimeout(this.retry.t);
    if (this.conn) this.cleanup();
    if (!this.retrying) return;
    if (!this.delay) this.delay = 100;
    //set url
    let url = this.url;
    if (root.location && !/^(ws|http)s?:/.test(url)) {
      //automaticall set base url
      url = root.location.protocol + "//" + root.location.host + url;
    }
    if (this.ws) {
      url = url.replace(/^http/, "ws");
    }
    //convert to url object
    let u = parseUrl(url, true);
    //add query params
    u.query.p = PROTO;
    if (this.path) u.query.path = this.path;
    if (this.paths.length) u.query.paths = JSON.stringify(this.paths);
    if (this.version) {
      u.query.v = this.version;
    }
    if (this.id) {
      u.query.id = this.id;
    }
    //the resume token: with it the server can send a patch spanning however
    //many versions were missed, instead of the whole document
    if (this.root) {
      u.query.h = this.root;
    }
    //add auth
    if (this.opts.username) {
      u.username = this.opts.username;
    }
    if (this.opts.password) {
      u.password = this.opts.password;
    }
    //convert back to string
    url = u.toString();
    //connect!
    if (this.ws) {
      this.conn = new root.WebSocket(url);
    } else {
      this.conn = new root.EventSource(url, { withCredentials: true });
    }
    let _this = this;
    events.forEach(function (e) {
      _this.conn["on" + e] = _this["conn" + e].bind(_this);
    });
    this.sleepCheck.last = null;
    this.sleepCheck();
  }
  disconnect() {
    let i = connections.indexOf(this);
    if (i >= 0) connections.splice(i, 1);
    this.retrying = false;
    this.cleanup();
    if (this.waiter) {
      this.waited();
    }
  }
  cleanup() {
    clearTimeout(this.pingout.t);
    if (!this.conn) {
      return;
    }
    let c = this.conn;
    this.conn = null;
    events.forEach(function (e) {
      c["on" + e] = null;
    });
    if (c && c.readyState !== c.CLOSED) {
      c.close();
    }
    this.statusCheck();
  }
  send(data) {
    let c = this.conn;
    if (c && c instanceof root.WebSocket && c.readyState === c.OPEN) {
      return c.send(data);
    }
  }
  pingin() {
    //ping receievd by server, reset last timer, start death timer for 45secs
    clearTimeout(this.pingin.t);
    this.pingin.t = setTimeout(this.retry.bind(this), PING_IN_INTERVAL);
  }
  pingout() {
    this.send("ping");
    clearTimeout(this.pingout.t);
    this.pingout.t = setTimeout(this.pingout.bind(this), PING_OUT_INTERVAL);
  }
  sleepCheck() {
    let data = this.sleepCheck;
    clearInterval(data.t);
    let now = Date.now();
    //should be ~5secs, over ~30sec - assume woken from sleep
    let woken = data.last && now - data.last > SLEEP_THRESHOLD;
    data.last = now;
    data.t = setTimeout(this.sleepCheck.bind(this), SLEEP_CHECK);
    if (woken) this.retry();
  }
  statusCheck(err) {
    let curr = !!this.connected;
    let next = !!(this.conn && this.conn.readyState === this.conn.OPEN);
    if (curr !== next) {
      this.connected = next;
      this.onchange(this.connected);
      if (this.connected) {
        this.onconnect();
      } else if (this.ondisconnect.length !== 1) {
        //arity-1 ondisconnect handlers are invoked from connclose with a
        //retry trigger, so skip the legacy transition-only notification
        this.ondisconnect();
      }
    }
  }
  connmessage(event) {
    let update;
    try {
      update = JSON.parse(event.data);
    } catch (err) {
      this.onerror(err);
      return;
    }
    if (update.ping) {
      this.pingin();
      return;
    }
    if ((update.path || "") !== this.path ||
      JSON.stringify(update.paths || []) !== JSON.stringify(this.paths)) {
      this.onerror(new Error("velox: server did not acknowledge selective sync paths"));
      this.disconnect();
      return;
    }

    const owns = (key) => Object.prototype.hasOwnProperty.call(update, key);
    const hasBody = owns("body");
    const hasOps = owns("ops");
    const isV3 = owns("proto") || owns("root") || owns("base") || hasOps;
    // Go uses an omitted body to represent a null State. Accept body:null as
    // the equivalent explicit spelling used by other implementations.
    const isClear = (!hasBody && !hasOps && !update.delta) ||
      (hasBody && !hasOps && update.body === null && !update.delta);
    const fail = (reason) => {
      this.resync(reason instanceof Error ? reason : new Error(String(reason)));
    };

    // A message must have exactly one state representation. In particular,
    // [] is a meaningful v3 no-op used to advance the version when a state
    // returns to a root the client already has, so truthiness is insufficient.
    if (!this.obj || (!isClear && hasBody === hasOps)) {
      fail("velox: update must contain exactly one of body or ops");
      return;
    }

    if (!Number.isSafeInteger(update.version) || update.version <= 0) {
      fail("velox: invalid update version");
      return;
    }
    if (owns("id") && (typeof update.id !== "string" || update.id.length === 0 || update.id.length > MAX_RESUME_TOKEN_LENGTH)) {
      fail("velox: invalid state id");
      return;
    }
    if (isV3 && !isClear) {
      if (typeof update.root !== "string" || update.root.length === 0 || update.root.length > MAX_RESUME_TOKEN_LENGTH) {
        fail("velox: invalid v3 root");
        return;
      }
      if (hasOps && (typeof update.base !== "string" || update.base.length > MAX_RESUME_TOKEN_LENGTH || !Array.isArray(update.ops))) {
        fail("velox: invalid v3 operations");
        return;
      }
    }
    if ((this.path || this.paths.length) && isClear && (typeof update.root !== "string" || update.root.length === 0)) {
      fail("velox: invalid selective sync root");
      return;
    }
    if ((this.path || this.paths.length) && hasOps && (update.ops.length !== 0 || update.base !== this.root || update.root !== this.root)) {
      fail("velox: invalid selective sync operations");
      return;
    }
    if ((this.path || this.paths.length) && update.delta) {
      fail("velox: selective sync does not accept merge patches");
      return;
    }
    if (!isClear && hasBody && (update.body === null || typeof update.body !== "object" ||
      ((this.path || this.paths.length) ? Array.isArray(update.body) !== Array.isArray(this.obj) : Array.isArray(update.body)))) {
      fail("velox: full state is not an object");
      return;
    }
    if (update.delta && (!hasBody || update.body === null || typeof update.body !== "object" || Array.isArray(update.body))) {
      fail("velox: invalid delta");
      return;
    }

    const changedState = owns("id") && this.id && this.id !== update.id;
    const heldVersion = changedState ? 0 : this.version;
    // SSE is ordered, but old EventSource instances and hostile intermediaries
    // can still deliver an already-applied or stale frame. Never let either
    // roll the local state backwards; a duplicate is already represented by
    // the state we hold.
    // A server can legitimately correct a forged/future persisted version
    // without changing the document: Go sends an empty operation list when
    // the echoed root already is current. Root equality makes this safe; every
    // other lower/equal frame is a replay and is ignored.
    const versionCorrection = isV3 && hasOps && update.ops.length === 0 &&
      update.base === this.root && update.root === this.root;
    if (heldVersion > 0 && update.version <= heldVersion && !versionCorrection) {
      return;
    }
    if (!this.id && !owns("id")) {
      fail("velox: initial update has no state id");
      return;
    }
    if (changedState && hasOps) {
      // A patch against a different server's tree is never meaningful, even
      // if an opaque token happens to collide with the previous one.
      fail("velox: operations arrived with a new state id");
      return;
    }
    if (changedState) {
      // A different state id means a different server; anything persisted for
      // the old one is meaningless, as are its version and resume token.
      if (this.store) this.store.clear();
      this.version = 0;
      this.root = "";
    }
    if (owns("id")) this.id = update.id;

    //perform update
    if (isClear) {
      clearSyncedProperties(this.obj);
      this.root = (this.path || this.paths.length) ? update.root : "";
      if (this.store) this.store.clear();
    } else if (hasOps) {
      //protocol v3: an ordered operation list against the tree named by base.
      //Hashes are opaque, so the base is the only evidence that the server is
      //patching the document we actually hold. Applying operations to the wrong
      //base can succeed and leave us silently wrong, which persistence would
      //then keep across reloads, so anything unexpected resyncs instead.
      if (update.base !== this.root) {
        this.resync("velox: operations apply to " + update.base + ", not the state held");
        return;
      }
      try {
        applyOps(this.obj, update.ops);
      } catch (err) {
        this.resync(err);
        return;
      }
    } else if (update.delta) {
      // apply to doc
      try {
        jsonpatch.apply(this.obj, update.body);
      } catch (err) {
        this.onerror(err);
      }
    } else {
      merge(this.obj, update.body);
    }
    if (isV3 && !isClear) {
      this.root = update.root;
    }
    //auto-angular
    if (typeof this.obj.$apply === "function") this.obj.$apply();
    //update
    this.onupdate(this.obj);
    this.version = update.version;
    this.persist();
    //successful msg resets retry counter
    this.backoff.reset();
  }
  //restore hydrates the document from storage so a reload starts from the last
  //known state and only needs the operations published since.
  restore() {
    if (!this.store) return;
    let saved = this.store.load();
    if (!saved) return;
    if ((saved.path || "") !== this.path || JSON.stringify(saved.paths || []) !== JSON.stringify(this.paths)) {
      this.store.clear();
      return;
    }
    try {
      merge(this.obj, saved.state);
    } catch (err) {
      this.store.clear();
      return;
    }
    this.id = saved.id;
    this.version = saved.version || 0;
    this.root = saved.root || "";
  }
  persist() {
    //without a resume token the server cannot use a stored document anyway
    if (!this.store || !this.root || !this.id) return;
    //Deliberately a function, evaluated when the debounce fires. Capturing the
    //metadata now but serialising this.obj later would let updates arriving in
    //between tear the two apart: the blob would claim an old version while
    //holding newer state, and resuming from it would apply the server's
    //operations to the wrong base.
    this.store.save(() => ({
      id: this.id,
      version: this.version,
      root: this.root,
      path: this.path,
      paths: this.paths,
      state: this.obj
    }));
  }
  //resync abandons the local document and reconnects for a full snapshot.
  //A diverged document cannot be repaired from a patch stream, and keeping it
  //would mean serving wrong state indefinitely — previously until the server's
  //state id happened to rotate, and with persistence enabled, across reloads
  //too.
  resync(reason) {
    clearSyncedProperties(this.obj);
    this.version = 0;
    this.root = "";
    if (this.store) this.store.clear();
    this.onerror(reason);
    //reconnect with no resume token, which the server answers with a snapshot
    this.retry();
  }
  connopen() {
    this.statusCheck();
    this.pingin(); //treat initial connection as incoming ping
    this.pingout(); //send initial ping
  }
  connclose() {
    this.statusCheck();
    if (this.opts.retry) {
      if (this.ondisconnect.length === 1) {
        //caller opted into manual retries by declaring a retry param.
        //notify on every close (even while offline) so a countdown UI
        //stays accurate; the caller's retry() reconnects when ready.
        if (this.retrying) {
          this.ondisconnect(this.connect.bind(this));
        }
        return;
      }
      //if enabled, backoff retry connection
      let d = this.backoff.duration();
      if (this.retrying && velox.online) {
        this.retry.t = setTimeout(this.connect.bind(this), d);
      }
    } else {
      //otherwise, disconnect
      this.disconnect();
    }
  }
  connerror(err) {
    if (this.conn && this.conn instanceof root.EventSource) {
      //eventsource has no close event - instead it has its
      //own retry mechanism. lets scrap that and simulate a close,
      //to use velox backoff retries.
      this.conn.close();
      this.connclose();
    } else {
      this.statusCheck();
      this.onerror(err);
    }
  }
  wait() {
    //this requires Promise support
    return this.waiter;
  }
}

//public interface
let velox = function (url, obj, opts) {
  if (velox.DEFAULT === SSE || !root.WebSocket) {
    return velox.sse(url, obj, opts);
  }
  return velox.ws(url, obj, opts);
};
velox.WS = WS;
velox.ws = function (url, obj, opts) {
  return new Velox(WS, url, obj, opts);
};
velox.SSE = velox.DEFAULT = SSE;
velox.sse = function (url, obj, opts) {
  return new Velox(SSE, url, obj, opts);
};
velox.proto = PROTO_VERISON;
velox.protoVersion = PROTO;
velox.connections = connections;
velox.online = true;
module.exports = velox;
