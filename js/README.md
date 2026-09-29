# velox for Node.js

## Server (Express Handler)

```js
let foo = {
  a: 1,
  b: 2
};

//serve velox library
app.get("/velox.js", velox.JS);
//convert foo into a syncable object
//adds a $push function
app.get("/sync", velox.handle(foo));

//make changes
foo.a = 42;
foo.c = 7;
//push changes to clients
foo.$push();
```

## Client

```js
const local = {};
const client = velox.sse("/sync", local, {path: "settings.device"});
client.onupdate = () => console.log(local);
```

`path` selects one object or array subtree. The local value contains only that
subtree. Paths such as `settings.device`, `items[0]`, and `["quoted key"]` are
supported. The `$.` prefix is optional; `path: ""` selects the full document.
The server must support selective sync; an older server is reported through
`onerror` and the client disconnects.

To sync several branches into one sparse local document, use `paths`:

```js
const local = {};
velox.sse("/sync", local, {paths: ["settings.device", "machines.local"]});
// local has settings.device and machines.local at their original locations.
```

Use either `path` or `paths`. An empty path list selects the full document.
`paths` preserves hierarchy even with one entry; `path` returns that subtree
directly.
