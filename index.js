const client = require("./js/client/entry-node");
const server = require("./js/server/entry-server");
//place server functions onto the exported function
client.handle = server.handle;
client.JS = server.JS;
//expose both client and server
module.exports = client;
