package virt

import (
	"encoding/xml"
	"strings"
	"testing"

	"libvirt.org/go/libvirtxml"
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

// A machine's project lives in its <metadata>, which is part of the domain XML, so it
// travels with the definition: a move turns the XML into a plan and adopts it on the
// target, and a live migration carries the same definition through libvirt. The project
// must come out of that round-trip (ADR-0060).
func TestProjectSurvivesTheMoveXML(t *testing.T) {
	doc := string(mustRead(t, "testdata/debian-uefi.xml"))
	// Where libvirt puts custom metadata, as DomainGetXMLDesc hands it back.
	doc = strings.Replace(doc, "</description>",
		"</description>\n  <metadata><virtomir:vm xmlns:virtomir=\""+metaURI+"\" owner=\"op\" project=\"dev\"/></metadata>", 1)

	// PlanMove parses the definition and marshals it into the plan's XML.
	var plan libvirtxml.Domain
	if err := plan.Unmarshal(doc); err != nil {
		t.Fatal(err)
	}
	planXML, err := plan.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	// The target host adopts the definition after RewriteForMove pointed it at the
	// copied disks, which parses and marshals it once more.
	moved, _, err := RewriteForMove(planXML, map[string]string{
		"/var/lib/libvirt/images/debian-uefi.qcow2": "/srv/pool-b/debian-uefi.qcow2",
	})
	if err != nil {
		t.Fatal(err)
	}
	var there libvirtxml.Domain
	if err := there.Unmarshal(moved); err != nil {
		t.Fatal(err)
	}
	if there.Metadata == nil {
		t.Fatalf("the metadata element was dropped:\n%s", moved)
	}
	// readMeta reads this same element out of DomainGetMetadata's answer.
	var m vmMeta
	if err := xml.Unmarshal([]byte(there.Metadata.XML), &m); err != nil {
		t.Fatal(err)
	}
	if m.Owner != "op" || m.Project != "dev" {
		t.Fatalf("the project did not travel: %+v (metadata: %s)", m, there.Metadata.XML)
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

func TestCPULimitsAndNested(t *testing.T) {
	x := fixture(t)
	s := settingsFromXML(x).Settings
	s.CPUWeight, s.CPULimit, s.Nested = 2048, 50, "on"
	if err := applySettingsFor(x, s, "vmx"); err != nil {
		t.Fatal(err)
	}
	doc, _ := x.Marshal()
	for _, want := range []string{`<shares>2048</shares>`, `<period>100000</period>`, `<quota>50000</quota>`, `<feature policy="require" name="vmx">`} {
		if !strings.Contains(doc, want) {
			t.Errorf("missing %s in\n%s", want, doc)
		}
	}
	back := settingsFromXML(x).Settings
	if back.CPUWeight != 2048 || back.CPULimit != 50 || back.Nested != "on" {
		t.Errorf("read back weight %d limit %d nested %q", back.CPUWeight, back.CPULimit, back.Nested)
	}

	s.Nested = "off"
	if err := applySettingsFor(x, s, "vmx"); err != nil {
		t.Fatal(err)
	}
	if doc, _ = x.Marshal(); !strings.Contains(doc, `<feature policy="disable" name="vmx">`) || strings.Count(doc, `name="vmx"`) != 1 {
		t.Errorf("nested off:\n%s", doc)
	}

	s.CPUWeight, s.CPULimit, s.Nested = 0, 0, ""
	if err := applySettingsFor(x, s, "vmx"); err != nil {
		t.Fatal(err)
	}
	if doc, _ = x.Marshal(); strings.Contains(doc, "<shares>") || strings.Contains(doc, "<quota>") || strings.Contains(doc, `name="vmx"`) {
		t.Errorf("not cleared:\n%s", doc)
	}

	for _, bad := range []Settings{{CPULimit: 101}, {CPUWeight: 1}, {Nested: "maybe"}} {
		b := settingsFromXML(x).Settings
		b.CPUWeight, b.CPULimit, b.Nested = bad.CPUWeight, bad.CPULimit, bad.Nested
		if err := applySettingsFor(x, b, "vmx"); err == nil {
			t.Errorf("accepted weight %d limit %d nested %q", bad.CPUWeight, bad.CPULimit, bad.Nested)
		}
	}
	b := settingsFromXML(x).Settings
	b.Nested = "on"
	if err := applySettingsFor(x, b, ""); err == nil {
		t.Error("nested virtualization accepted on a host without VT-x/AMD-V")
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

func TestNetworkIPv6(t *testing.T) {
	x, err := buildNetwork(NetworkSpec{Name: "dual", Mode: "nat", Subnet: "192.168.140.0/24", DHCP: true, Subnet6: "fd12:3456:789a:1::/64", DHCP6: true})
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := x.Marshal()
	for _, want := range []string{`<nat ipv6="yes">`, `address="fd12:3456:789a:1::1" family="ipv6" prefix="64"`, `<range start="fd12:3456:789a:1::100" end="fd12:3456:789a:1::ffff">`, `address="192.168.140.1"`} {
		if !strings.Contains(doc, want) {
			t.Errorf("missing %s in\n%s", want, doc)
		}
	}
	v, err := parseNetwork(doc)
	if err != nil {
		t.Fatal(err)
	}
	if v.Subnet != "192.168.140.1/24" || v.Subnet6 != "fd12:3456:789a:1::1/64" || v.DHCP6Start != "fd12:3456:789a:1::100" {
		t.Errorf("read back %+v", v)
	}
	// IPv6 only, routed, SLAAC only.
	x, err = buildNetwork(NetworkSpec{Name: "v6", Mode: "route", Subnet6: "2001:db8:5::/64"})
	if err != nil {
		t.Fatal(err)
	}
	doc, _ = x.Marshal()
	if strings.Contains(doc, "<dhcp>") || strings.Contains(doc, "<nat") || strings.Count(doc, "<ip ") != 1 {
		t.Errorf("v6 only:\n%s", doc)
	}
	for _, bad := range []NetworkSpec{
		{Name: "a", Mode: "nat"},
		{Name: "a", Mode: "nat", Subnet6: "fd00::/48"},
		{Name: "a", Mode: "nat", Subnet6: "fe80::/64"},
		{Name: "a", Mode: "nat", Subnet6: "10.0.0.0/24"},
		{Name: "a", Mode: "nat", Subnet6: "fd00::/64", DHCP: true},
		{Name: "a", Mode: "nat", Subnet: "10.9.0.0/24", DHCP6: true},
	} {
		if _, err := buildNetwork(bad); err == nil {
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
