//applyOps applies a protocol v3 patch in place.
//
//Operations are ordered and must be applied in sequence: a truncate precedes
//the assignments below the new length, and appends arrive in ascending index
//order. Unlike the v2 merge patch, operations address one child each, so
//properties the caller added for its own use — Angular's $-prefixed ones
//included — are never walked over.
//
//  ["s", path, value]                  assign; path targets the child
//  ["d", path]                         delete; path targets the child
//  ["n", path, length]                 truncate; path targets the array itself
//  ["x", path, start, delete, values?] splice; path targets the array itself
//
//Path elements are strings for object keys and numbers for array indices, so a
//numeric-looking object key never collides with an index.
const owns = (obj, key) => Object.prototype.hasOwnProperty.call(obj, key);

function setOwn(obj, key, value) {
  // __proto__ assignment has setter semantics on ordinary objects. Protocol
  // data must not be able to change the prototype of the local document.
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

function invalidPath(path, depth) {
  return new Error("velox: path escapes the document at " + path.slice(0, depth).join("."));
}

function resolve(node, path, depth) {
  for (let i = 0; i < depth; i++) {
    if (node === null || typeof node !== "object") {
      throw invalidPath(path, i);
    }
    let key = path[i];
    if (Array.isArray(node)) {
      if (!Number.isSafeInteger(key) || key < 0 || key >= node.length) {
        throw invalidPath(path, i + 1);
      }
    } else if (typeof key !== "string" || !owns(node, key)) {
      // Do not resolve inherited properties such as __proto__ or constructor.
      throw invalidPath(path, i + 1);
    }
    node = node[key];
  }
  if (node === null || typeof node !== "object") {
    throw invalidPath(path, path.length);
  }
  return node;
}

module.exports = function applyOps(root, ops) {
  if (!Array.isArray(ops)) {
    throw new Error("velox: operations must be an array");
  }
  for (let i = 0; i < ops.length; i++) {
    let op = ops[i];
    if (!Array.isArray(op)) {
      throw new Error("velox: operation " + i + " is not an array");
    }
    let kind = op[0];
    let path = op[1];
    if (!Array.isArray(path)) {
      throw new Error("velox: operation " + i + " has no path");
    }
    // $-prefixed properties belong to the embedding application (for example
    // Angular metadata) and are outside the synchronised document. Ignore an
    // operation targeting any part of that private tree rather than rejecting
    // the whole update and forcing a reconnect loop.
    if (path.some(key => typeof key === "string" && key[0] === "$")) {
      continue;
    }
    if (kind === "n") {
      if (op.length !== 3) {
        throw new Error("velox: invalid truncate operation");
      }
      let target = resolve(root, path, path.length);
      if (!Array.isArray(target)) {
        throw new Error("velox: truncate outside an array");
      }
      if (!Number.isSafeInteger(op[2]) || op[2] < 0) {
        throw new Error("velox: invalid truncate length");
      }
      target.length = op[2];
      continue;
    }
    if (kind === "x") {
      if (op.length !== 4 && op.length !== 5) {
        throw new Error("velox: invalid splice operation");
      }
      let target = resolve(root, path, path.length);
      if (!Array.isArray(target)) {
        throw new Error("velox: splice outside an array");
      }
      let start = op[2];
      let removed = op[3];
      let values = op.length > 4 ? op[4] : [];
      if (
        !Number.isInteger(start) || !Number.isInteger(removed) ||
        start < 0 || removed < 0 || start + removed > target.length ||
        !Array.isArray(values)
      ) {
        throw new Error("velox: invalid splice");
      }
      //spreading huge inserts would overflow the argument limit, so large
      //values go through in chunks after the deletion is applied once
      if (values.length <= 4096) {
        target.splice(start, removed, ...values);
      } else {
        target.splice(start, removed);
        for (let j = 0; j < values.length; j += 4096) {
          target.splice(start + j, 0, ...values.slice(j, j + 4096));
        }
      }
      continue;
    }
    if (path.length === 0) {
      throw new Error("velox: operation " + i + " has an empty path");
    }
    let parent = resolve(root, path, path.length - 1);
    let last = path[path.length - 1];
    if (kind === "s") {
      if (op.length !== 3) {
        throw new Error("velox: invalid set operation");
      }
      //assigning at the current length is how a grown array is expressed
      if (Array.isArray(parent)) {
        if (!Number.isSafeInteger(last) || last < 0 || last > parent.length) {
          throw new Error("velox: invalid array index");
        }
        parent[last] = op[2];
      } else {
        if (typeof last !== "string") {
          throw new Error("velox: object key is not a string");
        }
        setOwn(parent, last, op[2]);
      }
    } else if (kind === "d") {
      if (op.length !== 2) {
        throw new Error("velox: invalid delete operation");
      }
      //Arrays are only ever changed by assignment and truncation. A delete at
      //an index has no meaning — removing an element renumbers everything after
      //it, which the encoder expresses as assignments plus a length — and
      //JavaScript would honour it by leaving a hole rather than refusing.
      if (Array.isArray(parent)) {
        throw new Error("velox: delete against an array index");
      }
      if (typeof last !== "string") {
        throw new Error("velox: object key is not a string");
      }
      delete parent[last];
    } else {
      throw new Error("velox: unknown operation " + kind);
    }
  }
  return root;
};
