/* global window,WebSocket */
// velox - v0.1.0 - https://github.com/jpillora/velox
// Jaime Pillora <dev@jpillora.com> - MIT Copyright 2015
(function(window, document) {
  if(!window.WebSocket)
    return alert("This browser does not support WebSockets");
  //velox protocol version
  var proto = "v2";
  var vs = [];
  //public method
  var velox = function(type, url, obj) {
    var v = new Velox(type, url, obj);
    vs.push(v);
    return v;
  };
  velox.WS = {};
  velox.ws = function(url, obj) {
    return velox(velox.WS, url, obj)
  };
  velox.SSE = {};
  velox.sse = function(url, obj) {
    return velox(velox.SSE, url, obj)
  };
  velox.proto = proto;
  velox.online = true;
  //special merge - ignore $properties
  // x <- y
  var merge = function(x, y) {
    if (!x || typeof x !== "object" || !y || typeof y !== "object")
      return y;
    var k;
    if (x instanceof Array && y instanceof Array) {
      //remove extra elements
      while (x.length > y.length)
        x.pop();
    } else {
      //remove extra properties
      for (k in x)
        if (k[0] !== "$" && !(k in y))
          delete x[k];
    }
    for (k in y)
      x[k] = merge(x[k], y[k]);
    return x;
  };
  //global status change handler
  function onstatus(event) {
    velox.online = navigator.onLine;
    for(var i = 0; i < rts.length; i++) {
      if(velox.online && rts[i].autoretry)
        rts[i].retry();
    }
  }
  window.addEventListener('online',  onstatus);
  window.addEventListener('offline', onstatus);

  //helpers
  var events = ["message","error","open","close"];
  var loc = window.location;
  //velox class - represents a single websocket (Conn on the server-side)
  function Velox(type, url, obj) {
    switch(type) {
    case velox.WS:
      this.ws = true; break;
    case velox.SSE:
      this.sse = true; break;
    default:
      throw "Type must be velox.WS or velox.SSE";
    }
    if(!url) {
      url = "/velox";
    }
    if(!(/^(ws|http)s?:/.test(url))) {
      url = loc.protocol + "//" + loc.host + url;
    }
    if(this.ws) {
      url = url.replace(/^http/, "ws");
    }
    this.url = url;
    if(!obj || typeof obj !== "object")
      throw "Invalid object";
    this.obj = obj;
    this.version = 0;
    this.onupdate = function() {/*noop*/};
    this.onerror = function() {/*noop*/};
    this.connected = false;
    this.connect();
  }
  Velox.prototype = {
    connect: function() {
      this.autoretry = true;
      this.retry();
    },
    retry: function() {
      clearTimeout(this.retry.t);
      if(this.conn)
        this.cleanup();
      if(!this.delay)
        this.delay = 100;
      var url = this.url + (/\?/.test(this.url) ? "&" : "?") + "p="+proto+"&v="+this.version
      if(this.ws) {
        this.conn = new WebSocket(url);
      } else {
        this.conn = new EventSource(url, { withCredentials: true });
      }
      var _this = this;
      events.forEach(function(e) {
        _this.conn["on"+e] = _this["conn"+e].bind(_this);
      });
      this.ping.t = setInterval(this.ping.bind(this), 30 * 1000);
    },
    disconnect: function() {
      this.autoretry = false;
      this.cleanup();
    },
    cleanup: function(){
      if(!this.conn)
        return;
      var _this = this;
      events.forEach(function(e) {
        _this.conn["on"+e] = null;
      });
      if(this.conn &&
          (this.conn instanceof EventSource && this.conn.readyState !== EventSource.CLOSED) ||
          (this.conn instanceof WebSocket && this.conn.readyState !== WebSocket.CLOSED)) {
        this.conn.close();
      }
      this.conn = null;
      clearTimeout(this.ping.t);
    },
    send: function(data) {
      if(this.conn && this.conn instanceof WebSocket && this.conn.readyState === WebSocket.OPEN) {
        return this.conn.send(data);
      }
    },
    ping: function() {
      this.send("ping");
    },
    connmessage: function(event) {
      var str = event.data;
      if (str === "ping") return;
      var update;
      try {
        update = JSON.parse(str);
      } catch(err) {
        this.onerror(err);
        return;
      }
      if(!update.body || !this.obj) {
        this.onerror("null objects");
        return;
      }
      //perform update
      if(update.delta)
        jsonpatch.apply(this.obj, update.body);
      else
        merge(this.obj, update.body);
      //auto-angular
      if(typeof this.obj.$apply === "function")
        this.obj.$apply();
      if(typeof this.onupdate === "function")
        this.onupdate();
      this.version = update.version;
      //successful msg resets retry counter
      this.delay = 100;
    },
    connopen: function() {
      this.connected = true;
    },
    connclose: function() {
      this.connected = false;
      this.delay *= 2;
      if(this.autoretry && velox.online) {
        this.retry.t = setTimeout(this.connect.bind(this), this.delay);
      }
    },
    connerror: function(err) {
      // console.warn(err);
    }
  };
  //publicise
  window.velox = velox;
}(window, document, undefined));
