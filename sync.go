//go:generate go-bindata -pkg assets -o assets/assets.go velox.js

package velox

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jpillora/velox/assets"
)

const proto = "v2"

var JS = assets.VeloxJS

var defaultUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

//SyncHandler is a small wrapper around Sync which simply synchronises
//all incoming connections. Use Sync if you wish to implement user authentication
//or any other request-time checks.
func SyncHandler(gostruct interface{}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Sync(gostruct, w, r)
	})
}

//Sync upgrades a given HTTP connection into a WebSocket connection and synchronises
//the provided struct with the client. velox takes responsibility for writing the response
//in the event of failure.
func Sync(gostruct interface{}, w http.ResponseWriter, r *http.Request) (*Conn, error) {
	//access gostruct.State via interfaces:
	gosyncable, ok := gostruct.(interface {
		sync(gostruct interface{}) (*State, error)
	})
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
		conn.Connected = false
		conn.connected.Done()
	}()
	//hand over to state to keep in sync
	state.subscribe(conn)
	//pass connection to user
	return conn, nil
}
