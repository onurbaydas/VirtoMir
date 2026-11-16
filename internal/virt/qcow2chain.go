package virt

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"

	"libvirt.org/go/libvirtxml"
)

// libvirt only reports the images below a disk (<backingStore>) while its machine runs. For
// a stopped machine the chain is read here, from the qcow2 headers, so the base image of an
// overlay counts as used whatever the machine's state: deleting, renaming or resizing it
// would destroy every overlay on top.
//
// Only headers are read, and only of images that are qcow2 on record: the disk's own format
// comes from the machine's definition, the format of each backing image from the header of
// the overlay above it. A raw image is never looked into: its first bytes belong to the
// guest (see imagefmt.go).

const (
	qcow2HeaderMagic      = 0x514649fb // "QFI\xfb"
	qcow2ExtBackingFormat = 0xe2792aca // header extension: the backing image's format
	qcow2MaxChain         = 32
)

// qcow2Backing reads the backing image of the qcow2 file at path from its header: its path
// (absolute; "" for none, or one that is not a local file) and its recorded format ("" when
// the header does not say).
func qcow2Backing(path string) (backing, format string) {
	st, err := os.Stat(path)
	if err != nil || (!st.Mode().IsRegular() && st.Mode()&os.ModeDevice == 0) {
		return "", ""
	}
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()

	hdr := make([]byte, 104)
	if n, _ := f.ReadAt(hdr, 0); n < 72 {
		return "", ""
	} else if n < len(hdr) {
		clear(hdr[n:])
	}
	if binary.BigEndian.Uint32(hdr[0:]) != qcow2HeaderMagic {
		return "", ""
	}
	version := binary.BigEndian.Uint32(hdr[4:])
	off := binary.BigEndian.Uint64(hdr[8:])
	size := binary.BigEndian.Uint32(hdr[16:])
	if off == 0 || size == 0 || size > 1023 || off > 1<<40 {
		return "", ""
	}
	name := make([]byte, size)
	if n, _ := f.ReadAt(name, int64(off)); n != len(name) {
		return "", ""
	}
	backing = string(name)
	if strings.HasPrefix(backing, "json:") || strings.Contains(backing, "://") || strings.ContainsRune(backing, 0) {
		return "", "" // not a file of this host
	}
	if !filepath.IsAbs(backing) {
		backing = filepath.Join(filepath.Dir(path), backing)
	}
	backing = filepath.Clean(backing)

	// Header extensions follow the header: type, length, data padded to 8 bytes.
	pos := int64(72)
	if version >= 3 {
		if hl := binary.BigEndian.Uint32(hdr[100:]); hl >= 104 && hl <= 1<<16 {
			pos = int64(hl)
		}
	}
	ext := make([]byte, 8)
	for i := 0; i < 64; i++ {
		if n, _ := f.ReadAt(ext, pos); n != len(ext) {
			break
		}
		typ, l := binary.BigEndian.Uint32(ext[0:]), binary.BigEndian.Uint32(ext[4:])
		if typ == 0 || l > 1<<16 {
			break
		}
		if typ == qcow2ExtBackingFormat {
			data := make([]byte, l)
			if n, _ := f.ReadAt(data, pos+8); n == len(data) {
				format = string(data)
			}
			break
		}
		pos += 8 + int64((l+7)&^7)
	}
	return backing, format
}

// qcow2Chain lists the images below the qcow2 file at path, nearest first. It stops at an
// image that is not qcow2 on record.
func qcow2Chain(path string) []string {
	var out []string
	seen := map[string]bool{path: true}
	for cur := path; len(out) < qcow2MaxChain; {
		backing, format := qcow2Backing(cur)
		if backing == "" || seen[backing] {
			break
		}
		out = append(out, backing)
		seen[backing] = true
		if format != "qcow2" {
			break
		}
		cur = backing
	}
	return out
}

// backingImages lists every image below a disk: what libvirt reports for a running machine
// (the whole chain, not only its first image) and what the qcow2 headers say.
func backingImages(v libvirtxml.DomainDisk) []string {
	var out []string
	for b := v.BackingStore; b != nil; b = b.BackingStore {
		if src, _ := diskSource(b.Source); src != "" {
			out = appendUnique(out, src)
		}
	}
	if v.Source != nil && diskFormat(v) == "qcow2" {
		var path string
		switch {
		case v.Source.File != nil:
			path = v.Source.File.File
		case v.Source.Block != nil:
			path = v.Source.Block.Dev
		}
		if filepath.IsAbs(path) {
			for _, b := range qcow2Chain(path) {
				out = appendUnique(out, b)
			}
		}
	}
	return out
}
