const path = require("path");
const sync = require("./sync-state");

exports.handle = function handle(obj) {
  //get/create object's sync state
  let state = sync.state(obj);
  return function(req, res) {
    state.handle(req, res).then(() => {}, err => {});
  };
};

const bundle = path.join(__dirname, "..", "..", "assets", "bundle.js");

exports.JS = function(req, res) {
  res.sendFile(bundle);
};
