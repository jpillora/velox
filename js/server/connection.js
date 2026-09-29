const EventSourceTransport = require("./transport-sse");
const WebSocketTransport = require("./transport-ws");
const selective = require("./selective");

let connectionCount = 0;
const ROOT_HASH_RE = /^[0-9a-f]{32}$/;

function isSafeNonNegativeInteger(value) {
  return typeof value === "string" &&
    /^(?:0|[1-9]\d*)$/.test(value) &&
    Number.isSafeInteger(Number(value));
}

//Connection joins a given request to a sync state
module.exports = class Connection {
  constructor(state) {
    this.id = ++connectionCount;
    this.writes = 0;
    this.version = 0; //copy of client's version
    this.proto = 0; //protocol the client asked for; below 3 is served v2
    this.baseHash = ""; //opaque root the client currently holds
    this.connected = false;
    this.state = state;
    this.pushing = false;
    this.queued = false;
    this.path = "";
    this.paths = [];
  }

  async setup(req, res) {
    // SSE and WebSocket upgrades are GET-only. Reject other methods before a
    // transport is allocated so an endpoint mounted without Express's app.get
    // does not accidentally accept stateful-looking writes.
    if (req.method !== "GET") {
      res.status(405).send("Method Not Allowed");
      return false;
    }
    //optionally set specific version. A mismatched id means a different state
    //object, so the client's version and resume token are meaningless.
    if (this.state.id === req.query.id) {
      if (isSafeNonNegativeInteger(req.query.v) && Number(req.query.v) > 0) {
        this.version = Number(req.query.v);
      }
      if (typeof req.query.h === "string" && ROOT_HASH_RE.test(req.query.h)) {
        this.baseHash = req.query.h;
      }
    }
    //protocol negotiation: absent or older than 3 is served v2 unchanged
    if (isSafeNonNegativeInteger(req.query.p) && Number(req.query.p) > 0) {
      this.proto = Math.min(Number(req.query.p), 3);
    }
    if (req.query.path !== undefined && req.query.path !== "") {
      try {
        if (this.proto < 3) throw new Error("selective sync requires protocol v3");
        this.pathParts = selective.parsePath(req.query.path);
        this.path = req.query.path;
      } catch (err) {
        res.status(400).send(err.message);
        return false;
      }
    }
    if (req.query.paths !== undefined) {
      try {
        if (this.proto < 3 || this.path) throw new Error("multiple paths require protocol v3 and no single path");
        const selection = selective.normalizePaths(JSON.parse(req.query.paths));
        this.paths = selection.paths;
        this.pathSets = selection.parts;
      } catch (err) {
        res.status(400).send(err.message);
        return false;
      }
    }
    if (req.headers["accept"] === "text/event-stream") {
      this.transport = new EventSourceTransport(req, res);
    } else if (req.headers["upgrade"] === "websocket") {
      // TODO WEBSOCKETS
      // this.transport = new WebSocketTransport(req, res);
      res.status(501).send("WebSockets not implemented yet");
      return false;
    } else {
      res.status(400).send("Invalid sync request");
      return false;
    }
    this.debug("setup", req.query);
    this.connected = true;
    return true;
  }

  async wait() {
    this.debug("open");
    //subscribe while the transport connection is active
    this.state.subscribe(this);
    //start ping interval
    let keepAliveTimer = setInterval(this.keepAlive.bind(this), 25 * 1000);
    this.keepAlive();
    //block
    await this.transport.wait();
    //stop ping
    clearInterval(keepAliveTimer);
    //not connected
    this.connected = false;
    //unsubscribe
    this.state.unsubscribe(this);
    this.debug("close");
  }

  async keepAlive() {
    try {
      await this.transport.write({ping: true});
    } catch (err) {
      // Timers do not observe rejected promises. Close a failed stream here
      // rather than leaving an unhandled rejection and a dead subscriber.
      this.debug("keepalive failed", err);
      if (this.transport && typeof this.transport.close === "function") {
        this.transport.close();
      }
    }
  }

  async push() {
    if (this.path || this.paths.length) return this.pushSelected();
    //Version alone is not evidence a v3 client holds this document: an old
    //client, corrupt persistence, or a hostile reconnect can pair a current
    //version with no (or another) root. Send a snapshot in that case.
    if (this.version === this.state.version &&
      (this.proto < 3 || this.baseHash === this.state.rootHash)) {
      return; //already up to date
    }
    if (this.pushing) {
      this.queued = true;
      return;
    }
    this.pushing = true;
    let sent = false;
    try {
      //build update for this connection. The state can move on while the write
      //below is awaited, so record what was actually sent, not whatever the
      //state holds once the write returns.
      let sentVersion = this.state.version;
      let sentRoot = this.state.rootHash;
      let id = undefined;
      if (this.writes === 0) {
        id = this.state.id;
      }
      let payload;
      if (this.proto >= 3) {
        //a client whose base is still retained gets operations against it, however
        //many versions behind it has fallen; anything else takes the document
        let sameRoot = this.baseHash !== "" && this.baseHash === sentRoot;
        //The root is authoritative. If content is already identical but the
        //untrusted version is not, send an explicit no-op to repair only that
        //metadata; a snapshot is needlessly large and makes replay handling
        //less precise for clients.
        let ops = sameRoot ? "[]" : this.state.opsFor(this.baseHash);
        let useOps = sameRoot || (ops !== null && ops.length < this.state.json.length);
        let update = {
          id: id,
          version: sentVersion,
          proto: this.writes === 0 ? 3 : undefined,
          root: sentRoot,
          base: useOps ? this.baseHash : undefined,
          ops: null
        };
        payload = JSON.stringify(update).replace(
          /"ops":null\}$/,
          () => useOps ? `"ops":${ops}}` : `"body":${this.state.json}}`
        );
      } else {
        let delta = undefined;
        let deltaJson = null;
        //the v2 projection is computed on first use and memoised in the state
        if (this.version === this.state.version - 1) {
          deltaJson = this.state.deltaV2();
        }
        if (deltaJson && deltaJson.length < this.state.json.length) {
          delta = true;
        }

        let update = {
          id: id,
          version: sentVersion,
          delta: delta,
          body: null
        };
        //string replace to make use of cached json payload
        let body = delta ? deltaJson : this.state.json;
        payload = JSON.stringify(update).replace(
          /"body":null\}$/,
          () => `"body":${body}}`
        );
      }
      this.debug("write msg#" + this.writes + " " + payload.length + "bytes");
      //write onto the wire!
      await this.transport.write(payload);
      this.writes++;
      //success — recorded only after the write, so a failed send does not leave
      //the connection claiming the client holds a root it never received
      this.version = sentVersion;
      if (this.proto >= 3) {
        this.baseHash = sentRoot;
      }
      sent = true;
    } catch (err) {
      //State calls push without awaiting it. Never leave the connection marked
      //as pushing after a failed write (which would suppress every later
      //update), and close the transport that cannot carry a repair.
      this.debug("write failed", err);
      if (this.transport && typeof this.transport.close === "function") {
        this.transport.close();
      }
    } finally {
      this.pushing = false;
      if (sent && this.queued) {
        this.queued = false;
        void this.push();
      } else {
        this.queued = false;
      }
    }
  }

  async pushSelected() {
    if (this.pushing) { this.queued = true; return; }
    this.pushing = true;
    let sent = false;
    try {
      const version = this.state.version;
      const many = this.paths.length > 0;
      const body = many ? selective.projectMany(this.state.json, this.pathSets) :
        selective.project(this.state.json, this.pathParts);
      const root = selective.root(many ? "paths:" + JSON.stringify(this.paths) : this.path, body);
      if (this.version === version && this.baseHash === root) return;
      const same = this.baseHash === root && body !== "null";
      const head = {id: this.writes === 0 ? this.state.id : undefined,
        version, proto: this.writes === 0 ? 3 : undefined,
        path: this.path || undefined, paths: many ? this.paths : undefined,
        root, base: same ? this.baseHash : undefined};
      const payload = same ? JSON.stringify({...head, ops: []}) :
        JSON.stringify({...head, body: null}).replace(/"body":null\}$/, () => `"body":${body}}`);
      await this.transport.write(payload);
      this.writes++;
      this.version = version;
      this.baseHash = root;
      sent = true;
    } catch (err) {
      this.debug("selective write failed", err);
      if (this.transport && typeof this.transport.close === "function") this.transport.close();
    } finally {
      this.pushing = false;
      if (sent && this.queued) { this.queued = false; void this.pushSelected(); }
    }
  }

  debug() {
    if (this.state.opts.debug) {
      let args = Array.from(arguments);
      console.log.apply(console, ["connection#" + this.id + ":"].concat(args));
    }
  }
};
