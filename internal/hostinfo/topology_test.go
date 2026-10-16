package hostinfo

import (
	"context"
	"slices"
	"testing"
	"time"
)

func TestTopology(t *testing.T) {
	root := tree(t, map[string]string{
		"sys/devices/system/node/node0/cpulist": "0,2",
		"sys/devices/system/node/node0/meminfo": "Node 0 MemTotal:  8000000 kB\nNode 0 MemFree:   3000000 kB",
		"sys/devices/system/node/node1/cpulist": "1,3",
		"sys/devices/system/node/node1/meminfo": "Node 1 MemTotal:  8000000 kB\nNode 1 MemFree:   7000000 kB",

		"sys/devices/system/cpu/cpu0/topology/physical_package_id":  "0",
		"sys/devices/system/cpu/cpu0/topology/core_id":              "0",
		"sys/devices/system/cpu/cpu0/topology/thread_siblings_list": "0,2",
		"sys/devices/system/cpu/cpu1/topology/physical_package_id":  "1",
		"sys/devices/system/cpu/cpu1/topology/thread_siblings_list": "1,3",
		"sys/devices/system/cpu/cpu2/topology/physical_package_id":  "0",
		"sys/devices/system/cpu/cpu2/topology/thread_siblings_list": "0,2",
		"sys/devices/system/cpu/cpu3/online":                        "0",
		"proc/stat":                                                 "cpu  1 1 1 1 1 1 1 1 1 1\ncpu0 100 0 0 100 0 0 0 0 0 0\ncpu1 0 0 0 200 0 0 0 0 0 0",
	})
	r := &Reader{Root: root}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	topo := r.Topology(ctx)

	if len(topo.Nodes) != 2 || !slices.Equal(topo.Nodes[1].CPUs, []int{1, 3}) || topo.Nodes[0].MemFreeKiB != 3000000 {
		t.Fatalf("nodes = %+v", topo.Nodes)
	}
	if len(topo.CPUs) != 4 {
		t.Fatalf("cpus = %+v", topo.CPUs)
	}
	c1, c3 := topo.CPUs[1], topo.CPUs[3]
	if c1.Node != 1 || c1.Socket != 1 || !slices.Equal(c1.Siblings, []int{1, 3}) || !c1.Online {
		t.Errorf("cpu1 = %+v", c1)
	}
	if c3.Online || !slices.Equal(c3.Siblings, []int{3}) {
		t.Errorf("cpu3 = %+v", c3)
	}
}

func TestParseCPUListAndLoad(t *testing.T) {
	if got := parseCPUList("0-2,8,10-11"); !slices.Equal(got, []int{0, 1, 2, 8, 10, 11}) {
		t.Errorf("cpulist = %v", got)
	}
	if got := parseCPUList(""); got != nil {
		t.Errorf("empty = %v", got)
	}
	l := loadBetween(map[int]cpuTimes{0: {busy: 100, total: 200}}, map[int]cpuTimes{0: {busy: 175, total: 300}, 1: {busy: 5, total: 10}})
	if l[0] != 75 || len(l) != 1 {
		t.Errorf("load = %v", l)
	}
}
