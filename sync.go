package velox

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jpillora/velox/assets"
)

const proto = "v2"

var JS = assets.VeloxJS

//SyncHandler is a small wrapper around Sync which simply synchronises
//all incoming connections. Use Sync if you wish to implement user authentication
//or any other request-time checks.
func SyncHandler(gostruct interface{}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if conn, err := Sync(gostruct, w, r); err == nil {
			conn.Wait()
		}
	})
}

//Sync upgrades a given HTTP connection into a WebSocket connection and synchronises
//the provided struct with the client. velox takes responsibility for writing the response
//in the event of failure.
func Sync(gostruct interface{}, w http.ResponseWriter, r *http.Request) (Conn, error) {
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
	if r.URL.Query().Get("p") != proto {
		return nil, fmt.Errorf("protocol version mismatch")
	}
	version := int64(0)
	if v, err := strconv.ParseInt(r.URL.Query().Get("v"), 10, 64); err == nil {
		version = v
	}
	//ready
	conn := &conn{
		id:        r.RemoteAddr,
		connected: true,
		uptime:    time.Now(),
		version:   version,
	}
	if r.Header.Get("Accept") == "text/event-stream" {
		conn.transport = &evtSrcTrans{}
	} else {
		conn.transport = &wsTrans{}
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
	}()
	<-isConnected
	//initial update
	if state.version != conn.version {
		conn.transport.send(&update{
			Body:    state.bytes,
			Version: state.version,
		})
	}
	//hand over to state to keep in sync
	state.subscribe(conn)
	//pass connection to user
	return conn, nil
}
