const velox = require("../../..");
const express = require("express");
const app = express();
//convert foo into a syncable object (adds a $push function)
let foo = {
  a: 42,
  b: 7
};
app.get("/sync", velox.handle(foo));
//serve files
app.get("/velox.js", velox.js);
app.get("/", (req, res) => res.sendFile(__dirname + "/client.html"));
//listen
app.listen(4000, () => console.log("Listening on 4000..."));

//things happen...
setInterval(() => {
  //make changes
  foo.a--;
  foo.b++;
  //push changes to clients
  foo.$push();
}, 1000);
