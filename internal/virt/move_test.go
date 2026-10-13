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
