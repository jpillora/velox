const EventSourceTransport = require("./transport-sse");
const WebSocketTransport = require("./transport-ws");

let connectionCount = 0;

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
  }

  async setup(req, res) {
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
    //optionally set specific version. A mismatched id means a different state
    //object, so the client's version and resume token are meaningless.
    if (this.state.id === req.query.id) {
      if (/^\d+$/.test(req.query.v)) {
        this.version = parseInt(req.query.v, 10);
      }
      if (/^[0-9a-f]+$/.test(req.query.h || "")) {
        this.baseHash = req.query.h;
      }
    }
    //protocol negotiation: absent or older than 3 is served v2 unchanged
    if (/^\d+$/.test(req.query.p || "")) {
      this.proto = Math.min(parseInt(req.query.p, 10), 3);
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
    return this.transport.write({ping: true});
  }

  async push() {
    if (this.version === this.state.version) {
      return; //already up to date
    }
    if (this.pushing) {
      this.queued = true;
      return;
    }
    this.pushing = true;
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
      let ops = this.state.opsFor(this.baseHash);
      let useOps = ops !== null && ops.length < this.state.json.length;
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
        useOps ? `"ops":${ops}}` : `"body":${this.state.json}}`
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
        `"body":${body}}`
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
    //cleanup
    this.pushing = false;
    if (this.queued) {
      this.queued = false;
      this.push();
    }
  }

  debug() {
    if (this.state.opts.debug) {
      let args = Array.from(arguments);
      console.log.apply(console, ["connection#" + this.id + ":"].concat(args));
    }
  }
};
