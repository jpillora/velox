package main

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/donovanhide/eventsource"
)

type TimeEvent time.Time

func (t TimeEvent) Id() string    { return fmt.Sprint(time.Time(t).UnixNano()) }
func (t TimeEvent) Event() string { return "" }
func (t TimeEvent) Data() string  { return time.Time(t).String() }

func main() {

	http.Handle("/events", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("open")
		s := eventsource.NewServer()

		go func() {
			id := 1
			for {
				e := TimeEvent(time.Now())
				log.Printf("publish: %s", e.Data())
				s.Publish([]string{"time"}, &e)
				id++
				if id == 10 {
					break
				}
				time.Sleep(1 * time.Second)
			}
			s.Close()
		}()

		s.Handler("time").ServeHTTP(w, r)
		log.Printf("closed")
	}))

	http.Handle("/", http.FileServer(http.Dir(".")))
	log.Fatal(http.ListenAndServe(":9000", nil))
}
