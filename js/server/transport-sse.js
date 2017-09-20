const SseStream = require("ssestream");

let connectionCount = 0;

module.exports = class EventSourceTransport {
  constructor(req, res) {
    this.connectionId = ++connectionCount;
    this.eventId = 0;
    this.s = new SseStream(req);
    this.req = req;
    this.res = res;
    this.s.pipe(this.res);
    this.res.on("close", this.cleanup.bind(this));
    this.res.on("end", this.cleanup.bind(this));
    this.waiter = new Promise(waited => (this.waited = waited));
  }

  close() {
    this.req.connection.end();
  }

  wait() {
    return this.waiter;
  }

  flush() {
    if (!this.res.flush) {
      return;
    }
    //debounce
    clearTimeout(this.flushTimer);
    this.flushTimer = setTimeout(() => this.res.flush(), 50);
  }

  cleanup() {
    this.waited();
    this.s.unpipe(this.res);
  }

  writeAs(event, data) {
    return new Promise((resolve, _) => {
      this.s.write(
        {
          id: ++this.eventId,
          event: event,
          data: data
        },
        resolve
      );
    });
  }

  async write(data) {
    return this.writeAs("message", data);
  }
};
