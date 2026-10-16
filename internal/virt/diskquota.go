package virt

import (
	"os"
	"strings"
)

// DiskBytes returns, for each machine, the full (virtual) size of its own disks: the
// writable, unshared ones a backup copies too. A thin disk counts with the size it can
// grow to, which is what a disk quota has to plan for (ADR-0044). Machines that do not
// exist are left out.
func (c *Client) DiskBytes(uuids []string) (map[string]uint64, error) {
	l, err := c.conn()
	if err != nil {
		return nil, err
	}
	out := map[string]uint64{}
	for _, uuid := range uuids {
		_, d, err := c.lookup(uuid)
		if err != nil {
			continue
		}
		x, err := c.parseDomain(l, d, true)
		if err != nil {
			continue
		}
		var total uint64
		for _, disk := range backupDisks(x) {
			src := disk.Source
			switch {
			case src.Volume != nil:
				if p, err := l.StoragePoolLookupByName(src.Volume.Pool); err == nil {
					if v, err := l.StorageVolLookupByName(p, src.Volume.Volume); err == nil {
						if _, capacity, _, err := l.StorageVolGetInfo(v); err == nil {
							total += capacity
						}
					}
				}
			default:
				path, _ := diskSource(src)
				if !strings.HasPrefix(path, "/") {
					continue // network disks (Ceph, iSCSI by URL) live elsewhere
				}
				if v, err := l.StorageVolLookupByPath(path); err == nil {
					if _, capacity, _, err := l.StorageVolGetInfo(v); err == nil {
						total += capacity
						continue
					}
				}
				if fi, err := os.Stat(path); err == nil {
					total += uint64(fi.Size()) // not in a pool: its size on disk
				}
			}
		}
		out[strings.ToLower(uuid)] = total
	}
	return out, nil
}
