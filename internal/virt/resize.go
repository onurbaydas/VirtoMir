package virt

import (
	"fmt"
	"strings"

	"github.com/digitalocean/go-libvirt"
)

// maxResizeGiB keeps typos (an extra zero) from filling a pool.
const maxResizeGiB = 64 << 10

// ResizeDisk grows a machine's disk. Running machines see the new size at once (block
// resize); the guest still has to grow its partition and file system. Disks never shrink:
// that destroys data.
func (c *Client) ResizeDisk(uuid, target string, sizeGiB uint64) error {
	if sizeGiB == 0 || sizeGiB > maxResizeGiB {
		return invalidf("Size must be between 1 GiB and 64 TiB.")
	}
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
	if orDefault(disk.Device, "disk") != "disk" {
		return invalidf("%s is not a disk.", target)
	}
	src, _ := diskSource(disk.Source)
	if src == "" {
		return invalidf("%s has no image to resize.", target)
	}
	bytes := sizeGiB << 30
	vol, volErr := l.StorageVolLookupByPath(src)
	if volErr == nil {
		if _, capacity, _, err := l.StorageVolGetInfo(vol); err == nil && bytes <= capacity {
			return invalidf("The disk is already %s. Disks can only grow; shrinking would destroy data.", humanBytes(capacity))
		}
	}
	if on {
		if err := l.DomainBlockResize(d, target, bytes, libvirt.DomainBlockResizeBytes); err != nil {
			return classify(err)
		}
	} else {
		if volErr != nil {
			return refusedf("%s is not in a storage pool, so VirtoMir cannot resize it while the machine is off.", src)
		}
		if err := l.StorageVolResize(vol, bytes, 0); err != nil {
			return classify(err)
		}
	}
	c.log.Info("disk resized", "domain", d.Name, "target", target, "GiB", sizeGiB)
	return nil
}

// ResizeVolume grows a volume that no machine uses (resize attached disks from the machine).
func (c *Client) ResizeVolume(pool, name string, sizeGiB uint64) error {
	if sizeGiB == 0 || sizeGiB > maxResizeGiB {
		return invalidf("Size must be between 1 GiB and 64 TiB.")
	}
	l, err := c.conn()
	if err != nil {
		return err
	}
	p, err := l.StoragePoolLookupByName(pool)
	if err != nil {
		return classify(err)
	}
	v, err := l.StorageVolLookupByName(p, name)
	if err != nil {
		return classify(err)
	}
	path, err := l.StorageVolGetPath(v)
	if err != nil {
		return classify(err)
	}
	all, err := c.diskUsers(l)
	if err != nil {
		return refusedf("Could not check which machines use %s: %v", name, err)
	}
	if users := all[path]; len(users) > 0 {
		return refusedf("%s is used by %s: resize it from the machine's Hardware tab.", name, strings.Join(users, ", "))
	}
	_, capacity, _, err := l.StorageVolGetInfo(v)
	if err != nil {
		return classify(err)
	}
	if sizeGiB<<30 <= capacity {
		return invalidf("The volume is already %s. Volumes can only grow.", humanBytes(capacity))
	}
	return classify(l.StorageVolResize(v, sizeGiB<<30, 0))
}

func humanBytes(b uint64) string {
	const gib = 1 << 30
	if b >= gib {
		return fmt.Sprintf("%.1f GiB", float64(b)/gib)
	}
	return fmt.Sprintf("%d MiB", b>>20)
}
