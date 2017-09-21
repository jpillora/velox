//TODO
module.exports = class WebsocketTransport {
  constructor(req, res) {
    throw new Error("WebSocket not suported yet");
    // this.req = req;
    // this.res = res;
    // this.waiter = new Promise(waited => (this.waited = waited));
  }

  wait() {
    return this.waiter;
  }

  write(data) {
    //
  }
};
