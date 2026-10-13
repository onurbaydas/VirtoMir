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
