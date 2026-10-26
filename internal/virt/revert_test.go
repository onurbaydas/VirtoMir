package virt

import (
	"strings"
	"testing"

	"libvirt.org/go/libvirtxml"
)

func TestRevertBlockers(t *testing.T) {
	file := func(p string) *libvirtxml.DomainDiskSource {
		return &libvirtxml.DomainDiskSource{File: &libvirtxml.DomainDiskSourceFile{File: p}}
	}
	x := &libvirtxml.Domain{Devices: &libvirtxml.DomainDeviceList{Disks: []libvirtxml.DomainDisk{
		{Device: "disk", Source: file("/img/base.qcow2"), Target: &libvirtxml.DomainDiskTarget{Dev: "vda"}},
		{Device: "cdrom", Source: file("/iso/x.iso"), Target: &libvirtxml.DomainDiskTarget{Dev: "sda"}, ReadOnly: &libvirtxml.DomainDiskReadOnly{}},
	}}}
	free := map[string][]string{"/img/base.qcow2": {"tpl", "tpl (as backing image)"}, "/iso/x.iso": {"tpl", "other"}}
	if err := revertBlockers(x, "tpl", free); err != nil {
		t.Fatalf("own backing chain and shared ISO must not block: %v", err)
	}
	linked := map[string][]string{"/img/base.qcow2": {"tpl", "clone1 (as backing image)"}}
	err := revertBlockers(x, "tpl", linked)
	if err == nil || !strings.Contains(err.Error(), "clone1 builds on its disk") {
		t.Fatalf("err = %v", err)
	}
}
