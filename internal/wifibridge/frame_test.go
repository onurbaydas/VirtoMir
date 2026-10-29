package wifibridge

import (
	"encoding/binary"
	"net/netip"
	"testing"
)

type mapTable map[netip.Addr]MAC

func (m mapTable) Lookup(ip netip.Addr) (MAC, bool) { v, ok := m[ip]; return v, ok }
func (m mapTable) Learn(ip netip.Addr, mac MAC)     { m[ip] = mac }

var (
	host   = MAC{0xda, 0x2d, 0xb5, 0xa8, 0x9c, 0x79}
	vm     = MAC{0x52, 0x54, 0x00, 0x11, 0x22, 0x33}
	router = MAC{0xaa, 0xbb, 0xcc, 0x00, 0x00, 0x01}
	bcast  = MAC{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	vmIP   = netip.MustParseAddr("192.168.1.57")
	gwIP   = netip.MustParseAddr("192.168.1.1")
)

func eth(dst, src MAC, typ uint16, payload []byte) []byte {
	f := append(append(append([]byte{}, dst[:]...), src[:]...), byte(typ>>8), byte(typ))
	return append(f, payload...)
}

func arp(op uint16, sha MAC, spa netip.Addr, tha MAC, tpa netip.Addr) []byte {
	a := []byte{0, 1, 8, 0, 6, 4, byte(op >> 8), byte(op)}
	s, t := spa.As4(), tpa.As4()
	a = append(a, sha[:]...)
	a = append(a, s[:]...)
	a = append(a, tha[:]...)
	return append(a, t[:]...)
}

func ipv4(src, dst netip.Addr, proto byte, payload []byte) []byte {
	h := make([]byte, 20)
	h[0], h[9] = 0x45, proto
	binary.BigEndian.PutUint16(h[2:], uint16(20+len(payload)))
	s, d := src.As4(), dst.As4()
	copy(h[12:], s[:])
	copy(h[16:], d[:])
	return append(h, payload...)
}

func udp(sport, dport uint16, payload []byte) []byte {
	u := make([]byte, 8)
	binary.BigEndian.PutUint16(u[0:], sport)
	binary.BigEndian.PutUint16(u[2:], dport)
	binary.BigEndian.PutUint16(u[4:], uint16(8+len(payload)))
	binary.BigEndian.PutUint16(u[6:], 0xbeef)
	return append(u, payload...)
}

func dhcp(op byte, yiaddr netip.Addr, chaddr MAC) []byte {
	d := make([]byte, 240)
	d[0], d[1], d[2] = op, 1, 6
	y := yiaddr.As4()
	copy(d[16:], y[:])
	copy(d[28:], chaddr[:])
	return d
}

func TestARPRoundTrip(t *testing.T) {
	tab := mapTable{}
	// The machine asks for the router: it goes out with the host's MAC, inside ARP too.
	out, ok := Outbound(eth(bcast, vm, etherARP, arp(1, vm, vmIP, MAC{}, gwIP)), host, tab)
	if !ok || MAC(out[6:12]) != host || MAC(out[ethLen+8:ethLen+14]) != host {
		t.Fatalf("ARP out: %x", out)
	}
	if tab[vmIP] != vm {
		t.Error("the machine's address was not learned")
	}
	// The router answers the host's MAC; it is turned back to the machine's.
	in, ok := Inbound(eth(host, router, etherARP, arp(2, router, gwIP, host, vmIP)), host, tab)
	if !ok || MAC(in[0:6]) != vm || MAC(in[ethLen+18:ethLen+24]) != vm {
		t.Fatalf("ARP in: %x", in)
	}
	// An answer for the host itself stays with the host.
	if _, ok := Inbound(eth(host, router, etherARP, arp(2, router, gwIP, host, netip.MustParseAddr("192.168.1.210"))), host, tab); ok {
		t.Error("the host's own ARP answer went to the machines")
	}
	// "Who has the machine's address?" reaches the machines.
	if _, ok := Inbound(eth(bcast, router, etherARP, arp(1, router, gwIP, MAC{}, vmIP)), host, tab); !ok {
		t.Error("an ARP question did not reach the machines")
	}
}

func TestDHCPFromTheRouter(t *testing.T) {
	tab := mapTable{}
	zero := netip.MustParseAddr("0.0.0.0")
	discover := eth(bcast, vm, etherIPv4, ipv4(zero, netip.MustParseAddr("255.255.255.255"), 17, udp(68, 67, dhcp(1, zero, vm))))
	out, ok := Outbound(discover, host, tab)
	if !ok || MAC(out[6:12]) != host {
		t.Fatalf("discover: %x", out)
	}
	d := out[ethLen+20+8:]
	if binary.BigEndian.Uint16(d[10:12])&0x8000 == 0 || MAC(d[28:34]) != vm {
		t.Error("the request lacks the broadcast flag or the machine's MAC (the router keys the lease on it)")
	}
	if binary.BigEndian.Uint16(out[ethLen+20+6:]) != 0 {
		t.Error("the UDP checksum was not cleared after the change")
	}
	if len(tab) != 0 {
		t.Error("learned 0.0.0.0")
	}
	// The router's offer, broadcast: it reaches the machines and teaches its address.
	offer := eth(bcast, router, etherIPv4, ipv4(gwIP, netip.MustParseAddr("255.255.255.255"), 17, udp(67, 68, dhcp(2, vmIP, vm))))
	if in, ok := Inbound(offer, host, tab); !ok || MAC(in[0:6]) != bcast {
		t.Fatalf("offer: %v", ok)
	}
	if tab[vmIP] != vm {
		t.Error("the offered address was not learned")
	}
	// A router that answers unicast to the host's MAC: turned to the machine.
	if in, ok := Inbound(eth(host, router, etherIPv4, ipv4(gwIP, vmIP, 17, udp(67, 68, dhcp(2, vmIP, vm)))), host, tab); !ok || MAC(in[0:6]) != vm {
		t.Error("a unicast DHCP answer did not reach the machine")
	}
}

func TestIPv4Traffic(t *testing.T) {
	tab := mapTable{vmIP: vm}
	out, ok := Outbound(eth(router, vm, etherIPv4, ipv4(vmIP, gwIP, 1, make([]byte, 8))), host, tab)
	if !ok || MAC(out[6:12]) != host || MAC(out[0:6]) != router {
		t.Fatalf("out: %x", out)
	}
	in, ok := Inbound(eth(host, router, etherIPv4, ipv4(gwIP, vmIP, 1, make([]byte, 8))), host, tab)
	if !ok || MAC(in[0:6]) != vm {
		t.Fatalf("in: %x", in)
	}
	if _, ok := Inbound(eth(host, router, etherIPv4, ipv4(gwIP, netip.MustParseAddr("192.168.1.210"), 1, nil)), host, tab); ok {
		t.Error("the host's own traffic went to the machines")
	}
	if _, ok := Inbound(eth(MAC{2, 0, 0, 0, 0, 9}, router, etherIPv4, ipv4(gwIP, vmIP, 1, nil)), host, tab); ok {
		t.Error("a frame for another MAC was taken")
	}
	if _, ok := Outbound(eth(router, vm, 0x86dd, make([]byte, 40)), host, tab); ok {
		t.Error("IPv6 passed (it needs NDP translation)")
	}
	if _, ok := Outbound([]byte{1, 2, 3}, host, tab); ok {
		t.Error("a runt frame passed")
	}
}

func TestAccessPointQuirks(t *testing.T) {
	tab := mapTable{vmIP: vm}
	// An access point's ARP echo, from and to the host's MAC with no target hardware address.
	if _, ok := Inbound(eth(host, host, etherARP, arp(2, host, vmIP, MAC{}, vmIP)), host, tab); ok {
		t.Error("the access point's ARP echo reached the machine (an address conflict for it)")
	}
	// Multicast sent to the host's MAC reaches the machines as a broadcast.
	mdns := netip.MustParseAddr("224.0.0.251")
	in, ok := Inbound(eth(host, router, etherIPv4, ipv4(gwIP, mdns, 17, udp(5353, 5353, nil))), host, tab)
	if !ok || MAC(in[0:6]) != bcast {
		t.Errorf("multicast to the station's MAC: %v %x", ok, in)
	}
}
