const EventSourceTransport = require("./transport-sse");
const WebSocketTransport = require("./transport-ws");

let connectionCount = 0;

//Connection joins a given request to a sync state
module.exports = class Connection {
  constructor(state) {
    this.id = ++connectionCount;
    this.writes = 0;
    this.version = 0; //copy of client's version
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
    //optionally set specific version
    if (this.state.id === req.query.id && /^\d+$/.test(req.query.v)) {
      this.version = parseInt(req.query.v, 10);
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
    //build update for this connection
    let id = undefined;
    if (this.writes === 0) {
      id = this.state.id;
    }
    let delta = undefined;
    if (
      this.state.delta &&
      this.state.delta.length < this.state.json.length &&
      this.version === this.state.version - 1
    ) {
      delta = true;
    }

    let update = {
      id: id,
      version: this.state.version,
      delta: delta,
      body: null
    };
    //string replace to make use of cached json payload
    let body = delta ? this.state.delta : this.state.json;
    let payload = JSON.stringify(update).replace(
      /"body":null\}$/,
      `"body":${body}}`
    );
    this.debug("write msg#" + this.writes + " " + payload.length + "bytes");
    //write onto the wire!
    await this.transport.write(payload);
    this.writes++;
    //success
    this.version = this.state.version;
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
