package velox

import (
	"net/http"
	"testing"
)

// connCaptureTransport is deliberately synchronous: conn.send is responsible
// for deciding which fields advance its protocol cursor, independent of any
// transport buffering.
type connCaptureTransport struct{}

func (*connCaptureTransport) connect(http.ResponseWriter, *http.Request) error { return nil }
func (*connCaptureTransport) send(*Update) error                               { return nil }
func (*connCaptureTransport) wait() error                                      { return nil }
func (*connCaptureTransport) drain()                                           {}
func (*connCaptureTransport) close() error                                     { return nil }

func TestConnPingDoesNotResetSyncProgress(t *testing.T) {
	c := newConn(1, "test", &State{}, 17, ProtoVersion, "old-root")
	c.transport = &connCaptureTransport{}

	if err := c.send(&Update{Ping: true}); err != nil {
		t.Fatal(err)
	}
	if got := c.Version(); got != 17 {
		t.Fatalf("ping reset connection version to %d, want 17", got)
	}
	if c.baseHash != "old-root" {
		t.Fatalf("ping reset connection root to %q", c.baseHash)
	}

	if err := c.send(&Update{Version: 18, Root: "new-root"}); err != nil {
		t.Fatal(err)
	}
	if got := c.Version(); got != 18 {
		t.Fatalf("state update version = %d, want 18", got)
	}
	if c.baseHash != "new-root" {
		t.Fatalf("state update root = %q, want new-root", c.baseHash)
	}
	if err := c.send(&Update{Version: 19}); err != nil {
		t.Fatal(err)
	}
	if c.baseHash != "" {
		t.Fatalf("v3 clear retained connection root %q", c.baseHash)
	}
}
