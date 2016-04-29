/* global window,WebSocket */
(function(window, document) {
  //velox protocol version
  var proto = "v2";
  //public method
  var velox = function(url, obj) {
    if(velox.DEFAULT === velox.WS && window.WebSocket)
      return velox.ws(url, obj);
    else
      return velox.sse(url, obj);
  };
  velox.WS = velox.DEFAULT = {ws:true};
  velox.ws = function(url, obj) {
    return new Velox(velox.WS, url, obj)
  };
  velox.SSE = {sse:true};
  velox.sse = function(url, obj) {
    return new Velox(velox.SSE, url, obj)
  };
  velox.proto = proto;
  velox.online = true;
  //global status change handler
  //performs instant retries when the users
  //internet connection returns
  var vs = velox.connections = [];
  function onstatus(event) {
    velox.online = navigator.onLine;
    if(velox.online)
      for(var i = 0; i < vs.length; i++)
        if(vs[i].retrying)
          vs[i].retry();
  }
  window.addEventListener('online',  onstatus);
  window.addEventListener('offline', onstatus);
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
    this.url = url;
    if(!obj || typeof obj !== "object")
      throw "Invalid object";
    this.obj = obj;
    this.id = "";
    this.version = 0;
    this.onupdate = function() {/*noop*/};
    this.onerror = function() {/*noop*/};
    this.onconnect = function() {/*noop*/};
    this.ondisconnect = function() {/*noop*/};
    this.onchange = function() {/*noop*/};
    this.connected = false;
    this.connect();
  }
  Velox.prototype = {
    connect: function() {
      if(vs.indexOf(this) === -1)
        vs.push(this);
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
      //set url
      var url = this.url;
      if(!(/^(ws|http)s?:/.test(url))) {
        url = loc.protocol + "//" + loc.host + url;
      }
      if(this.ws) {
        url = url.replace(/^http/, "ws");
      }
      var params = [];
      if(this.version) params.push("v="+this.version);
      if(this.id) params.push("id="+this.id);
      if(params.length) url += (/\?/.test(this.url) ? "&" : "?") + params.join("&");
      //connect!
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
      this.sleepCheck.last = null;
      this.sleepCheck();
    },
    disconnect: function() {
      var i = vs.indexOf(this);
      if(i >= 0) vs.splice(i, 1);
      this.retrying = false;
      this.cleanup();
    },
    cleanup: function() {
      clearTimeout(this.pingout.t);
      if(!this.conn)
        return;
      var c = this.conn;
      this.conn = null;
      events.forEach(function(e) {
        c["on"+e] = null;
      });
      if(c && (c instanceof EventSource && c.readyState !== EventSource.CLOSED) ||
          (c instanceof WebSocket && c.readyState !== WebSocket.CLOSED)) {
        c.close();
      }
      this.statusCheck();
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
      var woken = data.last && (now - data.last) > 30*1000;
      data.last = now;
      data.t = setTimeout(this.sleepCheck.bind(this), 5*1000);
      if(woken) this.retry();
    },
    statusCheck: function(err) {
      var curr = !!this.connected;
      var next = undefined;
      var c = this.conn;
      if(c && (c instanceof EventSource && c.readyState === EventSource.OPEN) ||
            (c instanceof WebSocket && c.readyState === WebSocket.OPEN)) {
        next = true;
      } else {
        next = false;
      }
      if(curr !== next) {
        this.connected = next;
        this.onchange(this.connected);
        if(this.connected) {
          this.onconnect();
        } else {
          this.ondisconnect();
        }
      }
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
      if(update.id) {
        this.id = update.id;
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
      this.statusCheck();
      this.pingin(); //treat initial connection as incoming ping
    },
    connclose: function() {
      this.statusCheck();
      //backoff retry connection
      this.delay *= 2;
      if(this.retrying && velox.online) {
        this.retry.t = setTimeout(this.connect.bind(this), this.delay);
      }
    },
    connerror: function(err) {
      this.statusCheck();
      this.onerror(err);
    }
  };
  //publicise
  window.velox = velox;
}(window, document, undefined));
