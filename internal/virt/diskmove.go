package virt

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"

	"github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"
)

// MoveDisk moves one disk of a machine into another storage pool on the same host, as a
// standalone qcow2 (a linked clone's template stays where it is). A running machine keeps
// running: libvirt copies the disk while the guest writes to it (block copy) and switches
// over once both are in sync. A stopped machine's disk is copied with qemu-img. The old
// image is deleted afterwards if it is a pool volume no other machine uses.
func (c *Client) MoveDisk(ctx context.Context, uuid, target, pool string, report func(string)) error {
	l, d, err := c.lookup(uuid)
	if err != nil {
		return err
	}
	sum, err := summary(l, d)
	if err != nil {
		return classify(err)
	}
	on := isOn(sum.State) || sum.State == StatePaused
	x, err := c.parseDomain(l, d, !on)
	if err != nil {
		return err
	}
	disk := findDisk(x, target)
	if disk == nil {
		return fmt.Errorf("%w: no disk %s", ErrNotFound, target)
	}
	src, _ := diskSource(disk.Source)
	if orDefault(disk.Device, "disk") != "disk" || src == "" {
		return invalidf("%s is not a disk with an image.", target)
	}
	if disk.Shareable != nil {
		return refusedf("%s is shared with other machines and cannot be moved.", target)
	}
	if n, err := l.DomainSnapshotNum(d, 0); err == nil && n > 0 {
		return refusedf("%s has snapshots. Delete them before moving a disk: they live in or next to the image.", d.Name)
	}
	if cps, _, err := l.DomainListAllCheckpoints(d, 1, 0); err == nil && len(cps) > 0 {
		return refusedf("%s has backup checkpoints. Take a full backup or delete its backups before moving a disk.", d.Name)
	}
	dir, err := c.filePoolPath(l, pool)
	if err != nil {
		return err
	}
	dst := path.Join(dir, d.Name+"-"+target+".qcow2")
	if path.Dir(src) == dir {
		return invalidf("%s is already in pool %s.", target, pool)
	}
	if _, err := os.Stat(dst); err == nil {
		return refusedf("%s already exists in pool %s.", path.Base(dst), pool)
	}

	if on {
		err = c.blockCopy(ctx, l, d, target, dst, report)
	} else {
		report(fmt.Sprintf("Copying %s to pool %s…", target, pool))
		cmd := exec.CommandContext(ctx, "qemu-img", convertArgs(diskFormat(*disk), src, dst, false)...)
		if out, cerr := cmd.CombinedOutput(); cerr != nil {
			err = fmt.Errorf("copy %s: %v: %s", target, cerr, strings.TrimSpace(string(out)))
		}
	}
	if err != nil {
		_ = os.Remove(dst)
		c.refreshPool(l, pool)
		return err
	}
	// The persistent definition points at the new image too (a block copy pivots only the
	// running machine).
	if err := c.repointDisk(l, d, target, dst); err != nil {
		return err
	}
	c.refreshPool(l, pool)
	if vol, err := l.StorageVolLookupByPath(src); err == nil && len(c.diskUsers(l)[src]) == 0 {
		report("Deleting the old image…")
		if err := l.StorageVolDelete(vol, 0); err != nil {
			c.log.Warn("old image kept", "path", src, "err", err)
		}
	}
	c.log.Info("disk moved", "domain", d.Name, "target", target, "from", src, "to", dst)
	return nil
}

// filePoolPath is the directory of a pool whose volumes are plain files (dir, fs, netfs).
func (c *Client) filePoolPath(l *libvirt.Libvirt, pool string) (string, error) {
	sp, err := l.StoragePoolLookupByName(pool)
	if err != nil {
		return "", classify(err)
	}
	raw, err := l.StoragePoolGetXMLDesc(sp, 0)
	if err != nil {
		return "", classify(err)
	}
	var px libvirtxml.StoragePool
	if err := px.Unmarshal(raw); err != nil || px.Target == nil || px.Target.Path == "" ||
		(px.Type != "dir" && px.Type != "fs" && px.Type != "netfs") {
		return "", invalidf("Move disks into a directory, file system or NFS pool (pool %s is not one).", pool)
	}
	return px.Target.Path, nil
}

func (c *Client) refreshPool(l *libvirt.Libvirt, pool string) {
	if sp, err := l.StoragePoolLookupByName(pool); err == nil {
		_ = l.StoragePoolRefresh(sp, 0)
	}
}

// blockCopy mirrors a running machine's disk into dst and switches to it once in sync.
func (c *Client) blockCopy(ctx context.Context, l *libvirt.Libvirt, d libvirt.Domain, target, dst string, report func(string)) error {
	report("Starting live copy…")
	dest := fmt.Sprintf("<disk type='file'><source file='%s'/><driver type='qcow2'/></disk>", xmlEscape(dst))
	// A persistent machine needs a transient job (it does not survive a libvirt restart);
	// repointDisk then updates the persistent definition.
	if err := l.DomainBlockCopy(d, target, dest, nil, libvirt.DomainBlockCopyTransientJob); err != nil {
		return classify(err)
	}
	abort := func() { _ = l.DomainBlockJobAbort(d, target, 0) }
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			abort()
			return ctx.Err()
		case <-t.C:
		}
		found, _, _, cur, end, err := l.DomainGetBlockJobInfo(d, target, 0)
		if err != nil {
			abort()
			return classify(err)
		}
		if found == 0 {
			return fmt.Errorf("the copy of %s stopped before it was complete", target)
		}
		if end > 0 {
			report(fmt.Sprintf("Copying live: %d %% of %s", cur*100/end, humanBytes(end)))
		}
		if end > 0 && cur == end {
			break
		}
	}
	report("Switching to the new image…")
	if err := l.DomainBlockJobAbort(d, target, libvirt.DomainBlockJobAbortPivot); err != nil {
		abort()
		return classify(err)
	}
	return nil
}

// repointDisk sets the persistent definition's disk to the qcow2 at dst.
func (c *Client) repointDisk(l *libvirt.Libvirt, d libvirt.Domain, target, dst string) error {
	x, err := c.parseDomain(l, d, true)
	if err != nil {
		return err
	}
	disk := findDisk(x, target)
	if disk == nil {
		return fmt.Errorf("%w: no disk %s", ErrNotFound, target)
	}
	if !setDiskImage(disk, dst) {
		return nil
	}
	doc, err := x.Marshal()
	if err != nil {
		return err
	}
	_, err = l.DomainDefineXMLFlags(doc, libvirt.DomainDefineValidate)
	return classify(err)
}

// setDiskImage points a disk at a standalone qcow2 file; it reports whether anything changed.
func setDiskImage(disk *libvirtxml.DomainDisk, file string) bool {
	if src, _ := diskSource(disk.Source); src == file && disk.Driver != nil && disk.Driver.Type == "qcow2" {
		return false
	}
	disk.Source = &libvirtxml.DomainDiskSource{File: &libvirtxml.DomainDiskSourceFile{File: file}}
	if disk.Driver == nil {
		disk.Driver = &libvirtxml.DomainDiskDriver{Name: "qemu"}
	}
	disk.Driver.Type = "qcow2"
	disk.BackingStore = nil
	return true
}
