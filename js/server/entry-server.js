const path = require("path");
const sync = require("./sync-state");

exports.sync = sync.state;

exports.handle = function handle(obj, opts) {
  //get/create object's sync state
  let state = sync.state(obj, opts);
  return function(req, res) {
    state
      .handle(req, res)
      .then(() => {}, err => console.error("velox error", err));
  };
};

const bundle = path.join(__dirname, "..", "build", "bundle.js");

exports.js = function(req, res) {
  res.sendFile(bundle);
};
