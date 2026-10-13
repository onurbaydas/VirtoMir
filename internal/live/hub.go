// Package live samples the host and its domains on a fixed interval (and immediately on
// libvirt lifecycle events) and fans the resulting snapshots out to subscribers such as
// the browser's event stream. It keeps a short backlog so new subscribers can draw
// full charts right away.
package live

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/onurbaydas/virtomir/internal/virt"
)

// Source is what the hub samples (implemented by *virt.Client).
type Source interface {
	ListDomains() ([]virt.Domain, error)
	HostStats() (virt.HostStats, error)
	DomainStats() ([]virt.DomainStats, error)
	LifecycleEvents(ctx context.Context) (<-chan string, error)
}

type HostLive struct {
	CPUPercent  float64 `json:"cpuPercent"`
	MemTotalKiB uint64  `json:"memTotalKiB"`
	MemUsedKiB  uint64  `json:"memUsedKiB"`
	// Physical disk and network throughput; absent off Linux and on the first sample.
	IO *HostIO `json:"io,omitempty"`
}

type DomainLive struct {
	virt.Domain
	// Share of the domain's own vCPUs in use: 100 = every vCPU fully busy.
	CPUPercent float64 `json:"cpuPercent"`
	// Per vCPU, 100 = that vCPU fully busy. Running domains only, from the second sample on.
	VCPUPercent []float64  `json:"vcpuPercent,omitempty"`
	Disks       []DiskLive `json:"disks,omitempty"`
	Nets        []NetLive  `json:"nets,omitempty"`
	// What the guest itself reports via the balloon driver; 0 when it does not.
	GuestMemUsedKiB  uint64 `json:"guestMemUsedKiB,omitempty"`
	GuestMemTotalKiB uint64 `json:"guestMemTotalKiB,omitempty"`
	// Host memory actually used by the QEMU process.
	HostRSSKiB uint64 `json:"hostRssKiB,omitempty"`
}

type Snapshot struct {
	Time      time.Time    `json:"time"`
	Connected bool         `json:"connected"`
	Error     string       `json:"error,omitempty"`
	Host      HostLive     `json:"host"`
	Domains   []DomainLive `json:"domains"`
}

type cpuSample struct {
	cpuNs uint64
	at    time.Time
}

// Backlog is how many past snapshots a new subscriber receives (one minute at 1 s).
const Backlog = 60

type Hub struct {
	src      Source
	interval time.Duration
	log      *slog.Logger
	refresh  chan struct{}

	// Sampling state, only touched by the Run goroutine.
	prevHost   virt.HostStats
	prevHostOK bool
	prevDom    map[string]cpuSample
	prevStats  map[string]statsSample
	hostIO     func() (hostCounters, error) // nil: not available on this OS
	prevIO     hostCounters
	prevIOAt   time.Time
	prevIOOK   bool

	mu      sync.Mutex
	backlog [][]byte // oldest first, at most Backlog entries
	subs    map[chan []byte]struct{}
	closed  bool
}

func NewHub(src Source, interval time.Duration, log *slog.Logger) *Hub {
	return &Hub{
		src:       src,
		interval:  interval,
		log:       log,
		refresh:   make(chan struct{}, 1),
		prevDom:   map[string]cpuSample{},
		prevStats: map[string]statsSample{},
		hostIO:    defaultHostIO(),
		subs:      map[chan []byte]struct{}{},
	}
}

// Run samples until ctx is cancelled, then closes all subscriber channels.
func (h *Hub) Run(ctx context.Context) {
	defer h.closeAll()
	go h.watchEvents(ctx)

	t := time.NewTicker(h.interval)
	defer t.Stop()
	h.sample(time.Now())
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			h.sample(now)
		case <-h.refresh:
			h.sample(time.Now())
		}
	}
}

// Refresh requests an out-of-band sample (coalesced if one is already pending).
func (h *Hub) Refresh() {
	select {
	case h.refresh <- struct{}{}:
	default:
	}
}

