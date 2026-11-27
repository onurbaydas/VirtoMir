//go:build linux

package systemd

import (
	"context"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// listen opens a datagram socket like systemd's notify socket; name starting with '@' is an
// abstract address.
func listen(t *testing.T, name string) (*net.UnixConn, string) {
	t.Helper()
	addr := name
	if !strings.HasPrefix(name, "@") {
		addr = filepath.Join(t.TempDir(), name)
	}
	sockAddr := addr
	if strings.HasPrefix(addr, "@") {
		sockAddr = "\x00" + addr[1:]
	}
	c, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: sockAddr, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c, addr
}

func read(t *testing.T, c *net.UnixConn, within time.Duration) (string, bool) {
	t.Helper()
	buf := make([]byte, 256)
	c.SetReadDeadline(time.Now().Add(within))
	n, err := c.Read(buf)
	if err != nil {
		return "", false
	}
	return string(buf[:n]), true
}

func TestNotifyPathAndAbstractSocket(t *testing.T) {
	for _, name := range []string{"notify.sock", "@virtomir-test-" + strconv.FormatInt(time.Now().UnixNano(), 36)} {
		c, addr := listen(t, name)
		t.Setenv("NOTIFY_SOCKET", addr)
		if err := Notify(Ready); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got, ok := read(t, c, time.Second); !ok || got != Ready {
			t.Errorf("%s: got %q", name, got)
		}
	}
}

func TestNotifyWithoutSocketSendsNothing(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")
	if err := Notify(Ready); err != nil {
		t.Fatal(err)
	}
}

func TestStartWatchdog(t *testing.T) {
	t.Setenv("WATCHDOG_USEC", "")
	if StartWatchdog(t.Context(), nil) {
		t.Fatal("started without WATCHDOG_USEC")
	}

	c, addr := listen(t, "notify.sock")
	t.Setenv("NOTIFY_SOCKET", addr)
	t.Setenv("WATCHDOG_USEC", "200000") // pings every 500 ms, the shortest interval
	ctx, cancel := context.WithCancel(t.Context())
	var logged bool
	if !StartWatchdog(ctx, func(string, ...any) { logged = true }) {
		t.Fatal("not started")
	}
	if !logged {
		t.Error("nothing logged")
	}
	for i := range 2 {
		if got, ok := read(t, c, 2*time.Second); !ok || got != Watchdog {
			t.Fatalf("ping %d: got %q", i+1, got)
		}
	}
	cancel()
	// A ping that was already on its way may still arrive; after that, silence.
	read(t, c, 100*time.Millisecond)
	if got, ok := read(t, c, 1200*time.Millisecond); ok {
		t.Errorf("pinged %q after the context was cancelled", got)
	}
}
