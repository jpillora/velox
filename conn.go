package velox

import (
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

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

func (c *Conn) Wait() {
	c.connected.Wait()
}
