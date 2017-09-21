const SseStream = require("ssestream");
const throttle = require("lodash/throttle");

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
    this.flush = throttle(this.flush.bind(this), 50);
  }

  close() {
    this.req.connection.end();
  }

  wait() {
    return this.waiter;
  }

  cleanup() {
    this.waited();
    this.s.unpipe(this.res);
  }

  writeAs(event, data) {
    return new Promise((resolve, _) => {
      //attempt write
      this.s.write(
        {
          id: ++this.eventId,
          event: event,
          data: data
        },
        () => {
          //written!
          if (this.res.get("Content-Encoding") === "gzip") {
            this.flush();
          }
          resolve();
        }
      );
    });
  }

  async write(data) {
    return this.writeAs("message", data);
  }

  //flush is required for middleware which
  //response buffer data, like gzip. throttled to 50ms.
  flush() {
    if (this.res.flush) {
      this.res.flush();
    }
  }
};
