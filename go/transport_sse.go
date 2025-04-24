package velox

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/jpillora/eventsource"
)

type eventSourceTransport struct {
	mut          sync.Mutex
	writeTimeout time.Duration
	w            http.ResponseWriter
	isConnected  bool
	connected    chan struct{}
}

func (es *eventSourceTransport) connect(w http.ResponseWriter, r *http.Request) error {
	//connection controls
	es.isConnected = true
	es.connected = make(chan struct{})
	go func() {
		select {
		case <-es.connected:
		case <-r.Context().Done(): //client disconnected early
			es.close()
		}
	}()
	//eventsource headers
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Vary", "Accept")
	w.Header().Set("Content-Type", "text/event-stream")
	//connection is now expecting a stream of events
	es.w = w
	return nil
}

func (es *eventSourceTransport) send(upd *Update) error {
	b, err := json.Marshal(upd)
	if err != nil {
		return err
	}
	sent := make(chan error)
	go func() {
		if es.IsConnected() {
			err := eventsource.WriteEvent(es.w, eventsource.Event{
				ID:   strconv.FormatInt(upd.Version, 10),
				Data: b,
			})
			sent <- err
		}
	}()
	select {
	case <-time.After(es.writeTimeout):
		return errors.New("timeout")
	case err := <-sent:
		return err
	}
}

func (es *eventSourceTransport) wait() error {
	<-es.connected
	return nil
}

func (es *eventSourceTransport) IsConnected() bool {
	es.mut.Lock()
	defer es.mut.Unlock()
	return es.isConnected
}

func (es *eventSourceTransport) close() error {
	if es.IsConnected() {
		//unblocking the wait, causes the http handler to return
		close(es.connected)

		es.mut.Lock()
		es.isConnected = false
		es.mut.Unlock()
	}
	return nil
}
