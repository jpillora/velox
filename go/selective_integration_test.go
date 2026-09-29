package velox_test

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	velox "github.com/jpillora/velox/go"
)

func TestSelectiveClientOverSSE(t *testing.T) {
	type serverData struct {
		sync.Mutex
		Chosen struct {
			Value int `json:"value"`
		} `json:"chosen"`
		Ignored string `json:"ignored"`
	}
	source := &serverData{Ignored: "must not sync"}
	source.Chosen.Value = 7
	server := httptest.NewServer(velox.SyncHandler(source))
	defer server.Close()
	type selectedData struct {
		Value int `json:"value"`
	}
	local := &selectedData{}
	client, err := velox.NewClient(server.URL, local)
	if err != nil {
		t.Fatal(err)
	}
	client.Path = "chosen"
	client.Retry = false
	updates := make(chan int, 1)
	client.OnUpdate = func() { updates <- local.Value }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Connect(ctx) }()
	defer func() { cancel(); client.Disconnect() }()
	select {
	case value := <-updates:
		if value != 7 {
			t.Fatalf("selected value = %d, want 7", value)
		}
	case err := <-done:
		t.Fatalf("client stopped before update: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("selected update did not arrive")
	}
}

func TestMultiPathClientOverSSE(t *testing.T) {
	type sourceData struct {
		sync.Mutex
		Machines struct {
			Local struct {
				Name string `json:"name"`
			} `json:"local"`
			Remote string `json:"remote"`
		} `json:"machines"`
		Settings struct {
			Theme  string `json:"theme"`
			Secret string `json:"secret"`
		} `json:"settings"`
		Ignored string `json:"ignored"`
	}
	source := &sourceData{Ignored: "skip"}
	source.Machines.Local.Name = "laptop"
	source.Machines.Remote = "skip"
	source.Settings.Theme = "dark"
	source.Settings.Secret = "skip"
	server := httptest.NewServer(velox.SyncHandler(source))
	defer server.Close()
	type localData struct {
		Machines struct {
			Local struct {
				Name string `json:"name"`
			} `json:"local"`
		} `json:"machines"`
		Settings struct {
			Theme string `json:"theme"`
		} `json:"settings"`
	}
	local := &localData{}
	client, err := velox.NewClient(server.URL, local)
	if err != nil {
		t.Fatal(err)
	}
	client.Paths = []string{"settings.theme", "machines.local"}
	client.Retry = false
	updated := make(chan struct{}, 1)
	client.OnUpdate = func() { updated <- struct{}{} }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Connect(ctx) }()
	defer func() { cancel(); client.Disconnect() }()
	select {
	case <-updated:
		if local.Machines.Local.Name != "laptop" || local.Settings.Theme != "dark" {
			t.Fatalf("wrong projection: %+v", local)
		}
	case err := <-done:
		t.Fatalf("client stopped before update: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("multi-path update did not arrive")
	}
}
