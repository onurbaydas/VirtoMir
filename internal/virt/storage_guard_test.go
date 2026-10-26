package virt

import (
	"slices"
	"testing"
)

func TestCheckPoolPath(t *testing.T) {
	protected := []string{"/var/lib/virtomir", "/mnt/backups/"}
	for _, ok := range []struct{ in, want string }{
		{"/srv/vm-images", "/srv/vm-images"},
		{"/srv/vm-images/", "/srv/vm-images"},
		{"/home/alice/vms", "/home/alice/vms"},
		{"/var/lib/libvirt/images", "/var/lib/libvirt/images"},
		{"/var/lib/virtomir-shared", "/var/lib/virtomir-shared"},
		{"//data//vms", "/data/vms"},
	} {
		got, err := checkPoolPath(ok.in, protected)
		if err != nil || got != ok.want {
			t.Errorf("%s: %q, %v", ok.in, got, err)
		}
	}
	for _, bad := range []string{
		"", "rel/dir", "/", "//", "/etc", "/etc/", "/etc/libvirt", "/usr", "/usr/local/vms", "/bin", "/sbin",
		"/lib", "/lib64/x", "/boot", "/dev", "/dev/shm", "/proc", "/sys/fs", "/run", "/run/vms", "/root", "/home",
		"/var", "/var/lib", "/srv/../etc", "/var/lib/virtomir", "/var/lib/virtomir/backups", "/mnt/backups",
		"/mnt/backups/vm1",
	} {
		if got, err := checkPoolPath(bad, protected); err == nil {
			t.Errorf("accepted %q (as %q)", bad, got)
		}
	}
	if _, err := poolXML(PoolSpec{Name: "p", Path: "/var/lib/virtomir/x"}, protected...); err == nil {
		t.Error("poolXML accepted a pool in the state directory")
	}
	x, err := poolXML(PoolSpec{Name: "p", Path: "/srv/vms/"})
	if err != nil || x.Target.Path != "/srv/vms" {
		t.Errorf("poolXML: %v, %+v", err, x.Target)
	}
}

func TestBackedBy(t *testing.T) {
	descs := map[string]string{
		"overlay.qcow2": `<volume><name>overlay.qcow2</name><target><path>/pool/overlay.qcow2</path></target>
			<backingStore><path>/pool/base.qcow2</path><format type="qcow2"/></backingStore></volume>`,
		"rel.qcow2": `<volume><name>rel.qcow2</name><target><path>/pool/rel.qcow2</path></target>
			<backingStore><path>base.qcow2</path></backingStore></volume>`,
		"other.qcow2": `<volume><name>other.qcow2</name><target><path>/pool/other.qcow2</path></target>
			<backingStore><path>/pool/else.qcow2</path></backingStore></volume>`,
		"plain.raw": `<volume><name>plain.raw</name><target><path>/pool/plain.raw</path></target></volume>`,
	}
	got, err := backedBy("/pool/./base.qcow2", descs)
	if err != nil || !slices.Equal(got, []string{"overlay.qcow2", "rel.qcow2"}) {
		t.Fatalf("got %v, %v", got, err)
	}
	if got, err := backedBy("/pool/plain.raw", descs); err != nil || len(got) != 0 {
		t.Fatalf("plain: %v, %v", got, err)
	}
	descs["broken"] = "<volume"
	if _, err := backedBy("/pool/base.qcow2", descs); err == nil {
		t.Fatal("an unreadable volume must make it an error (fail closed)")
	}
}
