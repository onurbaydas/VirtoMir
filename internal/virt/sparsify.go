package virt

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"strings"

	"github.com/digitalocean/go-libvirt"
)

// sparsifyCmd runs virt-sparsify; tests replace it.
var sparsifyCmd = func(ctx context.Context, file string) error {
	out, err := exec.CommandContext(ctx, "virt-sparsify", "--in-place", file).CombinedOutput()
	if errors.Is(err, exec.ErrNotFound) {
		return refusedf("virt-sparsify is not installed on this host: install guestfs-tools (Debian, Ubuntu, Fedora) and try again.")
	}
	if err != nil {
		return fmt.Errorf("virt-sparsify %s: %v: %s", path.Base(file), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// SparsifyDisk gives the host back the space a stopped machine's disk image holds for
// blocks its file systems no longer use (virt-sparsify --in-place: free space is trimmed,
// so it also works for images with snapshots or a backing file). Only for stopped
// machines: a running guest writing to the image would corrupt it. It answers how many
// bytes the image shrank by.
func (c *Client) SparsifyDisk(ctx context.Context, uuid, target string, report func(string)) (uint64, error) {
	l, d, err := c.lookup(uuid)
	if err != nil {
		return 0, err
	}
	sum, err := summary(l, d)
	if err != nil {
		return 0, classify(err)
	}
	if sum.State != StateShutoff {
		return 0, refusedf("%s is %s: shut it down first. A disk is only compacted while nothing writes to it.", d.Name, sum.State)
	}
	x, err := c.parseDomain(l, d, true)
	if err != nil {
		return 0, err
	}
	disk := findDisk(x, target)
	if disk == nil {
		return 0, fmt.Errorf("%w: no disk %s", ErrNotFound, target)
	}
	if orDefault(disk.Device, "disk") != "disk" || disk.Source == nil || disk.Source.File == nil || disk.Source.File.File == "" {
		return 0, invalidf("%s is not a disk image file: only file disks (qcow2, raw) are compacted. Block storage frees space through the guest's TRIM.", target)
	}
	if disk.Shareable != nil || disk.ReadOnly != nil {
		return 0, refusedf("%s is shared or read-only and is left as it is.", target)
	}
	file, format := disk.Source.File.File, diskFormat(*disk)
	size := func() uint64 {
		if info, err := qemuImgInfo(ctx, file, format); err == nil && len(info) > 0 {
			return info[0].ActualSize
		}
		return 0
	}
	before := size()
	report(fmt.Sprintf("Trimming the free space of %s (virt-sparsify)…", target))
	if err := sparsifyCmd(ctx, file); err != nil {
		return 0, err
	}
	after := size()
	refreshPoolOf(l, file)
	c.log.Info("disk compacted", "domain", d.Name, "disk", target, "before", before, "after", after)
	if after >= before {
		return 0, nil
	}
	return before - after, nil
}

// refreshPoolOf refreshes the pool that holds file, so its free space is current.
func refreshPoolOf(l *libvirt.Libvirt, file string) {
	if v, err := l.StorageVolLookupByPath(file); err == nil {
		if p, err := l.StoragePoolLookupByVolume(v); err == nil {
			_ = l.StoragePoolRefresh(p, 0)
		}
	}
}
