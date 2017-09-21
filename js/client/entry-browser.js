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
//expose
window.velox = velox;
module.exports = velox;
