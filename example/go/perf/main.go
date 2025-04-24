package main

import (
	"bytes"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	"net/http/pprof"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/jpillora/sizestr"
	"github.com/jpillora/velox"
)

const debug = false

type Book struct {
	//required velox state, adds sync state and a Push() method
	velox.State
	//optional mutex, prevents race conditions (foo.Push will make use of the sync.Locker interface)
	sync.Mutex
	Lines Store
	N     int
}

func main() {
	const testFile = "/tmp/text.txt"
	if _, err := os.Stat(testFile); err != nil {
		log.Printf("downloading %s", testFile)
		resp, err := http.Get("https://dlg.usg.edu/record/dlg_zlgb_gb0176/fulltext.text")
		if err != nil {
			log.Fatal(err)
		}
		f, err := os.Create(testFile)
		if err != nil {
			log.Fatal(err)
		}
		io.Copy(f, resp.Body)
		f.Close()
	}
	//state we wish to sync
	b := &Book{N: 0}
	b.Lines = &Slice{}
	// b.Lines = Map{}
	txt, err := ioutil.ReadFile(testFile)
	if err != nil {
		log.Fatal(err)
	}
	lines := bytes.Split(txt, []byte{'\n'})
	for _, line := range lines {
		l := strings.TrimSpace(string(line))
		if l == "" {
			continue
		}
		b.Lines.add(l)
		b.N = b.Lines.len()
		if b.N == 3000 {
			break
		}
	}
	log.Printf("read %d lines", b.N)
	//swap lines
	go func() {
		for {
			b.Lock()
			if b.Lines.len() > 0 {
				i := b.Lines.rand()
				b.Lines.del(i)
				log.Printf(">>> delete %d", i)
			}
			b.N = b.Lines.len()
			if b.N >= 2 {
				x, y := b.Lines.rand(), b.Lines.rand()
				b.Lines.swap(x, y)
				log.Printf(">>> swap %d %d", x, y)
			}
			b.Unlock()
			b.Push()
			time.Sleep(50 * time.Millisecond)
		}
	}()

	//show goroutine/memory usage
	if debug {
		go func() {
			mem := runtime.MemStats{}
			for {
				g := runtime.NumGoroutine()
				runtime.ReadMemStats(&mem)
				time.Sleep(1000 * time.Millisecond)
				log.Printf("goroutines: %03d, allocated %s", g, sizestr.ToString(int64(mem.Alloc)))
			}
		}()
	}

	b.State.WriteTimeout = 3 * time.Second

	//sync handlers
	router := http.NewServeMux()
	router.Handle("/velox.js", velox.JS)
	router.Handle("/sync", velox.SyncHandler(b))
	router.Handle("/pprof/goroutine", pprof.Handler("goroutine"))
	//index handler
	router.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write(indexhtml)
	})

	//jpillora/gziphandler ignores websocket/eventsource connections
	//and gzips the rest
	// gzippedRouter := gziphandler.GzipHandler(router)

	//listen!
	log.Printf("Listening on 3000...")
	log.Fatal(http.ListenAndServe(":3000", router))
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
	const json = JSON.stringify(foo, null, 2)
	document.querySelector("#example").innerHTML = json.length + " bytes\n\n" + json;
};
</script>
`)

// compare performance of maps and slices

type Store interface {
	add(s string)
	swap(i, j int)
	del(i int)
	len() int
	rand() int
}

type Slice []string

func (s *Slice) add(str string) {
	*s = append(*s, str)
}

func (s Slice) swap(i, j int) {
	if i < s.len() && j < s.len() {
		s[i], s[j] = s[j], s[i]
	}
}

func (s *Slice) del(i int) {
	if i < s.len() {
		head := (*s)[0:i]
		if i-1 == s.len() {
			*s = head // drop last
		} else {
			tail := (*s)[i+1:]
			*s = append(head, tail...)
		}
	}
}

func (s Slice) len() int {
	return len(s)
}

func (s Slice) rand() int {
	return rand.Intn(s.len())
}

type Map map[int]string

func (m Map) add(str string) {
	k := len(m)
	m[k] = str
}

func (m Map) swap(i, j int) {
	m[i], m[j] = m[j], m[i]
}

func (m Map) del(i int) {
	delete(m, i)
}

func (m Map) len() int {
	return len(m)
}

func (m Map) rand() int {
	n := rand.Intn(20)
	for {
		for k := range m {
			if n == 0 {
				return k
			}
			n--
		}
	}
}
