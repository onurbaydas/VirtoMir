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

func TestFirewallValidation(t *testing.T) {
	for _, r := range []FirewallRule{
		{Direction: "sideways", Action: "accept", Protocol: "tcp"},
		{Direction: "in", Action: "allow", Protocol: "tcp"},
		{Direction: "in", Action: "accept", Protocol: "gre"},
		{Direction: "in", Action: "accept", Protocol: "icmp", Ports: "22"},
		{Direction: "in", Action: "accept", Protocol: "tcp", Ports: "70000"},
		{Direction: "in", Action: "accept", Protocol: "tcp", Ports: "90-80"},
		{Direction: "in", Action: "accept", Protocol: "tcp", Remote: "fe80::1"},
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
