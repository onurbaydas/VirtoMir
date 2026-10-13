package live

import (
	"bufio"
	"errors"
	"io/fs"
	"strconv"
	"strings"
	"time"
)

// HostIO is host disk and network throughput over the last sample interval, counting only
// physical devices: VM taps, bridges, loop and device-mapper devices would count traffic
// twice.
type HostIO struct {
	DiskReadBps  float64 `json:"diskReadBps"`
	DiskWriteBps float64 `json:"diskWriteBps"`
	NetRxBps     float64 `json:"netRxBps"`
	NetTxBps     float64 `json:"netTxBps"`
}

type hostCounters struct {
	diskRd, diskWr, netRx, netTx uint64 // bytes
}

// readHostCounters reads /proc and /sys from fsys (rooted at "/").
func readHostCounters(fsys fs.FS) (hostCounters, error) {
	var c hostCounters
	disk, err := fs.ReadFile(fsys, "proc/diskstats")
	if err != nil {
		return c, err
	}
	net, err := fs.ReadFile(fsys, "proc/net/dev")
	if err != nil {
		return c, err
	}
	hasDevice := func(dir, name string) bool {
		_, err := fs.Stat(fsys, dir+"/"+name+"/device")
		return err == nil
	}
	c.diskRd, c.diskWr = parseDiskstats(string(disk), func(n string) bool { return hasDevice("sys/block", n) })
	c.netRx, c.netTx = parseNetDev(string(net), func(n string) bool { return hasDevice("sys/class/net", n) })
	return c, nil
}

// parseDiskstats sums bytes read/written (sectors × 512) of whole physical disks.
func parseDiskstats(s string, physical func(string) bool) (rd, wr uint64) {
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 10 || !physical(f[2]) {
			continue
		}
		r, err1 := strconv.ParseUint(f[5], 10, 64)
		w, err2 := strconv.ParseUint(f[9], 10, 64)
		if err1 == nil && err2 == nil {
			rd += r * 512
			wr += w * 512
		}
	}
	return rd, wr
}

// parseNetDev sums received/transmitted bytes of physical interfaces.
func parseNetDev(s string, physical func(string) bool) (rx, tx uint64) {
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue // header lines
		}
		name = strings.TrimSpace(name)
		f := strings.Fields(rest)
		if len(f) < 9 || !physical(name) {
			continue
		}
		r, err1 := strconv.ParseUint(f[0], 10, 64)
		t, err2 := strconv.ParseUint(f[8], 10, 64)
		if err1 == nil && err2 == nil {
			rx += r
			tx += t
		}
	}
	return rx, tx
}

// sampleHostIO returns host rates once two samples exist; nil while unavailable.
func (h *Hub) sampleHostIO(now time.Time) *HostIO {
	if h.hostIO == nil {
		return nil
	}
	cur, err := h.hostIO()
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			h.log.Debug("host io unavailable", "err", err)
		}
		h.prevIOOK = false
		return nil
	}
	prev, prevAt, ok := h.prevIO, h.prevIOAt, h.prevIOOK
	h.prevIO, h.prevIOAt, h.prevIOOK = cur, now, true
	secs := now.Sub(prevAt).Seconds()
	if !ok || secs <= 0 {
		return nil
	}
	out := &HostIO{}
	if d, ok := delta(prev.diskRd, cur.diskRd); ok {
		out.DiskReadBps = float64(d) / secs
	}
	if d, ok := delta(prev.diskWr, cur.diskWr); ok {
		out.DiskWriteBps = float64(d) / secs
	}
	if d, ok := delta(prev.netRx, cur.netRx); ok {
		out.NetRxBps = float64(d) / secs
	}
	if d, ok := delta(prev.netTx, cur.netTx); ok {
		out.NetTxBps = float64(d) / secs
	}
	return out
}
