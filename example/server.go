package main

import (
	"log"
	"math/rand"
	"net/http"
	"os"
	"time"

	"github.com/jpillora/velox"
)

type Foo struct {
	velox.State    //adds sync state and an Update() method
	NumConnections int
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
			//show number of connections 'foo' is currently handling
			foo.NumConnections = foo.State.NumConnections()
			//push to all connections
			foo.Push()
			//do other stuff...
			time.Sleep(250 * time.Millisecond)
		}
	}()
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
&lt;pre id="example">&lt;/pre>
&lt;script src="/velox.js">&lt;/script>
&lt;script>
var foo = {};
var v = velox("/sync", foo);
v.onupdate = function() {
	example.innerHTML = JSON.stringify(foo, null, 2);
};
&lt;/script>
</pre>
<a href="https://github.com/jpillora/velox"><img style="position: absolute; z-index: 2; top: 0; right: 0; border: 0;" src="https://s3.amazonaws.com/github/ribbons/forkme_right_darkblue_121621.png" alt="Fork me on GitHub"></a>
<hr>

<!-- example -->
<pre id="example"></pre>
<script src="/velox.js"></script>
<script>
var foo = {};
var v = velox("/sync", foo);
v.onupdate = function() {
	example.innerHTML = JSON.stringify(foo, null, 2);
};
v.onchange = function(isConnected) {
	console.log("is connected", isConnected);
}
</script>
`)

//NOTE: deltas are not sent in the example since the target object is too small
