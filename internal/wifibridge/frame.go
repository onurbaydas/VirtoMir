// Package wifibridge puts machines on the LAN of a Wi-Fi card the way VirtualBox and VMware
// Workstation bridge to Wi-Fi: an access point accepts frames only from the card's own MAC
// address, so the machines' frames go out with the host's MAC (MAC translation) and come back
// by their IP address. The machines still get their addresses from the LAN's own DHCP server
// (the router): a DHCP request carries the machine's MAC inside it, and is sent with the
// broadcast flag so the answer reaches the host. IPv4 only; IPv6 needs NDP translation.
package wifibridge

import (
	"encoding/binary"
	"net/netip"
)

const (
	etherARP  = 0x0806
	etherIPv4 = 0x0800
	ethLen    = 14
)

// MAC is an Ethernet address.
type MAC [6]byte

// Table knows which machine has which IPv4 address (learned from what they send).
type Table interface {
	Lookup(ip netip.Addr) (MAC, bool)
	Learn(ip netip.Addr, mac MAC)
}

func etherType(f []byte) uint16 { return binary.BigEndian.Uint16(f[12:14]) }

func addr4(b []byte) netip.Addr { return netip.AddrFrom4([4]byte(b[:4])) }

// Outbound turns a frame a machine sent into the frame the Wi-Fi card sends: the host's MAC
// as sender (also inside ARP), and DHCP requests with the broadcast flag. It learns the
// machine's address. ok is false for frames that are not passed on (IPv6, other protocols).
func Outbound(f []byte, host MAC, t Table) (out []byte, ok bool) {
	if len(f) < ethLen {
		return nil, false
	}
	var vm MAC
	copy(vm[:], f[6:12])
	switch etherType(f) {
	case etherARP:
		if len(f) < ethLen+28 {
			return nil, false
		}
		out = append([]byte(nil), f...)
		a := out[ethLen:]
		if ip := addr4(a[14:18]); ip.IsValid() && !ip.IsUnspecified() {
			t.Learn(ip, vm)
		}
		copy(a[8:14], host[:]) // sender hardware address
	case etherIPv4:
		ihl, ok := ipv4Header(f)
		if !ok {
			return nil, false
		}
		out = append([]byte(nil), f...)
		if ip := addr4(out[ethLen+12:]); !ip.IsUnspecified() {
			t.Learn(ip, vm)
		}
		if udp, ok := udpPorts(out, ihl, 68, 67); ok && len(udp) >= 8+12 {
			binary.BigEndian.PutUint16(udp[8+10:], binary.BigEndian.Uint16(udp[8+10:])|0x8000) // DHCP flags: broadcast
			binary.BigEndian.PutUint16(udp[6:8], 0)                                            // no UDP checksum (allowed for IPv4)
		}
	default:
		return nil, false
	}
	copy(out[6:12], host[:])
	return out, true
}

// Inbound turns a frame the Wi-Fi card received into the frame for the machines' bridge: one
// for a machine's address goes to that machine's MAC; broadcasts (ARP questions, DHCP answers,
// which also teach a machine's address) go to all of them. ok is false for frames that are
// the host's own.
func Inbound(f []byte, host MAC, t Table) (out []byte, ok bool) {
	if len(f) < ethLen {
		return nil, false
	}
	broadcast := f[0]&1 == 1
	if !broadcast && MAC(f[0:6]) != host {
		return nil, false
	}
	switch etherType(f) {
	case etherARP:
		if len(f) < ethLen+28 {
			return nil, false
		}
		a := f[ethLen:]
		// Some access points answer ARP themselves and echo it back from and to the host's
		// MAC with no target hardware address; a machine would take it for an address
		// conflict (as found for the kernel's ipvlan MAC-NAT mode).
		if MAC(f[6:12]) == host && !broadcast && MAC(a[18:24]) == (MAC{}) {
			return nil, false
		}
		vm, mine := t.Lookup(addr4(a[24:28]))
		if broadcast {
			return append([]byte(nil), f...), true // "who has …": the machine with it answers
		}
		if !mine {
			return nil, false // for the host itself
		}
		out = append([]byte(nil), f...)
		copy(out[0:6], vm[:])
		copy(out[ethLen+18:ethLen+24], vm[:]) // target hardware address
		return out, true
	case etherIPv4:
		ihl, ok := ipv4Header(f)
		if !ok {
			return nil, false
		}
		if udp, ok := udpPorts(f, ihl, 67, 68); ok && len(udp) >= 8+44 {
			dhcp := udp[8:]
			var vm MAC
			copy(vm[:], dhcp[28:34]) // chaddr: the machine the answer is for
			if yi := addr4(dhcp[16:20]); !yi.IsUnspecified() {
				t.Learn(yi, vm)
			}
			out = append([]byte(nil), f...)
			if !broadcast {
				copy(out[0:6], vm[:])
			}
			return out, true
		}
		dst := addr4(f[ethLen+16:])
		if broadcast {
			return append([]byte(nil), f...), true
		}
		if dst.IsMulticast() { // some Wi-Fi networks send multicast to the station's MAC
			out = append([]byte(nil), f...)
			copy(out[0:6], []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
			return out, true
		}
		vm, mine := t.Lookup(dst)
		if !mine {
			return nil, false
		}
		out = append([]byte(nil), f...)
		copy(out[0:6], vm[:])
		return out, true
	}
	return nil, false
}

// ipv4Header checks an IPv4 frame and returns its header length.
func ipv4Header(f []byte) (int, bool) {
	if len(f) < ethLen+20 || f[ethLen]>>4 != 4 {
		return 0, false
	}
	ihl := int(f[ethLen]&0x0f) * 4
	return ihl, ihl >= 20 && len(f) >= ethLen+ihl
}

// udpPorts returns the UDP header and payload when the IPv4 frame is UDP from src to dst.
func udpPorts(f []byte, ihl int, src, dst uint16) ([]byte, bool) {
	if f[ethLen+9] != 17 || len(f) < ethLen+ihl+8 {
		return nil, false
	}
	udp := f[ethLen+ihl:]
	return udp, binary.BigEndian.Uint16(udp[0:2]) == src && binary.BigEndian.Uint16(udp[2:4]) == dst
}
