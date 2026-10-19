package virt

import (
	"strings"
	"testing"
)

func TestBuildNICOVN(t *testing.T) {
	nic, err := buildNIC(NICSpec{IfaceID: "vm-port-4f1c2a7e-525400aabbcc", MAC: "52:54:00:aa:bb:cc", OVNSubnet: "prod-web"})
	if err != nil {
		t.Fatal(err)
	}
	if nic.Source.Bridge == nil || nic.Source.Bridge.Bridge != "br-int" || nic.VirtualPort.Params.OpenVSwitch.InterfaceID != "vm-port-4f1c2a7e-525400aabbcc" || nic.MAC.Address != "52:54:00:aa:bb:cc" {
		t.Errorf("%+v", nic)
	}
	doc, _ := (&nic).Marshal()
	for _, want := range []string{`<source bridge="br-int">`, `<virtualport type="openvswitch">`, `interfaceid="vm-port-4f1c2a7e-525400aabbcc"`} {
		if !strings.Contains(doc, want) {
			t.Errorf("missing %s in %s", want, doc)
		}
	}
	if _, err := buildNIC(NICSpec{IfaceID: "p"}); err == nil {
		t.Error("an OVN port without its MAC")
	}
}
