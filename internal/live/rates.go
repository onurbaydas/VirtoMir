package live

import (
	"time"

	"github.com/onurbaydas/virtomir/internal/virt"
)

// DiskLive is one disk's I/O over the last sample interval.
type DiskLive struct {
	Name      string  `json:"name"`
	ReadBps   float64 `json:"readBps"`
	WriteBps  float64 `json:"writeBps"`
	ReadIops  float64 `json:"readIops"`
	WriteIops float64 `json:"writeIops"`
	// Average time per request in ms; omitted when there were no requests to measure.
	ReadLatencyMs  float64 `json:"readLatencyMs,omitempty"`
	WriteLatencyMs float64 `json:"writeLatencyMs,omitempty"`
}

// NetLive is one network adapter's traffic over the last sample interval, seen from the guest.
type NetLive struct {
	Name  string  `json:"name"` // host-side tap, e.g. vnet2
	RxBps float64 `json:"rxBps"`
	TxBps float64 `json:"txBps"`
	RxPps float64 `json:"rxPps"`
	TxPps float64 `json:"txPps"`
	// Dropped + errored packets per second, both directions.
	ProblemsPs float64 `json:"problemsPs,omitempty"`
}

type statsSample struct {
	s  virt.DomainStats
	at time.Time
}

// applyStats fills the per-vCPU, disk, network and memory fields of running domains from
// bulk stats. Rates need two samples; counters that went backwards (restart, hot-unplug)
// produce no rate for that interval.
func (h *Hub) applyStats(doms []DomainLive, stats []virt.DomainStats, now time.Time) {
	byUUID := make(map[string]virt.DomainStats, len(stats))
	for _, s := range stats {
		byUUID[s.UUID] = s
	}
	for i := range doms {
		d := &doms[i]
		cur, ok := byUUID[d.UUID]
		if !ok {
			continue
		}
		if b := cur.Balloon; b.AvailableKiB > 0 && b.UsableKiB <= b.AvailableKiB {
			d.GuestMemTotalKiB, d.GuestMemUsedKiB = b.AvailableKiB, b.AvailableKiB-b.UsableKiB
		}
		d.HostRSSKiB = cur.Balloon.RSSKiB

		prev, ok := h.prevStats[d.UUID]
		h.prevStats[d.UUID] = statsSample{s: cur, at: now}
		if !ok {
			continue
		}
		secs := now.Sub(prev.at).Seconds()
		if secs <= 0 {
			continue
		}
		d.VCPUPercent = vcpuPercent(prev.s.VCPUTimeNs, cur.VCPUTimeNs, now.Sub(prev.at))
		d.Disks = diskRates(prev.s.Disks, cur.Disks, secs)
		d.Nets = netRates(prev.s.Nets, cur.Nets, secs)
	}
	for id := range h.prevStats {
		if _, ok := byUUID[id]; !ok {
			delete(h.prevStats, id)
		}
	}
}

func vcpuPercent(prev, cur []uint64, wall time.Duration) []float64 {
	if len(cur) == 0 || wall <= 0 {
		return nil
	}
	out := make([]float64, len(cur))
	for i, c := range cur {
		if i < len(prev) {
			if d, ok := delta(prev[i], c); ok {
				out[i] = percent(d, uint64(wall.Nanoseconds()))
			}
		}
	}
	return out
}

func diskRates(prev, cur []virt.DiskCounters, secs float64) []DiskLive {
	var out []DiskLive
	for _, c := range cur {
		p, ok := findDisk(prev, c.Name)
		if !ok {
			continue
		}
		dl := DiskLive{Name: c.Name}
		rb, ok1 := delta(p.RdBytes, c.RdBytes)
		wb, ok2 := delta(p.WrBytes, c.WrBytes)
		rr, ok3 := delta(p.RdReqs, c.RdReqs)
		wr, ok4 := delta(p.WrReqs, c.WrReqs)
		if !(ok1 && ok2 && ok3 && ok4) {
			continue
		}
		dl.ReadBps, dl.WriteBps = float64(rb)/secs, float64(wb)/secs
		dl.ReadIops, dl.WriteIops = float64(rr)/secs, float64(wr)/secs
		if c.Timed && p.Timed {
			if rt, ok := delta(p.RdTimesNs, c.RdTimesNs); ok && rr > 0 {
				dl.ReadLatencyMs = float64(rt) / float64(rr) / 1e6
			}
			if wt, ok := delta(p.WrTimesNs, c.WrTimesNs); ok && wr > 0 {
				dl.WriteLatencyMs = float64(wt) / float64(wr) / 1e6
			}
		}
		out = append(out, dl)
	}
	return out
}

func netRates(prev, cur []virt.NetCounters, secs float64) []NetLive {
	var out []NetLive
	for _, c := range cur {
		p, ok := findNet(prev, c.Name)
		if !ok {
			continue
		}
		rb, ok1 := delta(p.RxBytes, c.RxBytes)
		tb, ok2 := delta(p.TxBytes, c.TxBytes)
		rp, ok3 := delta(p.RxPkts, c.RxPkts)
		tp, ok4 := delta(p.TxPkts, c.TxPkts)
		if !(ok1 && ok2 && ok3 && ok4) {
			continue
		}
		var problems uint64
		for _, pair := range [][2]uint64{{p.RxErrs, c.RxErrs}, {p.TxErrs, c.TxErrs}, {p.RxDrop, c.RxDrop}, {p.TxDrop, c.TxDrop}} {
			if d, ok := delta(pair[0], pair[1]); ok {
				problems += d
			}
		}
		out = append(out, NetLive{
			Name:  c.Name,
			RxBps: float64(rb) / secs, TxBps: float64(tb) / secs,
			RxPps: float64(rp) / secs, TxPps: float64(tp) / secs,
			ProblemsPs: float64(problems) / secs,
		})
	}
	return out
}

func findDisk(ds []virt.DiskCounters, name string) (virt.DiskCounters, bool) {
	for _, d := range ds {
		if d.Name == name {
			return d, true
		}
	}
	return virt.DiskCounters{}, false
}

func findNet(ns []virt.NetCounters, name string) (virt.NetCounters, bool) {
	for _, n := range ns {
		if n.Name == name {
			return n, true
		}
	}
	return virt.NetCounters{}, false
}

// delta is cur-prev for a monotonic counter; false when the counter was reset.
func delta(prev, cur uint64) (uint64, bool) {
	if cur < prev {
		return 0, false
	}
	return cur - prev, true
}
