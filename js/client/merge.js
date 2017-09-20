//recursive merge (x <- y) - ignore $properties
module.exports = function merge(x, y) {
  if (!x || typeof x !== "object" || !y || typeof y !== "object") return y;
  var k;
  if (x instanceof Array && y instanceof Array) {
    //remove extra elements
    while (x.length > y.length) x.pop();
  } else {
    //remove extra properties
    for (k in x) if (k[0] !== "$" && !(k in y)) delete x[k];
  }
  //iterate over either elements/properties
  for (k in y) x[k] = merge(x[k], y[k]);
  return x;
};
