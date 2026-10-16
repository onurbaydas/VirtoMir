package virt

import (
	"reflect"
	"strings"
	"testing"

	"libvirt.org/go/libvirtxml"
)

func TestFirewallRoundTrip(t *testing.T) {
	fw := Firewall{Enabled: true, DefaultIn: "drop", DefaultOut: "accept", AntiSpoof: true, Rules: []FirewallRule{
		{Direction: "in", Action: "accept", Protocol: "tcp", Ports: "22", Remote: "10.0.0.0/8", Comment: "ssh from the LAN"},
		{Direction: "in", Action: "accept", Protocol: "tcp", Ports: "8000-8100"},
		{Direction: "in", Action: "accept", Protocol: "icmp"},
		{Direction: "out", Action: "reject", Protocol: "udp", Ports: "53", Remote: "8.8.8.8"},
		{Direction: "out", Action: "drop", Protocol: "all", Remote: "192.168.50.0/24"},
	}}
	doc, err := buildFilter("virtomir-abc", fw)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<filter name="virtomir-abc" chain="root">`, `<filterref filter="clean-traffic">`,
		`srcipaddr="10.0.0.0" srcipmask="8"`, `dstportstart="22"`, `dstportstart="8000" dstportend="8100"`,
		`dstipaddr="8.8.8.8"`, `action="drop" direction="in" priority="900"`,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("filter lacks %s:\n%s", want, doc)
		}
	}
	if strings.Contains(doc, `direction="out" priority="900"`) {
		t.Error("accept-all default out written as a rule")
	}
	back, err := parseFilter(doc)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, fw) {
		t.Errorf("round trip:\n got %+v\nwant %+v", back, fw)
	}
}

func TestFirewallIPv6(t *testing.T) {
	fw := Firewall{Enabled: true, DefaultIn: "drop", DefaultOut: "accept", Rules: []FirewallRule{
		{Direction: "in", Action: "accept", Protocol: "tcp", Ports: "22"},                           // both families
		{Direction: "in", Action: "accept", Protocol: "tcp", Ports: "443", Remote: "2001:db8::/32"}, // IPv6 only
		{Direction: "out", Action: "drop", Protocol: "all", Remote: "fd00::1"},                      // IPv6 only
		{Direction: "in", Action: "accept", Protocol: "icmp"},                                       // icmp + icmpv6
		{Direction: "in", Action: "accept", Protocol: "udp", Ports: "53", Remote: "10.0.0.0/8"},     // IPv4 only
	}}
	doc, err := buildFilter("virtomir-v6", fw)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<tcp-ipv6 dstportstart="22"`, `<tcp dstportstart="22"`,
		`<tcp-ipv6 srcipaddr="2001:db8::" srcipmask="32" dstportstart="443"`,
		`<all-ipv6 dstipaddr="fd00::1"`, `<icmpv6>`, `<icmp>`,
		`type="135"`, `dstportstart="547"`,
		`<all-ipv6 comment="VirtoMir default in"`,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("filter lacks %s:\n%s", want, doc)
		}
	}
	if strings.Contains(doc, `<tcp srcipaddr="2001`) || strings.Contains(doc, `<udp-ipv6 srcipaddr="10.`) {
		t.Errorf("an address rule leaked into the other family:\n%s", doc)
	}
	back, err := parseFilter(doc)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, fw) {
		t.Errorf("round trip:\n got %+v\nwant %+v", back, fw)
	}
	// Accept-all defaults need no IPv6 basics.
	doc, _ = buildFilter("x", Firewall{Enabled: true, DefaultIn: "accept", DefaultOut: "accept"})
	if strings.Contains(doc, "neighbor discovery") {
		t.Errorf("basics without a drop policy:\n%s", doc)
	}
}

func TestFirewallValidation(t *testing.T) {
	for _, r := range []FirewallRule{
		{Direction: "sideways", Action: "accept", Protocol: "tcp"},
		{Direction: "in", Action: "allow", Protocol: "tcp"},
		{Direction: "in", Action: "accept", Protocol: "gre"},
		{Direction: "in", Action: "accept", Protocol: "icmp", Ports: "22"},
		{Direction: "in", Action: "accept", Protocol: "tcp", Ports: "70000"},
		{Direction: "in", Action: "accept", Protocol: "tcp", Ports: "90-80"},
		{Direction: "in", Action: "accept", Protocol: "tcp", Remote: "fe80::zz"},
		{Direction: "in", Action: "accept", Protocol: "tcp", Remote: "2001:db8::/129"},
		{Direction: "in", Action: "accept", Protocol: "tcp", Remote: "not-an-ip"},
		{Direction: "in", Action: "accept", Protocol: "tcp", Comment: `say "hi"`},
	} {
		if _, err := buildFilter("x", Firewall{Enabled: true, Rules: []FirewallRule{r}}); err == nil {
			t.Errorf("accepted %+v", r)
		}
	}
}

func TestSetFilterRefs(t *testing.T) {
	x := libvirtxml.Domain{Devices: &libvirtxml.DomainDeviceList{Interfaces: []libvirtxml.DomainInterface{
		{Source: &libvirtxml.DomainInterfaceSource{Network: &libvirtxml.DomainInterfaceSourceNetwork{Network: "default"}}},
		{Source: &libvirtxml.DomainInterfaceSource{Bridge: &libvirtxml.DomainInterfaceSourceBridge{Bridge: "br0"}},
			FilterRef: &libvirtxml.DomainInterfaceFilterRef{Filter: "clean-traffic"}},
		{Source: &libvirtxml.DomainInterfaceSource{Direct: &libvirtxml.DomainInterfaceSourceDirect{Dev: "eno1"}}},
	}}}
	if !setFilterRefs(&x, "virtomir-u") {
		t.Fatal("nothing changed")
	}
	ifs := x.Devices.Interfaces
	if ifs[0].FilterRef.Filter != "virtomir-u" || ifs[1].FilterRef.Filter != "virtomir-u" || ifs[2].FilterRef != nil {
		t.Errorf("refs = %+v %+v %+v", ifs[0].FilterRef, ifs[1].FilterRef, ifs[2].FilterRef)
	}
	if setFilterRefs(&x, "virtomir-u") {
		t.Error("changed twice")
	}
	if !setFilterRefs(&x, "") || ifs[0].FilterRef != nil || ifs[1].FilterRef != nil {
		t.Error("references not removed")
	}
	// A filter someone set up outside VirtoMir is left alone when disabling.
	ifs[0].FilterRef = &libvirtxml.DomainInterfaceFilterRef{Filter: "clean-traffic"}
	setFilterRefs(&x, "")
	if ifs[0].FilterRef == nil {
		t.Error("foreign filter removed")
	}
}
