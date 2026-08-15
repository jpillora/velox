//applyOps applies a protocol v3 patch in place.
//
//Operations are ordered and must be applied in sequence: a truncate precedes
//the assignments below the new length, and appends arrive in ascending index
//order. Unlike the v2 merge patch, operations address one child each, so
//properties the caller added for its own use — Angular's $-prefixed ones
//included — are never walked over.
//
//  ["s", path, value]  assign; path targets the child
//  ["d", path]         delete; path targets the child
//  ["n", path, length] truncate; path targets the array itself
//
//Path elements are strings for object keys and numbers for array indices, so a
//numeric-looking object key never collides with an index.
function resolve(node, path, depth) {
  for (let i = 0; i < depth; i++) {
    if (node === null || typeof node !== "object") {
      throw new Error("velox: path escapes the document at " + path.slice(0, i).join("."));
    }
    node = node[path[i]];
  }
  if (node === null || typeof node !== "object") {
    throw new Error("velox: path escapes the document at " + path.join("."));
  }
  return node;
}

module.exports = function applyOps(root, ops) {
  if (!Array.isArray(ops)) {
    throw new Error("velox: operations must be an array");
  }
  for (let i = 0; i < ops.length; i++) {
    let op = ops[i];
    let kind = op[0];
    let path = op[1];
    if (!Array.isArray(path)) {
      throw new Error("velox: operation " + i + " has no path");
    }
    if (kind === "n") {
      let target = resolve(root, path, path.length);
      if (!Array.isArray(target)) {
        throw new Error("velox: truncate outside an array");
      }
      target.length = op[2];
      continue;
    }
    if (path.length === 0) {
      throw new Error("velox: operation " + i + " has an empty path");
    }
    let parent = resolve(root, path, path.length - 1);
    let last = path[path.length - 1];
    if (kind === "s") {
      //assigning at the current length is how a grown array is expressed
      parent[last] = op[2];
    } else if (kind === "d") {
      delete parent[last];
    } else {
      throw new Error("velox: unknown operation " + kind);
    }
  }
  return root;
};
