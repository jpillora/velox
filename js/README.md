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
//TODO
velox.sse()
```