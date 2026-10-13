package live

import (
	"testing"
	"testing/fstest"
	"time"
)

const diskstats = `   8       0 sda 1000 0 2048 10 500 0 4096 20 0 30 30 0 0 0 0 0 0
   8       1 sda1 900 0 1024 10 400 0 2048 20 0 30 30 0 0 0 0 0 0
   7       0 loop0 50 0 99999 1 0 0 0 0 0 1 1 0 0 0 0 0 0
 253       0 dm-0 800 0 2000 5 400 0 4000 10 0 15 15 0 0 0 0 0 0
 259       0 nvme0n1 10 0 8 1 10 0 16 1 0 1 1 0 0 0 0 0 0
`

const netdev = `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 5000      50    0    0    0     0          0         0     5000      50    0    0    0     0       0          0
  eth0: 1000000   900    0    0    0     0          0         0   200000     300    0    0    0     0       0          0
virbr0: 7777      10    0    0    0     0          0         0     8888      10    0    0    0     0       0          0
 vnet2: 3333       5    0    0    0     0          0         0     4444       5    0    0    0     0       0          0
`

func procFS(disk, net string) fstest.MapFS {
	return fstest.MapFS{
		"proc/diskstats":                {Data: []byte(disk)},
		"proc/net/dev":                  {Data: []byte(net)},
		"sys/block/sda/device":          {Data: nil},
		"sys/block/nvme0n1/device":      {Data: nil},
		"sys/block/loop0/queue":         {Data: nil}, // no device link: virtual
		"sys/block/dm-0/dm":             {Data: nil},
		"sys/class/net/eth0/device":     {Data: nil},
		"sys/class/net/virbr0/bridge":   {Data: nil},
		"sys/class/net/vnet2/tun_flags": {Data: nil},
	}
}

func TestReadHostCounters(t *testing.T) {
	c, err := readHostCounters(procFS(diskstats, netdev))
	if err != nil {
		t.Fatal(err)
	}
	// sda + nvme0n1 only: partitions, loop and dm devices are not counted.
	if c.diskRd != (2048+8)*512 || c.diskWr != (4096+16)*512 {
		t.Errorf("disk = %d / %d", c.diskRd, c.diskWr)
	}
	// eth0 only: lo, the bridge and the VM tap are virtual.
	if c.netRx != 1000000 || c.netTx != 200000 {
		t.Errorf("net = %d / %d", c.netRx, c.netTx)
	}
}

func TestHostIORates(t *testing.T) {
	cur := hostCounters{}
	h := newTestHub(&fakeSource{})
	h.hostIO = func() (hostCounters, error) { return cur, nil }
	t0 := time.Unix(1000, 0)
	if h.sampleHostIO(t0) != nil {
		t.Fatal("first sample must have no rate")
	}
	cur = hostCounters{diskRd: 2048, diskWr: 4096, netRx: 1000, netTx: 500}
	io := h.sampleHostIO(t0.Add(2 * time.Second))
	if io == nil || io.DiskReadBps != 1024 || io.DiskWriteBps != 2048 || io.NetRxBps != 500 || io.NetTxBps != 250 {
		t.Errorf("io = %+v", io)
	}
}

func TestHostIOMissing(t *testing.T) {
	if _, err := readHostCounters(fstest.MapFS{}); err == nil {
		t.Error("missing /proc must be an error")
	}
	h := newTestHub(&fakeSource{})
	h.hostIO = nil
	if h.sampleHostIO(time.Now()) != nil {
		t.Error("no host io source must give nil")
	}
}
