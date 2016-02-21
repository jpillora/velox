//go:generate go-bindata -pkg velox -o assets.go velox.js

package velox

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const proto = "v2"

var defaultUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func SyncHandler(gostruct interface{}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := Sync(gostruct, w, r); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
		}
	})
}

func Sync(gostruct interface{}, w http.ResponseWriter, r *http.Request) (*Conn, error) {
	//access gostruct.State via interfaces:
	gosyncable, ok := gostruct.(syncable)
	if !ok {
		return nil, fmt.Errorf("cannot sync: does not embed velox.State")
	}
	//pass gostruct into gostruct.State and get its gostruct.State back out
	state, err := gosyncable.sync(gostruct)
	if err != nil {
		return nil, fmt.Errorf("cannot sync: %s", err)
	}
	//upgrade to websocket
	wsconn, err := defaultUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return nil, fmt.Errorf("cannot upgrade connection: %s", err)
	}
	//first message is the handshake
	handshake := struct {
		Protocol string
		Version  int64
	}{}
	if err := wsconn.ReadJSON(&handshake); err != nil {
		wsconn.WriteMessage(websocket.TextMessage, []byte("Invalid handshake message"))
		wsconn.Close()
		return nil, fmt.Errorf("handshake failed: %s", err)
	}
	if handshake.Protocol != proto {
		wsconn.WriteMessage(websocket.TextMessage, []byte("Invalid protocol version"))
		wsconn.Close()
		return nil, fmt.Errorf("protocol version mismatch")
	}
	//ready
	conn := &Conn{
		ID:        wsconn.RemoteAddr().String(),
		Connected: true,
		uptime:    time.Now(),
		conn:      wsconn,
		version:   handshake.Version,
	}
	//discard all future messages and mark disconnected
	conn.connected.Add(1)
	go func() {
		for {
			//msgType, msgBytes, err
			if _, _, err := wsconn.ReadMessage(); err != nil {
				break
			}
		}
		conn.connected.Done()
	}()
	//hand over to state to keep in sync
	state.subscribe(conn)
	//pass connection to user
	return conn, nil
}

type syncable interface {
	sync(gostruct interface{}) (*State, error)
}

//embedded JS file
var JSBytes = _veloxJs
var JSBytesDecompressed []byte

type jsServe struct{}

var JS *jsServe

func (j *jsServe) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	b := JSBytes
	//lazy decompression
	if !strings.Contains(req.Header.Get("Accept-Encoding"), "gzip") {
		if JSBytesDecompressed == nil {
			buff := bytes.Buffer{}
			g := gzip.NewWriter(&buff)
			g.Write(JSBytes)
			g.Close()
			JSBytesDecompressed = buff.Bytes()
		}
		b = JSBytesDecompressed
	} else {
		w.Header().Set("Content-Encoding", "gzip")
	}
	w.Header().Set("Content-Type", "text/javascript")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.Write(b)
}
