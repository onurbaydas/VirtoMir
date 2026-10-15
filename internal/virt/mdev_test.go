package virt

import (
	"testing"

	"libvirt.org/go/libvirtxml"
)

func vgpuCard(t *testing.T) HostPCI {
	t.Helper()
	var nd libvirtxml.NodeDevice
	if err := nd.Unmarshal(string(mustRead(t, "testdata/nodedev-vgpu.xml"))); err != nil {
		t.Fatal(err)
	}
	return HostPCI{Name: nd.Name, Address: pciAddress(nd.Capability.PCI), MDevTypes: mdevTypes(nd.Capability.PCI)}
}

func TestMDevTypes(t *testing.T) {
	p := vgpuCard(t)
	if p.Address != "0000:3b:00.0" {
		t.Errorf("address = %s", p.Address)
	}
	want := []HostMDevType{{"nvidia-63", "GRID P4-1Q", "vfio-pci", 8}, {"nvidia-65", "GRID P4-4Q", "vfio-pci", 0}}
	if len(p.MDevTypes) != 2 || p.MDevTypes[0] != want[0] || p.MDevTypes[1] != want[1] {
		t.Errorf("types = %+v", p.MDevTypes)
	}
	if n := mdevTypeName([]HostPCI{p}, "0000:3b:00.0", "nvidia-65"); n != "GRID P4-4Q" {
		t.Errorf("type name = %q", n)
	}
}

func TestCheckMDevCreate(t *testing.T) {
	pci := []HostPCI{vgpuCard(t), {Name: "pci_0000_00_1f_2", Address: "0000:00:1f.2"}}
	if p, err := checkMDevCreate(pci, MDevSpec{Parent: "0000:3B:00.0", Type: "nvidia-63"}); err != nil || p.Name != "pci_0000_3b_00_0" {
		t.Errorf("ok case: %v, %v", p.Name, err)
	}
	for _, c := range []struct {
		spec    MDevSpec
		refused bool
	}{
		{MDevSpec{Parent: "0000:99:00.0", Type: "nvidia-63"}, false}, // no such card
		{MDevSpec{Parent: "0000:00:1f.2", Type: "nvidia-63"}, false}, // card without types
		{MDevSpec{Parent: "0000:3b:00.0", Type: "nvidia-99"}, false}, // type not offered
		{MDevSpec{Parent: "0000:3b:00.0", Type: "nvidia-65"}, true},  // none left
	} {
		_, err := checkMDevCreate(pci, c.spec)
		if c.refused && !isRefused(err) || !c.refused && !isInvalid(err) {
			t.Errorf("%+v: %v", c.spec, err)
		}
	}
}

func TestMDevXML(t *testing.T) {
	id := newUUID()
	if !uuidRe.MatchString(id) || id[14] != '4' {
		t.Fatalf("uuid = %s", id)
	}
	doc, err := mdevXML("pci_0000_3b_00_0", "nvidia-63", id)
	if err != nil {
		t.Fatal(err)
	}
	var nd libvirtxml.NodeDevice
	if err := nd.Unmarshal(doc); err != nil {
		t.Fatal(err)
	}
	m := nd.Capability.MDev
	if nd.Parent != "pci_0000_3b_00_0" || m == nil || m.Type.ID != "nvidia-63" || m.UUID != id {
		t.Errorf("round trip: %s", doc)
	}
}

func TestNodeNameAddress(t *testing.T) {
	for in, want := range map[string]string{
		"pci_0000_3b_00_0": "0000:3b:00.0",
		"css_0_0_0023":     "",
		"pci_0000_3b":      "",
	} {
		if got := nodeNameAddress(in); got != want {
			t.Errorf("%s = %q, want %q", in, got, want)
		}
	}
}
