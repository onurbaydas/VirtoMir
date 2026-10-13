package live

import "time"

// Recorder keeps metric history (implemented by *history.Store).
type Recorder interface {
	Add(name string, t time.Time, v float64)
}

// SetRecorder makes the hub feed every connected sample into r. Call before Run.
func (h *Hub) SetRecorder(r Recorder) { h.rec = r }

// Series names. Machines are keyed by UUID so history survives a rename.
const (
	SeriesHostCPU       = "host.cpu"
	SeriesHostMem       = "host.mem"
	SeriesHostDiskRead  = "host.disk.read"
	SeriesHostDiskWrite = "host.disk.write"
	SeriesHostNetRx     = "host.net.rx"
	SeriesHostNetTx     = "host.net.tx"
)

// VMSeries returns the name of a machine's series, e.g. VMSeries(uuid, "cpu").
// Metrics: cpu, mem (guest-reported %), disk.read, disk.write, net.rx, net.tx.
func VMSeries(uuid, metric string) string { return "vm." + uuid + "." + metric }

func (h *Hub) record(s *Snapshot, now time.Time) {
	if h.rec == nil || !s.Connected {
		return
	}
	r := h.rec
	r.Add(SeriesHostCPU, now, s.Host.CPUPercent)
	if s.Host.MemTotalKiB > 0 {
		r.Add(SeriesHostMem, now, float64(s.Host.MemUsedKiB)/float64(s.Host.MemTotalKiB)*100)
	}
	if io := s.Host.IO; io != nil {
		r.Add(SeriesHostDiskRead, now, io.DiskReadBps)
		r.Add(SeriesHostDiskWrite, now, io.DiskWriteBps)
		r.Add(SeriesHostNetRx, now, io.NetRxBps)
		r.Add(SeriesHostNetTx, now, io.NetTxBps)
	}
	for _, d := range s.Domains {
		if d.State != "running" && d.State != "blocked" && d.State != "shutting-down" {
			continue // stopped machines leave gaps, not zeros
		}
		r.Add(VMSeries(d.UUID, "cpu"), now, d.CPUPercent)
		if d.GuestMemTotalKiB > 0 {
			r.Add(VMSeries(d.UUID, "mem"), now, float64(d.GuestMemUsedKiB)/float64(d.GuestMemTotalKiB)*100)
		}
		if d.Disks != nil {
			var rd, wr float64
			for _, x := range d.Disks {
				rd, wr = rd+x.ReadBps, wr+x.WriteBps
			}
			r.Add(VMSeries(d.UUID, "disk.read"), now, rd)
			r.Add(VMSeries(d.UUID, "disk.write"), now, wr)
		}
		if d.Nets != nil {
			var rx, tx float64
			for _, x := range d.Nets {
				rx, tx = rx+x.RxBps, tx+x.TxBps
			}
			r.Add(VMSeries(d.UUID, "net.rx"), now, rx)
			r.Add(VMSeries(d.UUID, "net.tx"), now, tx)
		}
	}
}
