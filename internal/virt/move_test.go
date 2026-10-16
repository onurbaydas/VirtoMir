package virt

import (
	"strings"
	"testing"
)

func TestRewriteForMove(t *testing.T) {
	doc := string(mustRead(t, "testdata/debian-uefi.xml"))
	out, notes, err := RewriteForMove(doc, map[string]string{
		"/var/lib/libvirt/images/debian-uefi.qcow2": "/srv/pool-b/debian-uefi.qcow2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "/srv/pool-b/debian-uefi.qcow2") || strings.Contains(out, "file=\"/var/lib/libvirt/images/debian-uefi.qcow2\"") {
		t.Errorf("disk not repointed:\n%s", out)
	}
	if strings.Contains(out, "debian-uefi_VARS.fd") || !strings.Contains(out, "OVMF_VARS_4M.ms.fd") {
		t.Errorf("nvram: path must go, template must stay")
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "Ejected") {
		t.Errorf("notes = %q", notes)
	}
	if strings.Count(out, "device=\"cdrom\"") != 1 {
		t.Errorf("the CD-ROM drive must stay (only its media goes)")
	}
}

func TestPoolXML(t *testing.T) {
	x, err := poolXML(PoolSpec{Name: "shared", Path: "/var/lib/virtomir-shared", Type: "netfs", Host: "192.168.77.10", Export: "/srv/vmshare"})
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := x.Marshal()
	for _, want := range []string{`type="netfs"`, `<host name="192.168.77.10">`, `<dir path="/srv/vmshare">`, `<format type="nfs">`, `<path>/var/lib/virtomir-shared</path>`} {
		if !strings.Contains(doc, want) {
			t.Errorf("missing %s in\n%s", want, doc)
		}
	}
	for _, bad := range []PoolSpec{
		{Name: "a", Path: "/x", Type: "netfs", Host: "bad host", Export: "/e"},
		{Name: "a", Path: "/x", Type: "netfs", Host: "nas", Export: "relative"},
		{Name: "a", Path: "rel", Type: "dir"},
		{Name: "a", Path: "/x", Type: "iscsi"},
	} {
		if _, err := poolXML(bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestTuningRoundTrip(t *testing.T) {
	x := fixture(t)
	s := settingsFromXML(x).Settings
	n := int(s.MaxVCPUs)
	s.Pinning = make([]string, n)
	s.Pinning[0] = "2"
	s.EmulatorPin = "0-1"
	s.HugePages = true
	s.NUMAMode, s.NUMANodes = "strict", "0"
	if err := applySettings(x, s); err != nil {
		t.Fatal(err)
	}
	doc, _ := x.Marshal()
	for _, want := range []string{`<vcpupin vcpu="0" cpuset="2">`, `<emulatorpin cpuset="0-1">`, `<hugepages>`, `<memory mode="strict" nodeset="0">`} {
		if !strings.Contains(doc, want) {
			t.Errorf("missing %s", want)
		}
	}
	back := settingsFromXML(x).Tuning
	if back.Pinning[0] != "2" || back.EmulatorPin != "0-1" || !back.HugePages || back.NUMAMode != "strict" {
		t.Errorf("read back %+v", back)
	}
	// Clearing everything removes the elements again.
	s.Pinning, s.EmulatorPin, s.HugePages, s.NUMAMode, s.NUMANodes = make([]string, n), "", false, "", ""
	if err := applySettings(x, s); err != nil {
		t.Fatal(err)
	}
	doc, _ = x.Marshal()
	if strings.Contains(doc, "vcpupin") || strings.Contains(doc, "hugepages") || strings.Contains(doc, "numatune") {
		t.Errorf("not cleared:\n%s", doc)
	}
	s.Pinning[0] = "two"
	if err := applySettings(x, s); err == nil {
		t.Error("bad cpuset accepted")
	}
}

func TestPoolXMLSAN(t *testing.T) {
	x, err := poolXML(PoolSpec{Name: "san", Type: "iscsi", Host: "192.168.77.10", Target: "iqn.2026-10.dev.virtomir:lun"})
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := x.Marshal()
	for _, want := range []string{`type="iscsi"`, `<host name="192.168.77.10">`, `<device path="iqn.2026-10.dev.virtomir:lun">`, `<path>/dev/disk/by-path</path>`} {
		if !strings.Contains(doc, want) {
			t.Errorf("iscsi: missing %s in\n%s", want, doc)
		}
	}
	x, err = poolXML(PoolSpec{Name: "ceph", Type: "rbd", Monitors: []string{"mon1", "10.0.0.2:3300"}, CephPool: "vms"})
	if err != nil {
		t.Fatal(err)
	}
	doc, _ = x.Marshal()
	for _, want := range []string{`type="rbd"`, `<name>vms</name>`, `<host name="mon1" port="6789">`, `<host name="10.0.0.2" port="3300">`} {
		if !strings.Contains(doc, want) {
			t.Errorf("rbd: missing %s in\n%s", want, doc)
		}
	}
	for _, bad := range []PoolSpec{
		{Name: "a", Type: "iscsi", Host: "h", Target: "not-an-iqn"},
		{Name: "a", Type: "rbd", CephPool: "vms"},
		{Name: "a", Type: "rbd", Monitors: []string{"bad host"}, CephPool: "vms"},
	} {
		if _, err := poolXML(bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestPoolXMLLocalBlock(t *testing.T) {
	x, err := poolXML(PoolSpec{Name: "fast", Type: "logical", Source: "vg-fast", Devices: []string{"/dev/sdb", "/dev/disk/by-id/nvme-X_1"}})
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := x.Marshal()
	for _, want := range []string{`type="logical"`, `<name>vg-fast</name>`, `<format type="lvm2">`, `<device path="/dev/sdb">`, `<device path="/dev/disk/by-id/nvme-X_1">`, `<path>/dev/vg-fast</path>`} {
		if !strings.Contains(doc, want) {
			t.Errorf("logical: missing %s in\n%s", want, doc)
		}
	}
	x, err = poolXML(PoolSpec{Name: "tank", Type: "zfs", Source: "tank/vms"})
	if err != nil {
		t.Fatal(err)
	}
	doc, _ = x.Marshal()
	if !strings.Contains(doc, `type="zfs"`) || !strings.Contains(doc, `<name>tank/vms</name>`) || strings.Contains(doc, "<target>") {
		t.Errorf("zfs:\n%s", doc)
	}
	for _, bad := range []PoolSpec{
		{Name: "a", Type: "logical"},
		{Name: "a", Type: "logical", Source: "vg/x"},
		{Name: "a", Type: "logical", Source: ".."},
		{Name: "a", Type: "logical", Source: "vg", Devices: []string{"/etc/passwd"}},
		{Name: "a", Type: "logical", Source: "vg", Devices: []string{"/dev/../etc/shadow"}},
		{Name: "a", Type: "logical", Source: "vg", Devices: []string{"/dev/sdb", "/dev/sdb"}},
		{Name: "a", Type: "zfs", Source: "tank/../x"},
		{Name: "a", Type: "zfs", Source: "tank", Devices: []string{"/dev/sdb"}},
	} {
		if _, err := poolXML(bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestNetworkVLAN(t *testing.T) {
	x, err := buildNetwork(NetworkSpec{Name: "lab10", Mode: "bridge", Bridge: "ovsbr0", OVS: true, VLAN: 10})
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := x.Marshal()
	for _, want := range []string{`<forward mode="bridge">`, `<bridge name="ovsbr0">`, `<virtualport type="openvswitch">`, `<tag id="10">`} {
		if !strings.Contains(doc, want) {
			t.Errorf("missing %s in\n%s", want, doc)
		}
	}
	for _, bad := range []NetworkSpec{
		{Name: "a", Mode: "bridge", Bridge: "br0", VLAN: 10},              // Linux bridge + tag
		{Name: "a", Mode: "bridge", Bridge: "br0", OVS: true, VLAN: 5000}, // out of range
		{Name: "a", Mode: "nat", Subnet: "192.168.50.0/24", VLAN: 10},     // NAT + tag
	} {
		if _, err := buildNetwork(bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	nic, err := buildNIC(NICSpec{Network: "lab10", VLAN: 20})
	if err != nil || nic.VLan == nil || nic.VLan.Tags[0].ID != 20 {
		t.Errorf("nic vlan: %+v %v", nic.VLan, err)
	}
	if _, err := buildNIC(NICSpec{Network: "lab10", VLAN: 4095}); err == nil {
		t.Error("vlan 4095 accepted")
	}
}
