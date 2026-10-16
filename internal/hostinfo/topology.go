package hostinfo

import (
	"context"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---- Topology: NUMA nodes, sockets, cores, threads and their load (P5) ----------------------

// Topology is how the host's CPUs and memory are laid out: the picture to pin by.
type Topology struct {
	Nodes []NUMANode `json:"nodes"`
	CPUs  []CPU      `json:"cpus"`
}

// NUMANode is one memory node with its CPUs. A host without NUMA reports a single node 0.
type NUMANode struct {
	ID          int   `json:"id"`
	CPUs        []int `json:"cpus"`
	MemTotalKiB int64 `json:"memTotalKiB"`
	MemFreeKiB  int64 `json:"memFreeKiB"`
}

// CPU is one logical CPU (hardware thread).
type CPU struct {
	ID     int  `json:"id"`
	Node   int  `json:"node"`
	Socket int  `json:"socket"`
	Core   int  `json:"core"`
	Online bool `json:"online"`
	// Siblings are the hardware threads sharing this CPU's core (itself included).
	Siblings []int `json:"siblings"`
	// LoadPercent is the busy share since the previous look (or over a short sample).
	LoadPercent *float64 `json:"loadPercent,omitempty"`
}

// Topology reads /sys/devices/system/{node,cpu} and samples /proc/stat for the load.
func (r *Reader) Topology(ctx context.Context) Topology {
	var t Topology
	nodeOf := map[int]int{}
	nodes, _ := filepath.Glob(r.path("sys/devices/system/node/node[0-9]*"))
	for _, dir := range nodes {
		id, err := strconv.Atoi(strings.TrimPrefix(filepath.Base(dir), "node"))
		if err != nil {
			continue
		}
		rel := "sys/devices/system/node/" + filepath.Base(dir)
		n := NUMANode{ID: id, CPUs: parseCPUList(r.read(rel + "/cpulist"))}
		n.MemTotalKiB, n.MemFreeKiB = nodeMem(r.read(rel + "/meminfo"))
		for _, c := range n.CPUs {
			nodeOf[c] = id
		}
		t.Nodes = append(t.Nodes, n)
	}
	sort.Slice(t.Nodes, func(i, j int) bool { return t.Nodes[i].ID < t.Nodes[j].ID })

	load := r.cpuLoad(ctx)
	cpus, _ := filepath.Glob(r.path("sys/devices/system/cpu/cpu[0-9]*"))
	for _, dir := range cpus {
		id, err := strconv.Atoi(strings.TrimPrefix(filepath.Base(dir), "cpu"))
		if err != nil {
			continue
		}
		rel := "sys/devices/system/cpu/" + filepath.Base(dir)
		c := CPU{ID: id, Node: nodeOf[id], Online: r.read(rel+"/online") != "0"}
		if v, ok := r.readInt(rel + "/topology/physical_package_id"); ok {
			c.Socket = int(v)
		}
		if v, ok := r.readInt(rel + "/topology/core_id"); ok {
			c.Core = int(v)
		}
		c.Siblings = parseCPUList(r.read(rel + "/topology/thread_siblings_list"))
		if len(c.Siblings) == 0 {
			c.Siblings = []int{id}
		}
		if v, ok := load[id]; ok {
			c.LoadPercent = f(v)
		}
		t.CPUs = append(t.CPUs, c)
	}
	sort.Slice(t.CPUs, func(i, j int) bool { return t.CPUs[i].ID < t.CPUs[j].ID })

	if len(t.Nodes) == 0 { // no NUMA in this kernel: one node holds everything
		n := NUMANode{ID: 0}
		for _, c := range t.CPUs {
			n.CPUs = append(n.CPUs, c.ID)
		}
		n.MemTotalKiB, n.MemFreeKiB = nodeMem(r.read("proc/meminfo"))
		t.Nodes = []NUMANode{n}
	}
	return t
}

// parseCPUList reads the kernel's list format: "0-3,8-11" or "5".
func parseCPUList(s string) []int {
	var out []int
	for _, part := range strings.Split(strings.TrimSpace(s), ",") {
		if part == "" {
			continue
		}
		lo, hi, isRange := strings.Cut(part, "-")
		a, err := strconv.Atoi(lo)
		if err != nil {
			continue
		}
		b := a
		if isRange {
			if b, err = strconv.Atoi(hi); err != nil || b < a {
				continue
			}
		}
		for i := a; i <= b; i++ {
			out = append(out, i)
		}
	}
	return out
}

// nodeMem reads MemTotal and MemFree (kB) from a node's meminfo ("Node 0 MemTotal: … kB")
// or from /proc/meminfo ("MemTotal: … kB").
func nodeMem(s string) (total, free int64) {
	for _, line := range strings.Split(s, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields := strings.Fields(v)
		if len(fields) == 0 {
			continue
		}
		n, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			continue
		}
		switch {
		case strings.HasSuffix(k, "MemTotal"):
			total = n
		case strings.HasSuffix(k, "MemFree"):
			free = n
		}
	}
	return total, free
}

// cpuTimes is one CPU's (busy, total) jiffies from /proc/stat.
type cpuTimes struct{ busy, total uint64 }

func (r *Reader) statSample() map[int]cpuTimes {
	out := map[int]cpuTimes{}
	for _, line := range strings.Split(r.read("proc/stat"), "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || !strings.HasPrefix(f[0], "cpu") || f[0] == "cpu" {
			continue
		}
		id, err := strconv.Atoi(strings.TrimPrefix(f[0], "cpu"))
		if err != nil {
			continue
		}
		var t cpuTimes
		for i, v := range f[1:] {
			n, _ := strconv.ParseUint(v, 10, 64)
			if i >= 8 { // guest and guest_nice are already counted in user and nice
				break
			}
			t.total += n
			if i != 3 && i != 4 { // idle, iowait
				t.busy += n
			}
		}
		out[id] = t
	}
	return out
}

// cpuLoad returns each CPU's busy percentage since the previous call; on the first call (or
// after a long pause) it samples for a quarter second.
func (r *Reader) cpuLoad(ctx context.Context) map[int]float64 {
	r.mu.Lock()
	prev, at := r.prevStat, r.prevStatAt
	r.mu.Unlock()
	if prev == nil || time.Since(at) > time.Minute || time.Since(at) < 100*time.Millisecond {
		prev = r.statSample()
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(250 * time.Millisecond):
		}
	}
	cur := r.statSample()
	r.mu.Lock()
	r.prevStat, r.prevStatAt = cur, time.Now()
	r.mu.Unlock()
	return loadBetween(prev, cur)
}

func loadBetween(prev, cur map[int]cpuTimes) map[int]float64 {
	out := map[int]float64{}
	for id, c := range cur {
		p, ok := prev[id]
		if !ok || c.total <= p.total {
			continue
		}
		out[id] = float64(c.busy-p.busy) * 100 / float64(c.total-p.total)
	}
	return out
}
