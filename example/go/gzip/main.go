package main

import (
	"compress/gzip"
	"log"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/NYTimes/gziphandler"
	"github.com/jpillora/velox"
)

type Foo struct {
	//required velox state, adds sync state and a Push() method
	velox.State
	//optional mutex, prevents race conditions (foo.Push will make use of the sync.Locker interface)
	sync.Mutex
	A, B int
	C    Bar
}

type Bar struct {
	X, Y int
}

func main() {
	//state we wish to sync
	foo := &Foo{A: 21, B: 42}
	go func() {
		for {
			foo.Lock()
			foo.C.X = rand.Intn(99)
			foo.C.Y = rand.Intn(99)
			foo.Unlock()
			//push to all connections
			foo.Push()
			//do other stuff...
			time.Sleep(250 * time.Millisecond)
		}
	}()
	//sync handlers
	http.Handle("/velox.js", velox.JS)
	//WARNING: minSize=0 is very important!
	minSize := 0
	gzipper, _ := gziphandler.NewGzipLevelAndMinSize(
		gzip.DefaultCompression, minSize)
	http.Handle("/sync", gzipper(velox.SyncHandler(foo)))
	//index handler
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write(indexhtml)
	})
	//listen!
	log.Printf("Listening on 3000...")
	log.Fatal(http.ListenAndServe(":3000", nil))
}

var indexhtml = []byte(`
<div>Status: <b id="status">disconnected</b></div>
<pre id="example"></pre>
<script src="/velox.js?dev=1"></script>
<script>
var foo = {};
var v = velox("/sync", foo);
v.onchange = function(isConnected) {
	document.querySelector("#status").innerHTML = isConnected ? "connected" : "disconnected";
};
v.onupdate = function() {
	document.querySelector("#example").innerHTML = JSON.stringify(foo, null, 2);
};
</script>
`)
