package wifibridge

import (
	"context"
	"errors"
	"net/netip"
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
		local: func(ip netip.Addr) bool { return ip == hostIP }}
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
