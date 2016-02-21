// velox - v0.2.0 - https://github.com/jpillora/velox
// Jaime Pillora <dev@jpillora.com> - MIT Copyright 2016
/* global window,WebSocket */
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
    for(var i = 0; i < vs.length; i++)
      if(velox.online && vs[i].autoretry)
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
;(function (global) {

if ("EventSource" in global) return;

var reTrim = /^(\s|\u00A0)+|(\s|\u00A0)+$/g;

var EventSource = function (url) {
  var eventsource = this,
      interval = 500, // polling interval
      lastEventId = null,
      cache = '';

  if (!url || typeof url != 'string') {
    throw new SyntaxError('Not enough arguments');
  }

  this.URL = url;
  this.readyState = this.CONNECTING;
  this._pollTimer = null;
  this._xhr = null;

  function pollAgain(interval) {
    eventsource._pollTimer = setTimeout(function () {
      poll.call(eventsource);
    }, interval);
  }

  function poll() {
    try { // force hiding of the error message... insane?
      if (eventsource.readyState == eventsource.CLOSED) return;

      // NOTE: IE7 and upwards support
      var xhr = new XMLHttpRequest();
      xhr.open('GET', eventsource.URL, true);
      xhr.setRequestHeader('Accept', 'text/event-stream');
      xhr.setRequestHeader('Cache-Control', 'no-cache');
      // we must make use of this on the server side if we're working with Android - because they don't trigger
      // readychange until the server connection is closed
      xhr.setRequestHeader('X-Requested-With', 'XMLHttpRequest');

      if (lastEventId != null) xhr.setRequestHeader('Last-Event-ID', lastEventId);
      cache = '';

      xhr.timeout = 50000;
      xhr.onreadystatechange = function () {
        if (this.readyState == 3 || (this.readyState == 4 && this.status == 200)) {
          // on success
          if (eventsource.readyState == eventsource.CONNECTING) {
            eventsource.readyState = eventsource.OPEN;
            eventsource.dispatchEvent('open', { type: 'open' });
          }

          var responseText = '';
          try {
            responseText = this.responseText || '';
          } catch (e) {}

          // process this.responseText
          var parts = responseText.substr(cache.length).split("\n"),
              eventType = 'message',
              data = [],
              i = 0,
              line = '';

          cache = responseText;

          // TODO handle 'event' (for buffer name), retry
          for (; i < parts.length; i++) {
            line = parts[i].replace(reTrim, '');
            if (line.indexOf('event') == 0) {
              eventType = line.replace(/event:?\s*/, '');
            } else if (line.indexOf('retry') == 0) {
              retry = parseInt(line.replace(/retry:?\s*/, ''));
              if(!isNaN(retry)) { interval = retry; }
            } else if (line.indexOf('data') == 0) {
              data.push(line.replace(/data:?\s*/, ''));
            } else if (line.indexOf('id:') == 0) {
              lastEventId = line.replace(/id:?\s*/, '');
            } else if (line.indexOf('id') == 0) { // this resets the id
              lastEventId = null;
            } else if (line == '') {
              if (data.length) {
                var event = new MessageEvent(data.join('\n'), eventsource.url, lastEventId);
                eventsource.dispatchEvent(eventType, event);
                data = [];
                eventType = 'message';
              }
            }
          }

          if (this.readyState == 4) pollAgain(interval);
          // don't need to poll again, because we're long-loading
        } else if (eventsource.readyState !== eventsource.CLOSED) {
          if (this.readyState == 4) { // and some other status
            // dispatch error
            eventsource.readyState = eventsource.CONNECTING;
            eventsource.dispatchEvent('error', { type: 'error' });
            pollAgain(interval);
          } else if (this.readyState == 0) { // likely aborted
            pollAgain(interval);
          } else {
          }
        }
      };

      xhr.send();

      setTimeout(function () {
        xhr.abort();
      }, xhr.timeout);

      eventsource._xhr = xhr;

    } catch (e) { // in an attempt to silence the errors
      eventsource.dispatchEvent('error', { type: 'error', data: e.message }); // ???
    }
  };

  poll(); // init now
};

EventSource.prototype = {
  close: function () {
    // closes the connection - disabling the polling
    this.readyState = this.CLOSED;
    clearInterval(this._pollTimer);
    this._xhr.abort();
  },
  CONNECTING: 0,
  OPEN: 1,
  CLOSED: 2,
  dispatchEvent: function (type, event) {
    var handlers = this['_' + type + 'Handlers'];
    if (handlers) {
      for (var i = 0; i < handlers.length; i++) {
        handlers[i].call(this, event);
      }
    }

    if (this['on' + type]) {
      this['on' + type].call(this, event);
    }
  },
  addEventListener: function (type, handler) {
    if (!this['_' + type + 'Handlers']) {
      this['_' + type + 'Handlers'] = [];
    }

    this['_' + type + 'Handlers'].push(handler);
  },
  removeEventListener: function (type, handler) {
    var handlers = this['_' + type + 'Handlers'];
    if (!handlers) {
      return;
    }
    for (var i = handlers.length - 1; i >= 0; --i) {
      if (handlers[i] === handler) {
        handlers.splice(i, 1);
        break;
      }
    }
  },
  onerror: null,
  onmessage: null,
  onopen: null,
  readyState: 0,
  URL: ''
};

var MessageEvent = function (data, origin, lastEventId) {
  this.data = data;
  this.origin = origin;
  this.lastEventId = lastEventId || '';
};

MessageEvent.prototype = {
  data: null,
  type: 'message',
  lastEventId: '',
  origin: ''
};

if ('module' in global) module.exports = EventSource;
global.EventSource = EventSource;

})(this);
