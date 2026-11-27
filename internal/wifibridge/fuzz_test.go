package wifibridge

import (
	"bytes"
	"net/netip"
	"testing"
)

// fuzzTable is a Table with no state shared between runs.
type fuzzTable struct {
	ips  map[netip.Addr]MAC
	seen map[MAC]bool
}

func newFuzzTable() *fuzzTable {
	return &fuzzTable{ips: map[netip.Addr]MAC{vmIP: vm}, seen: map[MAC]bool{vm: true}}
}

func (t *fuzzTable) Lookup(ip netip.Addr) (MAC, bool) { m, ok := t.ips[ip]; return m, ok }
func (t *fuzzTable) Learn(ip netip.Addr, mac MAC)     { t.ips[ip] = mac }
func (t *fuzzTable) Seen(mac MAC)                     { t.seen[mac] = true }
func (t *fuzzTable) Machine(mac MAC) bool             { return t.seen[mac] }

// seeds are the frames the other tests build, and some cut short.
func seeds() [][]byte {
	frames := [][]byte{
		eth(bcast, vm, etherARP, arp(1, vm, vmIP, MAC{}, gwIP)),
		eth(host, router, etherARP, arp(2, router, gwIP, host, vmIP)),
		eth(router, vm, etherIPv4, ipv4(vmIP, gwIP, 17, udp(5353, 53, []byte("query")))),
		eth(bcast, vm, etherIPv4, ipv4(netip.IPv4Unspecified(), netip.MustParseAddr("255.255.255.255"), 17, udp(68, 67, dhcp(1, netip.IPv4Unspecified(), vm)))),
		eth(bcast, router, etherIPv4, ipv4(gwIP, netip.MustParseAddr("255.255.255.255"), 17, udp(67, 68, dhcp(2, vmIP, vm)))),
		eth(host, router, etherIPv4, ipv4(gwIP, vmIP, 6, []byte("tcp payload"))),
	}
	var out [][]byte
	for _, f := range frames {
		out = append(out, f, f[:len(f)/2], f[:ethLen+1])
	}
	return append(out, nil, []byte{1, 2, 3})
}

func FuzzOutbound(f *testing.F) {
	for _, s := range seeds() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, frame []byte) {
		in := bytes.Clone(frame)
		out, ok := Outbound(frame, host, newFuzzTable())
		if !bytes.Equal(frame, in) {
			t.Fatal("Outbound changed its input")
		}
		if !ok {
			return
		}
		if len(out) < len(frame) {
			t.Fatalf("output of %d bytes for %d", len(out), len(frame))
		}
		if MAC(out[6:12]) != host {
			t.Fatalf("source %x, want the host's MAC", out[6:12])
		}
	})
}

func FuzzInbound(f *testing.F) {
	for _, s := range seeds() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, frame []byte) {
		out, ok := Inbound(bytes.Clone(frame), host, newFuzzTable())
		if ok && len(out) < len(frame) {
			t.Fatalf("output of %d bytes for %d", len(out), len(frame))
		}
	})
}
