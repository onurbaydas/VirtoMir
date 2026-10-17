package virt

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"libvirt.org/go/libvirtxml"
)

func TestDiskFormat(t *testing.T) {
	if got := diskFormat(libvirtxml.DomainDisk{}); got != "raw" {
		t.Errorf("no driver: %q, want raw (libvirt's default)", got)
	}
	d := libvirtxml.DomainDisk{Driver: &libvirtxml.DomainDiskDriver{Type: "qcow2"}}
	if got := diskFormat(d); got != "qcow2" {
		t.Errorf("qcow2 driver: %q", got)
	}
}

func TestConvertArgsNameTheFormat(t *testing.T) {
	got := strings.Join(convertArgs("raw", "/a", "/b", true), " ")
	if got != "convert -f raw -O qcow2 -c /a /b" {
		t.Errorf("args: %s", got)
	}
}

func TestCheckChain(t *testing.T) {
	parse := func(s string) []imageInfo {
		t.Helper()
		c, err := parseImageInfo([]byte(s))
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	overlay := `[{"filename":"/p/vm.qcow2","format":"qcow2","backing-filename":"/p/base.raw","backing-filename-format":"raw"},
	             {"filename":"/p/base.raw","format":"raw"}]`
	noFormat := `[{"filename":"/p/vm.qcow2","format":"qcow2","backing-filename":"/etc/shadow"}]`
	plain := `{"filename":"/p/vm.qcow2","format":"qcow2"}`
	vmdkOther := `[{"filename":"/p/d.vmdk","format":"vmdk","format-specific":{"type":"vmdk","data":{"create-type":"monolithicFlat","extents":[{"filename":"/etc/shadow"}]}}}]`
	vmdkSelf := `[{"filename":"/p/d.vmdk","format":"vmdk","format-specific":{"type":"vmdk","data":{"create-type":"streamOptimized","extents":[{"filename":"/p/d.vmdk"}]}}}]`

	cases := []struct {
		name    string
		chain   string
		backing bool
		ok      bool
	}{
		{"overlay with recorded format", overlay, true, true},
		{"overlay where none is allowed", overlay, false, false},
		{"backing format not recorded", noFormat, true, false},
		{"standalone image", plain, false, true},
		{"vmdk reading another file", vmdkOther, true, false},
		{"single-file vmdk", vmdkSelf, false, true},
	}
	for _, c := range cases {
		err := checkChain("/p/x", parse(c.chain), c.backing)
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok=%v", c.name, err, c.ok)
		}
	}
	if err := checkChain("/p/x", nil, true); err == nil {
		t.Error("an empty answer must not pass")
	}
}

func TestSniffFormat(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for name, c := range map[string]struct {
		data []byte
		want string
	}{
		"q.img":     {[]byte("QFI\xfb\x00\x00\x00\x03rest"), "qcow2"},
		"s.vmdk":    {[]byte("KDMV\x01\x00\x00\x00"), "vmdk"},
		"desc.vmdk": {[]byte("# Disk DescriptorFile\nRW 8 FLAT \"/etc/shadow\" 0\n"), "raw"},
		"tiny":      {[]byte("ab"), "raw"},
	} {
		got, err := sniffFormat(write(name, c.data))
		if err != nil || got != c.want {
			t.Errorf("%s: %q, %v; want %q", name, got, err, c.want)
		}
	}
}

// TestRawDiskCannotSmuggleHostFiles is the attack itself, against the real qemu-img: a guest
// writes a qcow2 header with a backing file into its raw disk. Copying that disk must copy
// the bytes, not the host file the header points at.
func TestRawDiskCannotSmuggleHostFiles(t *testing.T) {
	if _, err := exec.LookPath("qemu-img"); err != nil {
		t.Skip("qemu-img not installed")
	}
	dir := t.TempDir()
	secret := filepath.Join(dir, "host-secret")
	marker := []byte("HOST-SECRET-" + strings.Repeat("x", 4000))
	if err := os.WriteFile(secret, marker, 0o600); err != nil {
		t.Fatal(err)
	}
	// What the guest writes: a qcow2 header pointing at the host file, on a "raw" disk.
	disk := filepath.Join(dir, "guest-disk.raw")
	run := func(args ...string) []byte {
		t.Helper()
		out, err := exec.Command("qemu-img", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("qemu-img %v: %v: %s", args, err, out)
		}
		return out
	}
	run("create", "-q", "-f", "qcow2", "-b", secret, "-F", "raw", disk, "1M")

	ctx := context.Background()
	if _, err := checkedImage(ctx, disk, "raw", true); err != nil {
		t.Fatalf("a raw disk has no backing chain: %v", err)
	}
	out := filepath.Join(dir, "backup.qcow2")
	run(convertArgs("raw", disk, out, true)...)
	back := filepath.Join(dir, "backup.raw")
	run("convert", "-q", "-f", "qcow2", "-O", "raw", out, back)
	data, err := os.ReadFile(back)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("HOST-SECRET")) {
		t.Fatal("the copy contains the host file: the disk was probed instead of read as raw")
	}

	// The same file opened as qcow2 (what probing would do) has a backing file and is refused
	// where none is allowed, e.g. a backup being restored.
	if _, err := checkedImage(ctx, disk, "qcow2", false); err == nil {
		t.Error("an image with a backing file passed where none is allowed")
	}
	if got := backingFile(disk, "raw"); got != "" {
		t.Errorf("backingFile as raw = %q, want none", got)
	}
}

// TestVMDKDescriptorCannotReadHostFiles: a text VMDK whose extent is a host file is read as
// raw by sniffFormat, and refused if anyone opens it as vmdk.
func TestVMDKDescriptorCannotReadHostFiles(t *testing.T) {
	if _, err := exec.LookPath("qemu-img"); err != nil {
		t.Skip("qemu-img not installed")
	}
	dir := t.TempDir()
	secret := filepath.Join(dir, "host-secret")
	if err := os.WriteFile(secret, bytes.Repeat([]byte("S"), 512*8), 0o600); err != nil {
		t.Fatal(err)
	}
	desc := filepath.Join(dir, "disk.vmdk")
	d := "# Disk DescriptorFile\nversion=1\nCID=fffffffe\nparentCID=ffffffff\ncreateType=\"monolithicFlat\"\n" +
		"RW 8 FLAT \"" + secret + "\" 0\n\nddb.virtualHWVersion = \"4\"\n"
	if err := os.WriteFile(desc, []byte(d), 0o600); err != nil {
		t.Fatal(err)
	}
	if f, _ := sniffFormat(desc); f != "raw" {
		t.Errorf("a VMDK descriptor sniffed as %q, want raw", f)
	}
	if _, err := checkedImage(context.Background(), desc, "vmdk", true); err == nil {
		t.Error("a VMDK reading another file passed the check")
	}
}
