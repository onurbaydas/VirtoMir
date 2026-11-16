package virt

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"libvirt.org/go/libvirtxml"
)

// writeQcow2 writes a qcow2 header (version 3) with a backing image and its recorded format.
func writeQcow2(t *testing.T, path, backing, format string) {
	t.Helper()
	b := make([]byte, 4096)
	binary.BigEndian.PutUint32(b[0:], qcow2HeaderMagic)
	binary.BigEndian.PutUint32(b[4:], 3)
	binary.BigEndian.PutUint32(b[100:], 104)
	pos := 104
	// An extension this code does not know comes first: it is skipped.
	binary.BigEndian.PutUint32(b[pos:], 0x6803f857)
	binary.BigEndian.PutUint32(b[pos+4:], 5)
	pos += 8 + 8
	if format != "" {
		binary.BigEndian.PutUint32(b[pos:], qcow2ExtBackingFormat)
		binary.BigEndian.PutUint32(b[pos+4:], uint32(len(format)))
		copy(b[pos+8:], format)
		pos += 8 + (len(format)+7)&^7
	}
	pos += 8 // the end marker
	if backing != "" {
		binary.BigEndian.PutUint64(b[8:], uint64(pos))
		binary.BigEndian.PutUint32(b[16:], uint32(len(backing)))
		copy(b[pos:], backing)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func qcow2Disk(path string) libvirtxml.DomainDisk {
	return libvirtxml.DomainDisk{
		Driver: &libvirtxml.DomainDiskDriver{Type: "qcow2"},
		Source: &libvirtxml.DomainDiskSource{File: &libvirtxml.DomainDiskSourceFile{File: path}},
	}
}

// A stopped machine's overlay names its whole chain, though libvirt reports none of it.
func TestBackingImagesOfAStoppedMachine(t *testing.T) {
	dir := t.TempDir()
	base, mid, top := filepath.Join(dir, "base.qcow2"), filepath.Join(dir, "mid.qcow2"), filepath.Join(dir, "top.qcow2")
	writeQcow2(t, base, "", "")
	writeQcow2(t, mid, "base.qcow2", "qcow2") // relative: next to the overlay
	writeQcow2(t, top, mid, "qcow2")
	if got := backingImages(qcow2Disk(top)); !slices.Equal(got, []string{mid, base}) {
		t.Errorf("chain = %v", got)
	}
	if got := backingImages(qcow2Disk(base)); len(got) != 0 {
		t.Errorf("an image without a base has %v below it", got)
	}
}

// A raw image belongs to its guest: one that looks like an overlay is not believed, neither
// as a machine's disk nor as the base of an overlay.
func TestBackingImagesNeverReadRawImages(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "guest.raw")
	writeQcow2(t, fake, "/etc/shadow", "raw")
	raw := qcow2Disk(fake)
	raw.Driver.Type = "raw"
	if got := backingImages(raw); len(got) != 0 {
		t.Errorf("a raw disk was read as an overlay: %v", got)
	}
	raw.Driver = nil // libvirt's default is raw
	if got := backingImages(raw); len(got) != 0 {
		t.Errorf("a disk without a format was read as an overlay: %v", got)
	}
	for _, format := range []string{"raw", ""} {
		top := filepath.Join(dir, "top-"+format+".qcow2")
		writeQcow2(t, top, fake, format)
		if got := backingImages(qcow2Disk(top)); !slices.Equal(got, []string{fake}) {
			t.Errorf("backing format %q: chain = %v", format, got)
		}
	}
}

func TestBackingImagesOddHeaders(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.qcow2"), filepath.Join(dir, "b.qcow2")
	writeQcow2(t, a, b, "qcow2")
	writeQcow2(t, b, a, "qcow2") // a loop ends
	if got := backingImages(qcow2Disk(a)); !slices.Equal(got, []string{b}) {
		t.Errorf("loop: %v", got)
	}
	remote := filepath.Join(dir, "remote.qcow2")
	writeQcow2(t, remote, "nbd://10.0.0.1/export", "raw")
	if got := backingImages(qcow2Disk(remote)); len(got) != 0 {
		t.Errorf("a network base was taken for a file: %v", got)
	}
	short := filepath.Join(dir, "short.qcow2")
	if err := os.WriteFile(short, []byte("QFI\xfb"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{short, filepath.Join(dir, "missing.qcow2"), dir} {
		if got := backingImages(qcow2Disk(p)); len(got) != 0 {
			t.Errorf("%s: %v", p, got)
		}
	}
}

// For a running machine libvirt reports the chain; all of it counts, not only its first image.
func TestBackingImagesOfARunningMachine(t *testing.T) {
	file := func(p string) *libvirtxml.DomainDiskSource {
		return &libvirtxml.DomainDiskSource{File: &libvirtxml.DomainDiskSourceFile{File: p}}
	}
	d := libvirtxml.DomainDisk{
		Driver: &libvirtxml.DomainDiskDriver{Type: "qcow2"},
		Source: file("/nowhere/top.qcow2"),
		BackingStore: &libvirtxml.DomainDiskBackingStore{Source: file("/nowhere/mid.qcow2"),
			BackingStore: &libvirtxml.DomainDiskBackingStore{Source: file("/nowhere/base.qcow2"),
				BackingStore: &libvirtxml.DomainDiskBackingStore{}}},
	}
	if got := backingImages(d); !slices.Equal(got, []string{"/nowhere/mid.qcow2", "/nowhere/base.qcow2"}) {
		t.Errorf("chain = %v", got)
	}
}
