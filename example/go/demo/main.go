package main

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	_ "embed"

	"github.com/jpillora/velox"
)

//go:embed index.html
var indexHTML string

type Root struct {
	//required velox state, adds sync state and a Push() method
	velox.State
	//optional mutex, prevents race conditions (foo.Push will make use of the sync.Locker interface)
	sync.Mutex
	Users  `json:"users"`
	Random `json:"random"`
}

type User struct {
	Updated time.Time
	ID      string
	MouseX  int
	MouseY  int
}

type Users struct {
	NumConnections int
	Active         map[string]*User
}

func (s *Root) getUser(w http.ResponseWriter, r *http.Request) *User {
	id := ""
	if c, err := r.Cookie("id"); err == nil {
		id = c.Value
	} else {
		id = randID()
		http.SetCookie(w, &http.Cookie{Name: "id", Value: id, Expires: time.Now().Add(time.Hour)})
	}
	s.Lock()
	// clean out inactive
	for id2, u := range s.Users.Active {
		if time.Since(u.Updated) > 5*time.Minute {
			delete(s.Users.Active, id2)
		}
	}
	u, ok := s.Users.Active[id]
	if !ok {
		u = &User{ID: id}
		s.Users.Active[id] = u
	}
	s.Unlock()
	return u
}

type Random struct {
	Data map[string]int
}

type Bar struct {
	X, Y int
}

func main() {
	//state we wish to sync
	root := &Root{
		State: velox.State{
			// you should normally throttle updates to 1-2 seconds
			Throttle: 20 * time.Millisecond,
		},
		Users: Users{
			Active: map[string]*User{},
		},
		Random: Random{Data: map[string]int{}},
	}
	go func() {
		for {
			//change foo
			root.Lock()
			root.Users.NumConnections = root.State.NumConnections() //show number of connections 'foo' is currently handling
			root.Data[string(rune('A'+rand.Intn(26)))] = rand.Intn(26)
			root.Unlock()
			root.Push()
			//do other stuff...
			time.Sleep(250 * time.Millisecond)
		}
	}()
	//sync handlers
	http.Handle("/velox.js", velox.JS)
	http.Handle("/sync", velox.SyncHandler(root))
	http.Handle("/ping", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := root.getUser(w, r)
		q := r.URL.Query()
		root.Lock()
		u.Updated = time.Now()
		u.MouseX, _ = strconv.Atoi(q.Get("x"))
		u.MouseY, _ = strconv.Atoi(q.Get("y"))
		root.Unlock()
		root.Push()
	}))
	http.Handle("/root", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := json.MarshalIndent(root, "", "  ")
		w.Header().Set("Content-Type", "text/plain")
		w.Write(b)
	}))
	//index handler
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(indexHTML))
	})
	//listen!
	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}
	log.Printf("Listening on :%s...", port)
	s := http.Server{
		Addr: ":" + port,
	}
	log.Fatal(s.ListenAndServe())
}

func randID() string {
	h := md5.Sum([]byte(time.Now().Format(time.RFC3339Nano)))
	id := fmt.Sprintf("%x", h[0:4])
	return id
}
