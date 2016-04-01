package main

import (
	"log"
	"math/rand"
	"net/http"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/jpillora/velox"
)

const debug = false

type Foo struct {
	velox.State    //adds sync state and a Push() method
	sync.Mutex     //optionally add locking to help prevent race conditions (foo.Push will make user of the sync.Locker interface)
	NumConnections int
	NumGoRoutines  int     `json:",omitempty"`
	AllocMem       float64 `json:",omitempty"`
	A, B           int
	C              map[string]int
	D              Bar
}

type Bar struct {
	X, Y int
}

func main() {
	//state we wish to sync
	foo := &Foo{A: 21, B: 42, C: map[string]int{}}
	go func() {
		i := 0
		for {
			//change foo
			foo.Lock()
			foo.A++
			if i%2 == 0 {
				foo.B--
			}
			i++
			foo.C[string('A'+rand.Intn(26))] = i
			if i%2 == 0 {
				j := 0
				rmj := rand.Intn(len(foo.C))
				for k, _ := range foo.C {
					if j == rmj {
						delete(foo.C, k)
						break
					}
					j++
				}
			}
			if i%5 == 0 {
				foo.D.X--
				foo.D.Y++
			}
			foo.NumConnections = foo.State.NumConnections() //show number of connections 'foo' is currently handling
			foo.Unlock()
			//push to all connections
			foo.Push()
			//do other stuff...
			time.Sleep(250 * time.Millisecond)
		}
	}()

	//show memory/goroutine stats
	if debug {
		go func() {
			mem := &runtime.MemStats{}
			i := 0
			for {
				foo.NumGoRoutines = runtime.NumGoroutine()
				runtime.ReadMemStats(mem)
				foo.AllocMem = float64(mem.Alloc)
				time.Sleep(100 * time.Millisecond)
				i++
				// if i%10 == 0 { runtime.GC() }
				foo.Push()
			}
		}()
	}

	//sync handlers
	http.Handle("/velox.js", velox.JS)
	http.Handle("/sync", velox.SyncHandler(foo))
	//index handler
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write(indexhtml)
	})
	//listen!
	port := os.Getenv("PORT")
	if port == "" {
		port = "7070"
	}
	log.Printf("Listening on :%s...", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

var indexhtml = []byte(`

<!-- documentation -->
<pre id="code">
&lt;div>&lt;b id="isconnected">disconnected&lt;/b>&lt;/div>
&lt;pre id="example">&lt;/pre>
&lt;script src="/velox.js">&lt;/script>
&lt;script>
var foo = {};
var v = velox("/sync", foo);
v.onupdate = function() {
	example.innerHTML = JSON.stringify(foo, null, 2);
};
v.onchange = function(isConnected) {
	isconnected.innerHTML = isConnected ? "connected" : "disconnected";
}
&lt;/script>
</pre>
<a href="https://github.com/jpillora/velox"><img style="position: absolute; z-index: 2; top: 0; right: 0; border: 0;" src="https://s3.amazonaws.com/github/ribbons/forkme_right_darkblue_121621.png" alt="Fork me on GitHub"></a>
<hr>

<!-- example -->
<div><b id="isconnected">disconnected</b></div>
<pre id="example"></pre>
<script src="/velox.js"></script>
<script>
var foo = {};
var v = velox.sse("/sync", foo);
v.onupdate = function() {
	example.innerHTML = JSON.stringify(foo, null, 2);
};
v.onchange = function(isConnected) {
	isconnected.innerHTML = isConnected ? "connected" : "disconnected";
}
</script>
`)

//NOTE: deltas are not sent in the example since the target object is too small
