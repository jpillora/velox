//simple node client
var velox = require("../../..");
var foo = {};
var v = velox("http://localhost:4000/sync", foo);
v.onchange = function(connected) {
  console.log(connected ? "connected" : "disconnected");
};
v.onupdate = function() {
  console.log(foo);
};
v.onerror = function(e) {
  console.log(e);
};
