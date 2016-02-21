package velox

import (
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

//Conn represents a single websocket connection
//being synchronised. Its ID is the the connections remote
//address.
type Conn struct {
	//connection info
	Connected bool
	ID        string
	//internal state
	uptime    time.Time
	conn      *websocket.Conn
	version   int64
	connected sync.WaitGroup
}

//Wait will block until the connection is closed.
func (c *Conn) Wait() {
	c.connected.Wait()
}
