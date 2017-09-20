//pre-node setup
global.EventSource = require("eventsource");
global.WebSocket = require("ws");
//proxy velox
const velox = require("./velox");
module.exports = velox;