// Subscribe returns the recent snapshots (oldest first), a channel of new JSON snapshots and
// a function to unsubscribe. Slow subscribers only ever get the newest snapshot.
func (h *Hub) Subscribe() ([][]byte, <-chan []byte, func()) {
	ch := make(chan []byte, 1)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		close(ch)
		return nil, ch, func() {}
	}
	backlog := append([][]byte(nil), h.backlog...)
	h.subs[ch] = struct{}{}
	return backlog, ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.subs[ch]; ok {
			delete(h.subs, ch)
			close(ch)
		}
	}
}

func (h *Hub) watchEvents(ctx context.Context) {
	for ctx.Err() == nil {
		events, err := h.src.LifecycleEvents(ctx)
		if err != nil {
			h.log.Debug("lifecycle events unavailable, retrying", "err", err)
		} else {
			for range events {
				h.Refresh()
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
}

func (h *Hub) sample(now time.Time) {
	snap := Snapshot{Time: now.UTC(), Connected: true, Domains: []DomainLive{}}

	hs, err := h.src.HostStats()
	if err == nil {
		var doms []virt.Domain
		if doms, err = h.src.ListDomains(); err == nil {
			snap.Host = h.hostLive(hs)
			snap.Host.IO = h.sampleHostIO(now)
			snap.Domains = h.domainsLive(doms, now)
			// Extras: a failing bulk-stats call must not take the basic view down.
			if stats, serr := h.src.DomainStats(); serr == nil {
				h.applyStats(snap.Domains, stats, now)
			} else {
				h.log.Debug("domain stats unavailable", "err", serr)
				clear(h.prevStats)
			}
		}
	}
	if err != nil {
		snap.Connected, snap.Error = false, err.Error()
		h.prevHostOK = false
		clear(h.prevDom)
		clear(h.prevStats)
	}

	b, err := json.Marshal(snap)
	if err != nil {
		h.log.Error("encode snapshot", "err", err)
		return
	}
	h.publish(b)
}

func (h *Hub) hostLive(s virt.HostStats) HostLive {
	out := HostLive{MemTotalKiB: s.MemTotalKiB, MemUsedKiB: s.MemUsedKiB}
	if h.prevHostOK && s.CPUTotalNs > h.prevHost.CPUTotalNs && s.CPUBusyNs >= h.prevHost.CPUBusyNs {
		out.CPUPercent = percent(s.CPUBusyNs-h.prevHost.CPUBusyNs, s.CPUTotalNs-h.prevHost.CPUTotalNs)
	}
	h.prevHost, h.prevHostOK = s, true
	return out
}

func (h *Hub) domainsLive(doms []virt.Domain, now time.Time) []DomainLive {
	seen := make(map[string]bool, len(doms))
	out := make([]DomainLive, 0, len(doms))
	for _, d := range doms {
		seen[d.UUID] = true
		dl := DomainLive{Domain: d}
		if d.State == virt.StateRunning || d.State == virt.StateBlocked || d.State == virt.StateShuttingDown {
			if prev, ok := h.prevDom[d.UUID]; ok && d.CPUTimeNs >= prev.cpuNs && d.VCPUs > 0 {
				wall := now.Sub(prev.at)
				if wall > 0 {
					dl.CPUPercent = percent(d.CPUTimeNs-prev.cpuNs, uint64(wall.Nanoseconds())*uint64(d.VCPUs))
				}
			}
			h.prevDom[d.UUID] = cpuSample{cpuNs: d.CPUTimeNs, at: now}
		} else {
			delete(h.prevDom, d.UUID)
		}
		out = append(out, dl)
	}
	for id := range h.prevDom {
		if !seen[id] {
			delete(h.prevDom, id)
		}
	}
	return out
}

func percent(part, whole uint64) float64 {
	if whole == 0 {
		return 0
	}
	p := float64(part) / float64(whole) * 100
	return min(max(p, 0), 100)
}

func (h *Hub) publish(b []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.backlog) == Backlog {
		h.backlog = append(h.backlog[:0], h.backlog[1:]...)
	}
	h.backlog = append(h.backlog, b)
	for ch := range h.subs {
		select { // drop the stale snapshot if the subscriber hasn't read it yet
		case <-ch:
		default:
		}
		ch <- b
	}
}

func (h *Hub) closeAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for ch := range h.subs {
		delete(h.subs, ch)
		close(ch)
	}
}
