package virt

// Disk images are never opened by qemu-img with format probing. Probing reads the format from
// the image's first bytes, and those can be controlled by a guest (a raw disk) or by whoever
// made an imported file (an OVA): a raw disk whose first sector looks like a qcow2 header with
// backing file /etc/shadow would make qemu-img, running as root here, read that host file
// into a backup or a converted image. So every qemu-img call names the format it expects, and
// formats that can refer to other files are checked before their data is read.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"libvirt.org/go/libvirtxml"
)

// diskFormat is the image format libvirt opens a disk with: its <driver type>, or raw, which
// is libvirt's default when none is given.
func diskFormat(d libvirtxml.DomainDisk) string {
	if d.Driver != nil && d.Driver.Type != "" {
		return d.Driver.Type
	}
	return "raw"
}

// imageInfo is what `qemu-img info --output=json` says about one image of a chain.
type imageInfo struct {
	Filename      string `json:"filename"`
	Format        string `json:"format"`
	ActualSize    uint64 `json:"actual-size"` // bytes the image takes on the host
	VirtualSize   uint64 `json:"virtual-size"` // the disk's size as the guest sees it
	Backing       string `json:"backing-filename"`
	BackingFull   string `json:"full-backing-filename"`
	BackingFormat string `json:"backing-filename-format"`
	Specific      *struct {
		Type string `json:"type"`
		Data struct {
			CreateType string `json:"create-type"`
			Extents    []struct {
				Filename string `json:"filename"`
			} `json:"extents"`
		} `json:"data"`
	} `json:"format-specific"`
}

// qemuImgInfo reads an image and its backing chain, opened as format (never probed).
var qemuImgInfo = func(ctx context.Context, path, format string) ([]imageInfo, error) {
	out, err := exec.CommandContext(ctx, "qemu-img", "info", "--output=json", "--backing-chain", "-U", "-f", format, path).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil, fmt.Errorf("qemu-img info %s: %s", filepath.Base(path), strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, err
	}
	return parseImageInfo(out)
}

// parseImageInfo takes `qemu-img info --backing-chain` output: an array, or one object.
func parseImageInfo(out []byte) ([]imageInfo, error) {
	out = bytes.TrimSpace(out)
	var chain []imageInfo
	if len(out) > 0 && out[0] == '{' {
		var one imageInfo
		if err := json.Unmarshal(out, &one); err != nil {
			return nil, fmt.Errorf("read qemu-img info: %w", err)
		}
		return []imageInfo{one}, nil
	}
	if err := json.Unmarshal(out, &chain); err != nil {
		return nil, fmt.Errorf("read qemu-img info: %w", err)
	}
	return chain, nil
}

// checkChain refuses images that would make qemu-img read files it was not given: a backing
// file whose format is not recorded (it would be probed), a backing file when none is
// allowed, and VMDK extents other than the image itself.
func checkChain(path string, chain []imageInfo, allowBacking bool) error {
	if len(chain) == 0 {
		return fmt.Errorf("qemu-img said nothing about %s", filepath.Base(path))
	}
	for _, img := range chain {
		name := img.Filename
		if name == "" {
			name = path
		}
		if img.Backing != "" {
			if !allowBacking {
				return refusedf("%s refers to another image (%s); VirtoMir does not follow that here.", filepath.Base(name), img.Backing)
			}
			if img.BackingFormat == "" {
				return refusedf("%s is an overlay on %s without a recorded format; set it with qemu-img rebase -u -F <format> first.", filepath.Base(name), img.Backing)
			}
		}
		if img.Specific != nil && img.Specific.Type == "vmdk" {
			for _, x := range img.Specific.Data.Extents {
				if !sameFile(x.Filename, name) {
					return refusedf("%s is a VMDK that reads another file (%s); only single-file VMDKs are accepted.", filepath.Base(name), x.Filename)
				}
			}
		}
	}
	return nil
}

func sameFile(a, b string) bool {
	if a == b {
		return true
	}
	fa, errA := os.Stat(a)
	fb, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(fa, fb)
}

// checkedImage opens path as format and checks its chain (see checkChain).
func checkedImage(ctx context.Context, path, format string, allowBacking bool) ([]imageInfo, error) {
	chain, err := qemuImgInfo(ctx, path, format)
	if err != nil {
		return nil, err
	}
	return chain, checkChain(path, chain, allowBacking)
}

// convertArgs copies src (opened as format) to a qcow2 at dst.
func convertArgs(format, src, dst string, compress bool) []string {
	args := []string{"convert", "-f", format, "-O", "qcow2"}
	if compress {
		args = append(args, "-c")
	}
	return append(args, src, dst)
}

// sniffFormat names the format of a file from its magic bytes:
// qcow2, vmdk, vhdx, vhd (vpc), vdi, qed, or raw.
func sniffFormat(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	size := fi.Size()

	buf := make([]byte, 512)
	n, _ := io.ReadFull(f, buf)
	if n < 4 {
		return "raw", nil
	}

	switch {
	case bytes.Equal(buf[:4], []byte("QFI\xfb")):
		return "qcow2", nil
	case bytes.Equal(buf[:4], []byte("KDMV")):
		return "vmdk", nil
	case n >= 8 && bytes.Equal(buf[:8], []byte("vhdxfile")):
		return "vhdx", nil
	case n >= 8 && bytes.Equal(buf[:8], []byte("conectix")):
		return "vpc", nil // qemu-img format for VHD
	case bytes.Equal(buf[:4], []byte("QED\x00")):
		return "qed", nil
	case n >= 68 && bytes.Equal(buf[64:68], []byte("\x7f\x10\xda\xbe")):
		return "vdi", nil
	case bytes.HasPrefix(buf, []byte("<<< Oracle VM VirtualBox")):
		return "vdi", nil
	}

	// Dynamic VHDs store the "conectix" header in the last 512-byte footer
	if size >= 512 {
		footer := make([]byte, 512)
		if _, err := f.ReadAt(footer, size-512); err == nil {
			if bytes.Equal(footer[:8], []byte("conectix")) {
				return "vpc", nil
			}
		}
	}

	return "raw", nil
}
