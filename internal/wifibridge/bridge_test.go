package wifibridge

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The host once reached a machine through the bridge, its ARP question was taken for a
// machine's, and its own address landed in the drop set: the host lost its connection.
func TestTheHostIsNeverAMachine(t *testing.T) {
	hostIP := netip.MustParseAddr("192.168.1.210")
	var learned []netip.Addr
	tab := &table{m: map[netip.Addr]MAC{}, learn: func(ip netip.Addr) { learned = append(learned, ip) },
		never: func(ip netip.Addr) bool { return ip == hostIP }}
	bridgeMAC := MAC{0xe2, 0xe3, 0xcc, 0xa7, 0xb3, 0x96}

	ask := eth(bcast, bridgeMAC, etherARP, arp(1, bridgeMAC, hostIP, MAC{}, vmIP))
	if !fromHost(ask, []MAC{bridgeMAC}) {
		t.Fatal("the host's own ARP question on the bridge was not recognised")
	}
	// Even if a frame from the host's address got through, it is not learned.
	Outbound(eth(bcast, vm, etherARP, arp(1, vm, hostIP, MAC{}, vmIP)), host, tab)
	if len(learned) != 0 {
		t.Fatalf("learned %v", learned)
	}
	Outbound(eth(bcast, vm, etherARP, arp(1, vm, vmIP, MAC{}, gwIP)), host, tab)
	Outbound(eth(bcast, vm, etherARP, arp(1, vm, vmIP, MAC{}, gwIP)), host, tab)
	if len(learned) != 1 || learned[0] != vmIP {
		t.Errorf("a machine: learned %v (once)", learned)
	}
	if fromHost(eth(bcast, vm, etherARP, nil), []MAC{bridgeMAC}) {
		t.Error("a machine's frame taken for the host's")
	}
}

type pingRunner struct{ fail atomic.Bool }

func (p *pingRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	if name == "ping" && p.fail.Load() {
		return nil, errors.New("exit status 1")
	}
	if name == "ip" && strings.Join(args, " ") == "-4 route show default dev wlan0" {
		return []byte("default via 192.168.1.1 proto dhcp src 192.168.1.210 metric 600\n"), nil
	}
	return nil, nil
}

func TestWatchNoticesTheHostCutOff(t *testing.T) {
	r := &pingRunner{}
	gw := gateway(context.Background(), r, "wlan0")
	if gw.String() != "192.168.1.1" {
		t.Fatalf("gateway %s", gw)
	}
	done := make(chan struct{})
	go func() { watch(context.Background(), r, "wlan0", gw, 5*time.Millisecond, 3); close(done) }()
	select {
	case <-done:
		t.Fatal("gave up while the gateway answered")
	case <-time.After(60 * time.Millisecond):
	}
	r.fail.Store(true)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("did not notice the host lost its gateway")
	}
}

// What a machine claims does not decide everything: the gateway's address and addresses
// outside the LAN are never a machine's, and a machine that makes up senders does not fill
// the table.
func TestTableRefusesWhatIsNotAMachines(t *testing.T) {
	lan := []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24")}
	var learned []netip.Addr
	tab := &table{m: map[netip.Addr]MAC{}, learn: func(ip netip.Addr) { learned = append(learned, ip) },
		never: func(ip netip.Addr) bool { return ip == gwIP || !onLAN(ip, lan) }}
	for _, ip := range []string{"192.168.1.1", "8.8.8.8", "10.0.0.5", "192.168.1.0", "192.168.1.255", "169.254.7.7", "224.0.0.251", "127.0.0.1", "255.255.255.255"} {
		tab.Learn(netip.MustParseAddr(ip), vm)
	}
	if len(learned) != 0 {
		t.Fatalf("learned %v", learned)
	}
	if _, ok := tab.Lookup(gwIP); ok {
		t.Error("the gateway's address is a machine's: the host would route it into the bridge")
	}
	tab.Learn(vmIP, vm)
	other := MAC{0x52, 0x54, 0x00, 0xaa, 0xbb, 0xcc}
	tab.Learn(vmIP, other) // the address moved to another machine: known, not announced again
	if got, _ := tab.Lookup(vmIP); got != other || len(learned) != 1 {
		t.Errorf("moved address: %v, announced %v", got, learned)
	}

	big := &table{m: map[netip.Addr]MAC{}, learn: func(netip.Addr) {}}
	for i := 0; i < maxAddresses+500; i++ {
		big.Learn(netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)}), vm)
		big.Seen(MAC{2, 0, byte(i >> 16), byte(i >> 8), byte(i), 1})
	}
	if len(big.m) != maxAddresses || len(big.macs) != maxMachines {
		t.Errorf("%d addresses, %d machines remembered", len(big.m), len(big.macs))
	}
	if !big.Machine(MAC{2, 0, 0, 0, 0, 1}) || big.Machine(MAC{2, 0, 9, 9, 9, 1}) {
		t.Error("which MACs are machines")
	}
}

func TestOnLAN(t *testing.T) {
	nets := []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24"), netip.MustParsePrefix("10.8.0.0/31")}
	for ip, want := range map[string]bool{
		"192.168.1.57": true, "192.168.1.254": true, "192.168.1.0": false, "192.168.1.255": false,
		"192.168.2.1": false, "10.8.0.0": true, "10.8.0.1": true, "10.8.0.2": false,
	} {
		if got := onLAN(netip.MustParseAddr(ip), nets); got != want {
			t.Errorf("onLAN(%s) = %v", ip, got)
		}
	}
	if !onLAN(netip.MustParseAddr("203.0.113.9"), nil) {
		t.Error("a card without an address says nothing about the LAN")
	}
}

func TestViewsShowWhatTheRelayLearned(t *testing.T) {
	tab := &table{m: map[netip.Addr]MAC{}, learn: func(netip.Addr) {}}
	tab.Learn(netip.MustParseAddr("192.168.1.57"), MAC{0x52, 0x54, 0, 0x11, 0x22, 0x33})
	tab.Learn(netip.MustParseAddr("192.168.1.9"), MAC{0x52, 0x54, 0, 0xaa, 0xbb, 0xcc})
	m := &Manager{
		bridges: map[string]Config{"lan": {Network: "lan", Bridge: "wbr0", Uplink: "wlp3s0"}, "cafe": {Network: "cafe", Bridge: "wbr1", Uplink: "wlan1"}},
		tables:  map[string]*table{"lan": tab},
	}
	v := m.Views()
	if len(v) != 2 || v[0].Network != "cafe" || v[1].Network != "lan" {
		t.Fatalf("views = %+v", v)
	}
	if v[0].Running || len(v[0].Machines) != 0 || v[0].Machines == nil {
		t.Errorf("a bridge whose relay is not up: %+v", v[0])
	}
	want := []Learned{{IP: "192.168.1.9", MAC: "52:54:00:aa:bb:cc"}, {IP: "192.168.1.57", MAC: "52:54:00:11:22:33"}}
	if !v[1].Running || v[1].Uplink != "wlp3s0" || v[1].Bridge != "wbr0" || !slices.Equal(v[1].Machines, want) {
		t.Errorf("lan = %+v", v[1])
	}
}
