package live

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"testing"
	"time"

	"github.com/onurbaydas/virtomir/internal/virt"
)

type fakeSource struct {
	host virt.HostStats
	doms []virt.Domain
	err  error
}

func (f *fakeSource) ListDomains() ([]virt.Domain, error) { return f.doms, f.err }
func (f *fakeSource) HostStats() (virt.HostStats, error)  { return f.host, f.err }
func (f *fakeSource) LifecycleEvents(ctx context.Context) (<-chan string, error) {
	return nil, errors.New("not supported")
}

func newTestHub(src Source) *Hub {
	return NewHub(src, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func latest(t *testing.T, h *Hub) Snapshot {
	t.Helper()
	backlog, _, cancel := h.Subscribe()
	defer cancel()
	if len(backlog) == 0 {
		t.Fatal("no snapshot published")
	}
	var s Snapshot
	if err := json.Unmarshal(backlog[len(backlog)-1], &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func near(a, b float64) bool { return math.Abs(a-b) < 0.01 }

func TestCPUPercent(t *testing.T) {
	src := &fakeSource{
		host: virt.HostStats{CPUBusyNs: 0, CPUTotalNs: 0, MemTotalKiB: 1000, MemUsedKiB: 250},
		doms: []virt.Domain{{UUID: "a", State: virt.StateRunning, VCPUs: 2, CPUTimeNs: 0}},
	}
	h := newTestHub(src)
	t0 := time.Unix(1000, 0)
	h.sample(t0)
	if s := latest(t, h); s.Domains[0].CPUPercent != 0 || s.Host.CPUPercent != 0 {
		t.Fatalf("first sample must have no rate: %+v", s)
	}

	// 1 s later: the domain used 1 s of CPU on 2 vCPUs -> 50 %; host 30 of 120 ns busy -> 25 %.
	src.doms[0].CPUTimeNs = uint64(time.Second)
	src.host.CPUBusyNs, src.host.CPUTotalNs = 30, 120
	h.sample(t0.Add(time.Second))
	s := latest(t, h)
	if !near(s.Domains[0].CPUPercent, 50) {
		t.Errorf("domain cpu = %v, want 50", s.Domains[0].CPUPercent)
	}
	if !near(s.Host.CPUPercent, 25) {
		t.Errorf("host cpu = %v, want 25", s.Host.CPUPercent)
	}
	if !s.Connected || s.Host.MemUsedKiB != 250 {
		t.Errorf("unexpected snapshot %+v", s)
	}
}

func TestStoppedDomainResetsRate(t *testing.T) {
	src := &fakeSource{doms: []virt.Domain{{UUID: "a", State: virt.StateRunning, VCPUs: 1, CPUTimeNs: 5}}}
	h := newTestHub(src)
	t0 := time.Unix(1000, 0)
	h.sample(t0)
	src.doms[0].State, src.doms[0].CPUTimeNs = virt.StateShutoff, 0
	h.sample(t0.Add(time.Second))
	if _, ok := h.prevDom["a"]; ok {
		t.Error("stopped domain should drop its previous sample")
	}
	// Restarted: CPU time starts from 0 again; the first sample must not report a rate.
	src.doms[0].State, src.doms[0].CPUTimeNs = virt.StateRunning, 100
	h.sample(t0.Add(2 * time.Second))
	if s := latest(t, h); s.Domains[0].CPUPercent != 0 {
		t.Errorf("cpu after restart = %v, want 0", s.Domains[0].CPUPercent)
	}
}

func TestDisconnectedSnapshot(t *testing.T) {
	h := newTestHub(&fakeSource{err: errors.New("socket closed")})
	h.sample(time.Now())
	s := latest(t, h)
	if s.Connected || s.Error != "socket closed" || s.Domains == nil {
		t.Errorf("unexpected snapshot %+v", s)
	}
}

func TestSlowSubscriberGetsNewest(t *testing.T) {
	h := newTestHub(&fakeSource{})
	_, ch, cancel := h.Subscribe()
	defer cancel()
	h.publish([]byte("1"))
	h.publish([]byte("2"))
	if got := string(<-ch); got != "2" {
		t.Errorf("got %q, want newest snapshot", got)
	}
}

func TestRunClosesSubscribers(t *testing.T) {
	h := newTestHub(&fakeSource{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.Run(ctx); close(done) }()
	_, ch, unsub := h.Subscribe()
	defer unsub()
	cancel()
	<-done
	for range ch { // must terminate
	}
}

func TestBacklog(t *testing.T) {
	h := newTestHub(&fakeSource{})
	for i := range Backlog + 5 {
		h.publish([]byte{byte(i)})
	}
	backlog, _, cancel := h.Subscribe()
	defer cancel()
	if len(backlog) != Backlog {
		t.Fatalf("backlog has %d entries, want %d", len(backlog), Backlog)
	}
	if backlog[0][0] != 5 || backlog[Backlog-1][0] != Backlog+4 {
		t.Errorf("backlog must keep the newest entries, oldest first: first=%d last=%d", backlog[0][0], backlog[Backlog-1][0])
	}
}
