package virt

import (
	"encoding/xml"
	"strings"
	"testing"

	"libvirt.org/go/libvirtxml"
)

func TestMetaRoundTrip(t *testing.T) {
	b, err := xml.Marshal(vmMeta{Template: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != `<vm template="true"></vm>` {
		t.Fatalf("marshal = %s", got)
	}
	// libvirt hands metadata back with its namespace prefix.
	var m vmMeta
	raw := `<virtomir:vm xmlns:virtomir="` + metaURI + `" linked-from="0e1d-aa"/>`
	if err := xml.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	if m.Template || m.LinkedFrom != "0e1d-aa" {
		t.Fatalf("unmarshal = %+v", m)
	}
	if !(vmMeta{}).empty() || m.empty() {
		t.Fatal("empty() is wrong")
	}
}

func TestOverlayXML(t *testing.T) {
	doc, err := overlayXML("web1-vda.qcow2", "/var/lib/libvirt/images/base.qcow2", "qcow2", 10<<30)
	if err != nil {
		t.Fatal(err)
	}
	var v libvirtxml.StorageVolume
	if err := v.Unmarshal(doc); err != nil {
		t.Fatal(err)
	}
	if v.Target.Format.Type != "qcow2" || v.BackingStore.Path != "/var/lib/libvirt/images/base.qcow2" ||
		v.BackingStore.Format.Type != "qcow2" || v.Capacity.Value != 10<<30 {
		t.Fatalf("overlay = %s", doc)
	}
	doc, _ = overlayXML("x.qcow2", "/b.img", "", 1)
	if !strings.Contains(doc, `<format type="raw">`) {
		t.Fatalf("unknown base format should default to raw: %s", doc)
	}
}

func TestTemplateDisksAndSeed(t *testing.T) {
	x := &libvirtxml.Domain{Devices: &libvirtxml.DomainDeviceList{Disks: []libvirtxml.DomainDisk{
		{Device: "disk", Target: &libvirtxml.DomainDiskTarget{Dev: "vda"}},
		{Device: "cdrom", Target: &libvirtxml.DomainDiskTarget{Dev: "sda"}, Source: &libvirtxml.DomainDiskSource{File: &libvirtxml.DomainDiskSourceFile{File: "/i/tpl-cidata.iso"}}},
		{Device: "disk", Target: &libvirtxml.DomainDiskTarget{Dev: "vdb"}, ReadOnly: &libvirtxml.DomainDiskReadOnly{}},
		{Target: &libvirtxml.DomainDiskTarget{Dev: "vdc"}},
	}}}
	var got []string
	for _, d := range templateDisks(x) {
		got = append(got, d.Target.Dev)
	}
	if strings.Join(got, ",") != "vda,vdc" {
		t.Fatalf("templateDisks = %v", got)
	}

	// The template's own seed is replaced, not duplicated.
	setSeed(x, "/i/web1-cidata.iso")
	if len(x.Devices.Disks) != 4 || x.Devices.Disks[1].Source.File.File != "/i/web1-cidata.iso" {
		t.Fatalf("seed not replaced: %+v", x.Devices.Disks[1].Source.File)
	}
	// Without one, a CD-ROM is added.
	y := &libvirtxml.Domain{}
	setSeed(y, "/i/s.iso")
	if len(y.Devices.Disks) != 1 || y.Devices.Disks[0].Device != "cdrom" || y.Devices.Disks[0].Target.Bus != "sata" {
		t.Fatalf("seed not added: %+v", y.Devices.Disks)
	}

	if d := qcow2Driver(&libvirtxml.DomainDiskDriver{Name: "qemu", Type: "raw", Cache: "none"}); d.Type != "qcow2" || d.Cache != "none" {
		t.Fatalf("qcow2Driver = %+v", d)
	}
}
