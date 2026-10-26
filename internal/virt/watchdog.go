package virt

import (
	"net"
	"sync"
	"time"

	"github.com/digitalocean/go-libvirt/socket"
)

// go-libvirt calls take no deadline: when libvirtd hangs (not dies), every call waits for
// ever and the live view keeps showing the last state. The watchdog pings libvirt on its own
// connection; when a ping gets no answer in time, it closes the socket. That fails every
// waiting call, the live hub reports libvirt as unreachable and the next call reconnects.
// Long operations (copying an image) do not trip it: libvirtd answers other calls meanwhile.

var (
	watchEvery   = 15 * time.Second
	watchTimeout = 20 * time.Second
)

// keepConn is a socket.Dialer that remembers the connection it made, so the watchdog can
// close it without going through libvirt (Disconnect would send an RPC and hang too).
type keepConn struct {
	socket.Dialer
	mu   sync.Mutex
	conn net.Conn
}

func (k *keepConn) Dial() (net.Conn, error) {
	c, err := k.Dialer.Dial()
	k.mu.Lock()
	k.conn = c
	k.mu.Unlock()
	return c, err
}

func (k *keepConn) close() {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.conn != nil {
		_ = k.conn.Close()
	}
}

// watch pings through ping every watchEvery until done is closed; a ping that does not
// return within watchTimeout makes it call drop once and stop.
func watch(done <-chan struct{}, ping func() error, drop func()) {
	every, timeout := watchEvery, watchTimeout // read once: tests change them
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
		}
		answered := make(chan struct{})
		go func() {
			_ = ping() // an error is an answer; only silence counts
			close(answered)
		}()
		select {
		case <-done:
			return
		case <-answered:
		case <-time.After(timeout):
			drop()
			return
		}
	}
}
