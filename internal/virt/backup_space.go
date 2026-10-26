package virt

import (
	"github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"
)

// backupNeed estimates the bytes a backup writes: what its disks take on the host (an
// upper bound; backups are compressed and skip unused space). An incremental backup holds
// only what changed, so only a tenth of that is asked for.
func backupNeed(allocated []uint64, incremental bool) uint64 {
	var need uint64
	for _, a := range allocated {
		need += a
	}
	if incremental {
		need /= 10
	}
	return need
}

// backupRoom refuses a backup that cannot fit in dir before anything is written. When the
// free space or a disk's size is unknown, the backup goes ahead.
func (c *Client) backupRoom(l *libvirt.Libvirt, d libvirt.Domain, dir BackupDir, disks []libvirtxml.DomainDisk, incremental bool) error {
	free, _, ok := diskSpace(string(dir))
	if !ok {
		return nil
	}
	var allocated []uint64
	for _, disk := range disks {
		alloc, _, _, err := l.DomainGetBlockInfo(d, disk.Target.Dev, 0)
		if err != nil {
			return nil
		}
		allocated = append(allocated, alloc)
	}
	if need := backupNeed(allocated, incremental); free < need {
		return refusedf("Not enough space in %s: the backup needs about %s, %s is free. Free some space or choose another backup location.",
			dir, humanBytes(need), humanBytes(free))
	}
	return nil
}

// BackupSpace is the free and total bytes where backups go (ok false when unknown).
func BackupSpace(dir BackupDir) (free, total uint64, ok bool) {
	return diskSpace(string(dir))
}
