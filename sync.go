package velox

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jpillora/velox/assets"
)

//NOTE(@jpillora): always assume v1, include v2 in checks when we get there...
const proto = "v1"

var JS = assets.VeloxJS

type syncer interface {
	sync(gostruct interface{}) (*State, error)
}

//SyncHandler is a small wrapper around Sync which simply synchronises
//all incoming connections. Use Sync if you wish to implement user authentication
//or any other request-time checks.
func SyncHandler(gostruct interface{}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if conn, err := Sync(gostruct, w, r); err == nil {
			//do an initial push only to this client
			conn.Push()
			conn.Wait()
		}
	})
}

//Sync upgrades a given HTTP connection into a WebSocket connection and synchronises
//the provided struct with the client. velox takes responsibility for writing the response
//in the event of failure.
func Sync(gostruct interface{}, w http.ResponseWriter, r *http.Request) (Conn, error) {
	//access gostruct.State via interfaces:
	gosyncable, ok := gostruct.(syncer)
	if !ok {
		return nil, fmt.Errorf("cannot sync: does not embed velox.State")
	}
	//extract internal state from gostruct
	state, err := gosyncable.sync(gostruct)
	if err != nil {
		return nil, fmt.Errorf("cannot sync: %s", err)
	}
	version := int64(0)
	//matching id, allow user to pick version
	if id := r.URL.Query().Get("id"); id != "" && id == state.id {
		if v, err := strconv.ParseInt(r.URL.Query().Get("v"), 10, 64); err == nil && v > 0 {
			version = v
		}
	}
	//ready
	conn := &conn{
		id:        r.RemoteAddr,
		state:     state,
		connected: true,
		uptime:    time.Now(),
		version:   version,
	}
	if r.Header.Get("Accept") == "text/event-stream" {
		conn.transport = &evtSrcTrans{}
	} else if r.Header.Get("Upgrade") == "websocket" {
		conn.transport = &wsTrans{}
	} else {
		return nil, fmt.Errorf("Invalid sync request")
	}
	//connect to client over set transport
	conn.waiter.Add(1)
	isConnected := make(chan bool)
	go func() {
		//ping loop (every 25s, browser timesout after 30s)
		go func() {
			for conn.connected {
				time.Sleep(25 * time.Second)
				conn.transport.send(&update{Ping: true})
			}
		}()
		//connect and block
		if err = conn.transport.connect(w, r, isConnected); err != nil {
			//TODO(jpillora): log nicely
			// log.Printf("connection error: %s", err)
		}
		//disconnected, done waiting
		conn.connected = false
		conn.waiter.Done()
		conn.close()
	}()
	//wait here until transport completes connection
	<-isConnected
	//hand over to state to keep in sync
	state.subscribe(conn)
	//pass connection to user
	return conn, nil
}
