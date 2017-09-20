//TODO
module.exports = class WebsocketTransport {
  constructor(req, res) {
    this.req = req;
    this.res = res;
    this.waiter = new Promise(waited => (this.waited = waited));
    throw new Error("WebSocket not suported yet");
  }

  wait() {
    return this.waiter;
  }

  write(data) {
    //
  }
};
