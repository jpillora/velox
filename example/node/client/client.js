//simple node client
var velox = require("../../../js");
var foo = {};
var v = velox("http://localhost:3000/sync", foo);
v.onchange = function(isConnected) {
  console.log(isConnected ? "connected" : "disconnected");
};
v.onupdate = function() {
  console.log(foo);
};
