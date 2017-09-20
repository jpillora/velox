const jsonpatch = require("fast-json-patch");
const merge = require("./merge");

const PROTO_VERISON = "v2";
const PING_IN_INTERVAL = 45 * 1000;
const PING_OUT_INTERVAL = 25 * 1000;
const SLEEP_CHECK = 5 * 1000;
const SLEEP_THRESHOLD = 30 * 1000;
const MAX_RETRY_DELAY = 10 * 1000;
const IS_BROWSER = typeof window === "object";
const IS_NODE = typeof global === "object";
const WS = Symbol("WS");
const SSE = Symbol("SSE");
const root = IS_BROWSER ? window : IS_NODE ? global : null;
if (!root) {
  throw "where am i...";
}

//helpers
let events = ["message", "error", "open", "close"];
let connections = []; //track open connections

//velox class - represents a single websocket (Conn on the server-side)
class Velox {
  constructor(type, url, obj) {
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
    if (!url) {
      url = "/velox";
    }
    this.url = url;
    if (!obj || typeof obj !== "object") throw "Invalid object";
    this.obj = obj;
    this.id = "";
    this.version = 0;
    this.onupdate = function() {
      /*noop*/
    };
    this.onerror = function() {
      /*noop*/
    };
    this.onconnect = function() {
      /*noop*/
    };
    this.ondisconnect = function() {
      /*noop*/
    };
    this.onchange = function() {
      /*noop*/
    };
    this.connected = false;
    this.connect();
  }
  connect() {
    if (connections.indexOf(this) === -1) {
      connections.push(this);
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
    let params = [];
    if (this.version) params.push("v=" + this.version);
    if (this.id) params.push("id=" + this.id);
    if (params.length) url += (/\?/.test(url) ? "&" : "?") + params.join("&");
    //connect!
    if (this.ws) {
      this.conn = new root.WebSocket(url);
    } else {
      this.conn = new root.EventSource(url, {withCredentials: true});
    }
    let _this = this;
    events.forEach(function(e) {
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
  }
  cleanup() {
    clearTimeout(this.pingout.t);
    if (!this.conn) {
      return;
    }
    let c = this.conn;
    this.conn = null;
    events.forEach(function(e) {
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
      } else {
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
    if (update.id) {
      this.id = update.id;
    }
    if (!update.body || !this.obj) {
      this.onerror("null objects");
      return;
    }
    //perform update
    if (update.delta) {
      jsonpatch.applyPatch(this.obj, update.body);
    } else {
      merge(this.obj, update.body);
    }
    //auto-angular
    if (typeof this.obj.$apply === "function") this.obj.$apply();
    this.onupdate(this.obj);
    this.version = update.version;
    //successful msg resets retry counter
    this.delay = 100;
  }
  connopen() {
    this.statusCheck();
    this.pingin(); //treat initial connection as incoming ping
    this.pingout(); //send initial ping
  }
  connclose() {
    this.statusCheck();
    //backoff retry connection
    this.delay = Math.min(MAX_RETRY_DELAY, this.delay * 2);
    if (this.retrying && velox.online) {
      this.retry.t = setTimeout(this.connect.bind(this), this.delay);
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
}

//public method
let velox = function(url, obj) {
  if (velox.DEFAULT === SSE || !root.WebSocket) {
    return velox.sse(url, obj);
  }
  return velox.ws(url, obj);
};
velox.WS = WS;
velox.ws = function(url, obj) {
  return new Velox(WS, url, obj);
};
velox.SSE = velox.DEFAULT = SSE;
velox.sse = function(url, obj) {
  return new Velox(SSE, url, obj);
};
velox.proto = PROTO_VERISON;
velox.connections = connections;
velox.online = true;
module.exports = velox;
