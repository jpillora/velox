package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/jpillora/velox"
)

type Foo struct {
	velox.State //adds sync state and an Update() method
	A, B        int
	C           []int
	D           string
	E           Bar
}

type Bar struct {
	X, Y int
}

func main() {
	//state we wish to sync
	foo := &Foo{A: 21, B: 42, D: "0"}
	go func() {
		i := 0
		for {
			//change foo
			foo.A++
			if i%2 == 0 {
				foo.B--
			}
			i++
			if i > 10 {
				foo.C = foo.C[1:]
			}
			foo.C = append(foo.C, i)
			if i%5 == 0 {
				foo.E.Y++
			}
			//push to observers
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
<pre id="out"></pre>
<script src="/velox.js"></script>
<script>
	var foo = {};
	var v = velox.sse("/sync", foo);
	v.onupdate = function() {
		out.innerHTML = JSON.stringify(foo, null, 2);
	};
</script>
`)

//NOTE: deltas are not sent in the example since the target object is too small
