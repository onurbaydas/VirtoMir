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
	host     virt.HostStats
	doms     []virt.Domain
	stats    []virt.DomainStats
	statsErr error
	err      error
}

func (f *fakeSource) ListDomains() ([]virt.Domain, error) { return f.doms, f.err }
func (f *fakeSource) HostStats() (virt.HostStats, error)  { return f.host, f.err }
func (f *fakeSource) DomainStats() ([]virt.DomainStats, error) {
	return f.stats, f.statsErr
}
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

func TestIORates(t *testing.T) {
	src := &fakeSource{
		doms: []virt.Domain{{UUID: "a", State: virt.StateRunning, VCPUs: 2}},
		stats: []virt.DomainStats{{
			UUID:       "a",
			VCPUTimeNs: []uint64{0, 0},
			Balloon:    virt.BalloonStats{AvailableKiB: 2000, UsableKiB: 500, RSSKiB: 1800},
			Disks:      []virt.DiskCounters{{Name: "vda"}},
			Nets:       []virt.NetCounters{{Name: "vnet0"}},
		}},
	}
	h := newTestHub(src)
	t0 := time.Unix(1000, 0)
	h.sample(t0)
	s := latest(t, h)
	if d := s.Domains[0]; d.Disks != nil || d.VCPUPercent != nil {
		t.Fatalf("first sample must have no rates: %+v", d)
	}
	if d := s.Domains[0]; d.GuestMemUsedKiB != 1500 || d.GuestMemTotalKiB != 2000 || d.HostRSSKiB != 1800 {
		t.Errorf("memory = %+v", d)
	}

	// 2 s later.
	st := &src.stats[0]
	st.VCPUTimeNs = []uint64{uint64(2 * time.Second), uint64(500 * time.Millisecond)}
	// Fresh slices, like the real client returns on every call.
	st.Disks = []virt.DiskCounters{{Name: "vda", RdBytes: 2 << 20, WrBytes: 4 << 20, RdReqs: 10, WrReqs: 20}}
	st.Nets = []virt.NetCounters{{Name: "vnet0", RxBytes: 3000, TxBytes: 1000, RxPkts: 30, TxPkts: 10, RxDrop: 4}}
	h.sample(t0.Add(2 * time.Second))
	d := latest(t, h).Domains[0]

	if len(d.VCPUPercent) != 2 || !near(d.VCPUPercent[0], 100) || !near(d.VCPUPercent[1], 25) {
		t.Errorf("vcpu = %v, want [100 25]", d.VCPUPercent)
	}
	if len(d.Disks) != 1 || !near(d.Disks[0].ReadBps, 1<<20) || !near(d.Disks[0].WriteBps, 2<<20) || !near(d.Disks[0].WriteIops, 10) {
		t.Errorf("disks = %+v", d.Disks)
	}
	if d.Disks[0].ReadLatencyMs != 0 {
		t.Errorf("no latency counters -> no latency, got %v", d.Disks[0].ReadLatencyMs)
	}
	if len(d.Nets) != 1 || !near(d.Nets[0].RxBps, 1500) || !near(d.Nets[0].TxPps, 5) || !near(d.Nets[0].ProblemsPs, 2) {
		t.Errorf("nets = %+v", d.Nets)
	}
}

func TestIOCounterResetGivesNoRate(t *testing.T) {
	src := &fakeSource{
		doms:  []virt.Domain{{UUID: "a", State: virt.StateRunning, VCPUs: 1}},
		stats: []virt.DomainStats{{UUID: "a", Disks: []virt.DiskCounters{{Name: "vda", RdBytes: 1000}}}},
	}
	h := newTestHub(src)
	t0 := time.Unix(1000, 0)
	h.sample(t0)
	src.stats[0].Disks = []virt.DiskCounters{{Name: "vda", RdBytes: 10}} // counter went backwards (device re-attached)
	h.sample(t0.Add(time.Second))
	if d := latest(t, h).Domains[0]; len(d.Disks) != 0 {
		t.Errorf("reset counter must not produce a rate: %+v", d.Disks)
	}
}

func TestLatency(t *testing.T) {
	got := diskRates(
		[]virt.DiskCounters{mkDisk(0, 0, 0, 0)},
		[]virt.DiskCounters{mkDisk(4, 2, 8_000_000, 0)}, 1)
	if len(got) != 1 || !near(got[0].ReadLatencyMs, 2) || got[0].WriteLatencyMs != 0 {
		t.Errorf("latency = %+v, want read 2 ms and no write latency", got)
	}
}

func TestStatsFailureKeepsBasics(t *testing.T) {
	src := &fakeSource{doms: []virt.Domain{{UUID: "a", State: virt.StateRunning, VCPUs: 1}}, statsErr: errors.New("unsupported")}
	h := newTestHub(src)
	h.sample(time.Now())
	if s := latest(t, h); !s.Connected || len(s.Domains) != 1 {
		t.Errorf("bulk stats failure must not break the snapshot: %+v", s)
	}
}

// mkDisk builds counters with latency fields set (as libvirt reports them for real disks).
func mkDisk(rdReqs, wrReqs, rdTimes, wrTimes uint64) virt.DiskCounters {
	return virt.DiskCounters{Name: "vda", RdReqs: rdReqs, WrReqs: wrReqs, RdTimesNs: rdTimes, WrTimesNs: wrTimes, Timed: true}
}
