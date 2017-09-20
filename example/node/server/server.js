const velox = require("../../../js");
const express = require("express");
const app = express();

let foo = {
  a: 42,
  b: 7
};

setInterval(() => {
  //make changes
  foo.a--;
  foo.b++;
  //push changes to clients
  foo.$push();
}, 1000);

//convert foo into a syncable object (adds a $push function)
app.get("/sync", velox.handle(foo));
//serve files
app.get("/", (req, res) => res.sendFile(__dirname + "/client.html"));
app.get("/velox.js", velox.JS);
//listen
app.listen(3000, () => console.log("Listening on 3000..."));
