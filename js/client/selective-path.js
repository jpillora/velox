function parsePath(path) {
  if (typeof path !== "string" || path.length > 1024) throw new Error("velox: invalid selective sync path");
  if (path === "") return [];
  if (path[0] !== "$") path = (path[0] === "." || path[0] === "[" ? "$" : "$.") + path;
  if (path.length < 3) throw new Error("velox: invalid selective sync path");
  const parts = [];
  let i = 1;
  while (i < path.length) {
    if (path[i] === ".") {
      const match = /^[A-Za-z_][A-Za-z_0-9]*/.exec(path.slice(++i));
      if (!match) throw new Error("velox: invalid selective sync path");
      parts.push(match[0]);
      i += match[0].length;
    } else if (path[i] === "[") {
      i++;
      if (path[i] === '"') {
        const start = i++;
        while (i < path.length) {
          if (path[i] === "\\") { i += 2; continue; }
          if (path[i] === '"') break;
          i++;
        }
        if (path[i + 1] !== "]") throw new Error("velox: invalid selective sync path");
        parts.push(JSON.parse(path.slice(start, i + 1)));
        i += 2;
      } else {
        const match = /^(0|[1-9][0-9]*)\]/.exec(path.slice(i));
        if (!match || !Number.isSafeInteger(Number(match[1]))) throw new Error("velox: invalid selective sync path");
        parts.push(Number(match[1]));
        i += match[0].length;
      }
    } else {
      throw new Error("velox: invalid selective sync path");
    }
  }
  if (!parts.length) throw new Error("velox: invalid selective sync path");
  return parts;
}

function normalizePaths(paths) {
  if (!Array.isArray(paths) || paths.length > 32) throw new Error("velox: invalid selective sync paths");
  // Go sorts UTF-8 strings by bytes, which is Unicode code point order.
  // JavaScript's default UTF-16 sort disagrees for supplementary characters.
  const ordered = paths.slice().sort((a, b) => {
    if (typeof a !== "string" || typeof b !== "string") throw new Error("velox: invalid selective sync path");
    let i = 0, j = 0;
    while (i < a.length && j < b.length) {
      const x = a.codePointAt(i), y = b.codePointAt(j);
      if (x !== y) return x - y;
      i += x > 0xffff ? 2 : 1;
      j += y > 0xffff ? 2 : 1;
    }
    return i < a.length ? 1 : j < b.length ? -1 : 0;
  });
  const unique = [];
  const parts = [];
  for (const path of ordered) {
    if (path === "") throw new Error("velox: an empty path cannot be combined with other paths");
    if (unique.length && path === unique[unique.length - 1]) continue;
    const parsed = parsePath(path);
    if (parsed.some(part => typeof part === "number" && part > 65535)) {
      throw new Error("velox: array index exceeds multi-path limit 65535");
    }
    unique.push(path);
    parts.push(parsed);
  }
  return {paths: unique, parts};
}

module.exports = {parsePath, normalizePaths};
