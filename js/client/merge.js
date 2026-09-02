//recursive merge (x <- y) - ignore $properties
//
//Only own properties participate. Besides matching JSON's object model, this
//keeps a server-supplied "__proto__" or "constructor" key from walking into
//Object.prototype while a snapshot is merged.
const owns = (obj, key) => Object.prototype.hasOwnProperty.call(obj, key);

function setOwn(obj, key, value) {
  // Assignment to __proto__ invokes a legacy setter on ordinary objects. Define
  // it as data instead, so it remains a normal synchronised JSON key.
  if (key === "__proto__") {
    Object.defineProperty(obj, key, {
      value: value,
      writable: true,
      enumerable: true,
      configurable: true
    });
  } else {
    obj[key] = value;
  }
}

module.exports = function merge(x, y) {
  if (!x || typeof x !== "object" || !y || typeof y !== "object") return y;
  var k;
  if (x instanceof Array && y instanceof Array) {
    //remove extra elements
    while (x.length > y.length) x.pop();
  } else {
    //remove extra properties
    for (k of Object.keys(x)) if (k[0] !== "$" && !owns(y, k)) delete x[k];
  }
  //iterate over either elements/properties
  for (k of Object.keys(y)) {
    // $-prefixed properties are reserved for the embedding application. They
    // must not be replaced by a snapshot after a reconnect.
    if (k[0] === "$") continue;
    let previous = owns(x, k) ? x[k] : undefined;
    setOwn(x, k, merge(previous, y[k]));
  }
  return x;
};
