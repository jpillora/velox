/* global window,WebSocket */
(function(window, document) {
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
  //recursive merge (x <- y) - ignore $properties
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
    //iterate over either elements/properties
    for (k in y)
      x[k] = merge(x[k], y[k]);
    return x;
  };
  //global status change handler
  function onstatus(event) {
    velox.online = navigator.onLine;
    for(var i = 0; i < vs.length; i++)
      if(velox.online && vs[i].retrying)
        vs[i].retry();
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
      if(!window.WebSocket)
        throw "This browser does not support WebSockets";
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
    this.onconnect = function() {/*noop*/};
    this.ondisconnect = function() {/*noop*/};
    this.connected = false;
    this.connect();
  }
  Velox.prototype = {
    connect: function() {
      this.retrying = true;
      this.retry();
    },
    retry: function() {
      clearTimeout(this.retry.t);
      if(this.conn)
        this.cleanup();
      if(!this.retrying)
        return;
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
      this.pingout.t = setInterval(this.pingout.bind(this), 30 * 1000);
      this.sleepCheck();
    },
    disconnect: function() {
      this.retrying = false;
      this.cleanup();
    },
    cleanup: function() {
      clearTimeout(this.pingout.t);
      if(!this.conn)
        return;
      var conn = this.conn;
      this.conn = null;
      events.forEach(function(e) {
        conn["on"+e] = null;
      });
      if(conn && (conn instanceof EventSource && conn.readyState !== EventSource.CLOSED) ||
          (conn instanceof WebSocket && conn.readyState !== WebSocket.CLOSED)) {
        conn.close();
      }
    },
    send: function(data) {
      if(this.conn && this.conn instanceof WebSocket && this.conn.readyState === WebSocket.OPEN) {
        return this.conn.send(data);
      }
    },
    pingout: function() {
      this.send("ping");
    },
    pingin: function() {
      //ping receievd by server, reset last timer, start death timer for 30secs
      clearTimeout(this.pingin.t);
      this.pingin.t = setTimeout(this.retry.bind(this), 30 * 1000);
    },
    sleepCheck: function() {
      var data = this.sleepCheck;
      clearInterval(data.t);
      var now = Date.now();
      //should be ~5secs, over ~30sec - assume woken from sleep
      if(data.last && (now - data.last) > 30*1000)
        this.retry();
      data.last = now;
      data.t = setTimeout(this.sleepCheck.bind(this), 5*1000);
    },
    connmessage: function(event) {
      var update;
      try {
        update = JSON.parse(event.data);
      } catch(err) {
        this.onerror(err);
        return;
      }
      if(update.ping) {
        this.pingin();
        return
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
      this.onupdate(this.obj);
      this.version = update.version;
      //successful msg resets retry counter
      this.delay = 100;
    },
    connopen: function() {
      this.connected = true;
      this.onconnect();
      this.pingin(); //treat initial connection as ping
    },
    connclose: function() {
      this.connected = false;
      this.ondisconnect();
      //backoff retry connection
      this.delay *= 2;
      if(this.retrying && velox.online) {
        this.retry.t = setTimeout(this.connect.bind(this), this.delay);
      }
    },
    connerror: function(err) {
      this.onerror(err);
    }
  };
  //publicise
  window.velox = velox;
}(window, document, undefined));
