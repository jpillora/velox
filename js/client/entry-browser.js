//pre-browser setup
require("event-source-polyfill");
const velox = require("./velox");
//watch online/offline events
//performs instant retries when the users
//internet connection returns
var vs = velox.connections;
function onstatus(event) {
  velox.online = navigator.onLine;
  if (velox.online) {
    for (var i = 0; i < vs.length; i++) {
      if (vs[i].retrying) vs[i].retry();
    }
  }
}
window.addEventListener("online", onstatus);
window.addEventListener("offline", onstatus);
//some platforms surface a network change (e.g. joining wifi, switching
//networks) without toggling online/offline. short-circuit the retry backoff
//for any conn that is currently disconnected. unlike onstatus we must guard on
//!connected: a conn stays retrying while healthy, so retrying alone would drop
//live connections on every network fluctuation.
function onnetworkchange() {
  if (!navigator.onLine) return;
  velox.online = true;
  for (var i = 0; i < vs.length; i++) {
    if (vs[i].retrying && !vs[i].connected) vs[i].retry();
  }
}
var conn = navigator.connection || navigator.mozConnection || navigator.webkitConnection;
if (conn && conn.addEventListener) {
  conn.addEventListener("change", onnetworkchange);
}
//expose
window.velox = velox;
module.exports = velox;
