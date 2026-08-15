//Store persists the synced document so that a page reload resumes from a
//patch instead of a full snapshot.
//
//Three things make this safe to leave on:
// - writes are debounced, because serialising a large document on every update
//   would cost more than the reload it saves. A stale persisted version is
//   harmless: it only widens the resume patch the server has to compute.
// - a quota failure disables persistence for the session rather than breaking
//   the connection.
// - the stored state is tagged with the server's state id, so a restarted or
//   replaced server is detected and the stored copy discarded.
const DEBOUNCE = 2000;

class Store {
  constructor(key, backend) {
    this.key = key;
    this.backend = backend;
    this.enabled = !!backend;
    this.timer = null;
    this.pending = null;
  }

  load() {
    if (!this.enabled) return null;
    let raw;
    try {
      raw = this.backend.getItem(this.key);
    } catch (err) {
      this.enabled = false;
      return null;
    }
    if (!raw) return null;
    try {
      let saved = JSON.parse(raw);
      if (!saved || typeof saved !== "object" || !saved.id || !saved.state) {
        return null;
      }
      return saved;
    } catch (err) {
      this.clear();
      return null;
    }
  }

  //save schedules a write. Repeated calls collapse into one.
  save(snapshot) {
    if (!this.enabled) return;
    this.pending = snapshot;
    if (this.timer) return;
    this.timer = setTimeout(this.flush.bind(this), DEBOUNCE);
  }

  flush() {
    this.timer = null;
    if (!this.enabled || !this.pending) return;
    let snapshot = this.pending;
    this.pending = null;
    try {
      this.backend.setItem(this.key, JSON.stringify(snapshot));
    } catch (err) {
      //most likely QuotaExceededError; give up quietly for this session
      this.enabled = false;
      this.clear();
    }
  }

  clear() {
    if (!this.backend) return;
    try {
      this.backend.removeItem(this.key);
    } catch (err) {
      /*nothing useful to do*/
    }
  }
}

//create returns a Store for the given options, or null when persistence is off.
//opts.persist may be true or an explicit key; opts.storage overrides the
//backend so Node and tests can supply their own.
module.exports = function create(opts, url, root) {
  if (!opts || !opts.persist) return null;
  let backend = opts.storage;
  if (!backend) {
    try {
      backend = root && root.localStorage;
    } catch (err) {
      backend = null;
    }
  }
  if (!backend) return null;
  let key = typeof opts.persist === "string" ? opts.persist : "velox:" + url;
  return new Store(key, backend);
};
