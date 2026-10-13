package virt

import (
	"slices"
	"testing"

	"github.com/digitalocean/go-libvirt"
)

func tp(field string, v any) libvirt.TypedParam {
	return libvirt.TypedParam{Field: field, Value: libvirt.TypedParamValue{I: v}}
}

func TestParseStats(t *testing.T) {
	// Shape as returned by libvirt 11 for a running VM (types as go-libvirt decodes them).
	s := parseStats("u1", []libvirt.TypedParam{
		tp("cpu.time", uint64(9_000_000_000)),
		tp("cpu.user", uint64(1)),
		tp("balloon.current", uint64(2097152)),
		tp("balloon.maximum", uint64(4194304)),
		tp("balloon.rss", uint64(1500000)),
		tp("balloon.usable", uint64(1200000)),
		tp("balloon.available", uint64(2000000)),
		tp("vcpu.current", uint32(2)),
		tp("vcpu.maximum", uint32(4)),
		tp("vcpu.0.state", int32(1)),
		tp("vcpu.0.time", uint64(5_000_000_000)),
		tp("vcpu.1.time", uint64(3_000_000_000)),
		tp("net.count", uint32(1)),
		tp("net.0.name", "vnet2"),
		tp("net.0.rx.bytes", uint64(1000)),
		tp("net.0.tx.bytes", uint64(2000)),
		tp("net.0.rx.drop", uint64(3)),
		tp("block.count", uint32(2)),
		tp("block.0.name", "vda"),
		tp("block.0.rd.bytes", uint64(4096)),
		tp("block.0.wr.bytes", uint64(8192)),
		tp("block.0.rd.reqs", uint64(1)),
		tp("block.0.wr.reqs", uint64(2)),
		tp("block.0.rd.times", uint64(100_000)),
		tp("block.0.wr.times", uint64(200_000)),
		tp("block.1.name", "sda"), // empty CD-ROM: no counters
		tp("some.future.field", "x"),
	})
	if s.UUID != "u1" || s.CPUTimeNs != 9_000_000_000 {
		t.Errorf("cpu: %+v", s)
	}
	if !slices.Equal(s.VCPUTimeNs, []uint64{5_000_000_000, 3_000_000_000}) {
		t.Errorf("vcpu times = %v", s.VCPUTimeNs)
	}
	if b := s.Balloon; b.CurrentKiB != 2097152 || b.MaximumKiB != 4194304 || b.RSSKiB != 1500000 || b.UsableKiB != 1200000 || b.AvailableKiB != 2000000 {
		t.Errorf("balloon = %+v", b)
	}
	if len(s.Nets) != 1 || s.Nets[0].Name != "vnet2" || s.Nets[0].RxBytes != 1000 || s.Nets[0].TxBytes != 2000 || s.Nets[0].RxDrop != 3 {
		t.Errorf("nets = %+v", s.Nets)
	}
	if len(s.Disks) != 2 || s.Disks[0].Name != "vda" || s.Disks[0].WrBytes != 8192 || !s.Disks[0].Timed {
		t.Errorf("disks = %+v", s.Disks)
	}
	if s.Disks[1].Name != "sda" || s.Disks[1].Timed {
		t.Errorf("cdrom = %+v", s.Disks[1])
	}
}

func TestIndexed(t *testing.T) {
	if n, f, ok := indexed("12.rd.bytes"); !ok || n != 12 || f != "rd.bytes" {
		t.Errorf("got %d %q %v", n, f, ok)
	}
	for _, bad := range []string{"count", "x.name", "-1.name"} {
		if _, _, ok := indexed(bad); ok {
			t.Errorf("%q must not parse", bad)
		}
	}
}
