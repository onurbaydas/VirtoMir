package virt

import (
	"sync/atomic"
	"testing"
	"time"
)

func fastWatch(t *testing.T) {
	t.Helper()
	every, timeout := watchEvery, watchTimeout
	watchEvery, watchTimeout = 5*time.Millisecond, 30*time.Millisecond
	t.Cleanup(func() { watchEvery, watchTimeout = every, timeout })
}

func TestWatchDropsAHungConnection(t *testing.T) {
	fastWatch(t)
	hang := make(chan struct{})
	defer close(hang)
	dropped := make(chan struct{})
	go watch(make(chan struct{}), func() error { <-hang; return nil }, func() { close(dropped) })
	select {
	case <-dropped:
	case <-time.After(2 * time.Second):
		t.Fatal("a ping that never returns did not drop the connection")
	}
}

func TestWatchKeepsAnAnsweringConnection(t *testing.T) {
	fastWatch(t)
	var pings, drops atomic.Int32
	done := make(chan struct{})
	go watch(done, func() error { pings.Add(1); return nil }, func() { drops.Add(1) })
	time.Sleep(150 * time.Millisecond)
	close(done)
	if pings.Load() < 3 || drops.Load() != 0 {
		t.Fatalf("pings=%d drops=%d", pings.Load(), drops.Load())
	}
}
