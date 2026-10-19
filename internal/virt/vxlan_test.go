package virt

import (
	"slices"
	"testing"
)

func TestVXLANNetworks(t *testing.T) {
	// The gateway host: NAT and DHCP on the overlay.
	gw, err := buildNetwork(NetworkSpec{Name: "overlay", Mode: "vxlan", VNI: 100, Subnet: "10.100.0.0/24", DHCP: true,
		Peers: []string{"192.168.77.11", " 192.168.77.12", "192.168.77.11", "::ffff:192.168.77.13"}})
	if err != nil {
		t.Fatal(err)
	}
	if gw.Forward == nil || gw.Forward.Mode != "nat" || gw.Bridge.Name != "vxbr100" || gw.MTU.Size != VXLANMTU || len(gw.IPs) != 1 {
		t.Errorf("gateway network: forward=%+v bridge=%+v mtu=%+v ips=%+v", gw.Forward, gw.Bridge, gw.MTU, gw.IPs)
	}
	raw, err := gw.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	v, err := parseNetwork(raw)
	if err != nil {
		t.Fatal(err)
	}
	if v.Mode != "vxlan" || v.VNI != 100 || !slices.Equal(v.Peers, []string{"192.168.77.11", "192.168.77.12", "192.168.77.13"}) || v.Subnet != "10.100.0.1/24" {
		t.Errorf("parsed = %+v\n%s", v, raw)
	}

	// The other hosts: plain layer 2, no address and no DHCP of their own.
	l2, err := buildNetwork(NetworkSpec{Name: "overlay", Mode: "vxlan", VNI: 100, Peers: []string{"192.168.77.10"}})
	if err != nil {
		t.Fatal(err)
	}
	if l2.Forward != nil || len(l2.IPs) != 0 || l2.Bridge.Name != "vxbr100" {
		t.Errorf("layer-2 network: forward=%+v ips=%+v", l2.Forward, l2.IPs)
	}

	for _, bad := range []NetworkSpec{
		{Name: "x", Mode: "vxlan"},
		{Name: "x", Mode: "vxlan", VNI: 1 << 24},
		{Name: "x", Mode: "vxlan", VNI: 5, Peers: []string{"host.example"}},
		{Name: "x", Mode: "vxlan", VNI: 5, Peers: []string{"239.1.1.1"}},
		{Name: "x", Mode: "vxlan", VNI: 5, Peers: []string{"127.0.0.1"}},
	} {
		if _, err := buildNetwork(bad); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}

	// A network another tool tagged is not mistaken for ours.
	if _, ok := parseVXLANMeta(nil); ok {
		t.Error("nil metadata")
	}
}

func TestClusterVXLANMarker(t *testing.T) {
	x, err := buildNetwork(NetworkSpec{Name: "ov", Mode: "vxlan", VNI: 7, Peers: []string{"10.0.0.2"}, Cluster: true})
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := x.Marshal()
	v, err := parseNetwork(doc)
	if err != nil || !v.ClusterWide || v.VNI != 7 {
		t.Fatalf("cluster overlay read back as %+v (%v)", v, err)
	}
	x, _ = buildNetwork(NetworkSpec{Name: "ov", Mode: "vxlan", VNI: 7, Peers: []string{"10.0.0.2"}})
	doc, _ = x.Marshal()
	if v, _ := parseNetwork(doc); v.ClusterWide {
		t.Error("a hand-made overlay is not cluster-wide")
	}
}
