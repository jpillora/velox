const crypto = require("crypto");
const {parsePath, normalizePaths} = require("../client/selective-path");

function lookup(root, parts) {
  let value = root;
  for (const part of parts) {
    if (value === null || typeof value !== "object" ||
      Array.isArray(value) !== (typeof part === "number") ||
      !Object.prototype.hasOwnProperty.call(value, part)) {
      return {found: false};
    }
    value = value[part];
  }
  return {found: true, value};
}

function project(json, parts) {
  const result = lookup(JSON.parse(json), parts);
  return result.found ? JSON.stringify(result.value) : "null";
}

function projectMany(json, partsList) {
  const source = JSON.parse(json);
  if (source === null) return "null";
  const containers = new WeakSet();
  function container(part) {
    const node = typeof part === "number" ? [] : Object.create(null);
    containers.add(node);
    return node;
  }
  function insert(node, parts, value) {
    let target = node;
    for (let i = 0; i < parts.length - 1; i++) {
      const key = parts[i];
      if (Object.prototype.hasOwnProperty.call(target, key)) {
        if (!containers.has(target[key])) return;
      } else {
        target[key] = container(parts[i + 1]);
      }
      target = target[key];
    }
    target[parts[parts.length - 1]] = value;
  }
  const result = container(Array.isArray(source) ? 0 : "");
  for (const parts of partsList) {
    const selected = lookup(source, parts);
    if (!selected.found) continue;
    insert(result, parts, selected.value);
  }
  return JSON.stringify(result);
}

function root(path, body) {
  return crypto.createHash("sha256").update(path).update("\0").update(body).digest("hex").slice(0, 32);
}

module.exports = {parsePath, normalizePaths, project, projectMany, root};
