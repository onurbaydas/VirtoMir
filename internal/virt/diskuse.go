package virt

import (
	"fmt"
	"strings"

	"github.com/digitalocean/go-libvirt"
)

// checkDiskFree refuses to give a machine a writable disk that another machine already
// uses: two guests writing one image corrupt it. CD/DVD media and read-only or shareable
// disks are not checked (callers skip those). self is the machine getting the disk ("" when
// it does not exist yet). Like deleting, it fails closed when the machines cannot be read.
func (c *Client) checkDiskFree(l *libvirt.Libvirt, src, self string) error {
	if src == "" {
		return nil
	}
	users, err := c.diskUsers(l)
	if err != nil {
		return fmt.Errorf("could not check whether %s is used by another machine: %w", src, err)
	}
	return diskConflict(src, self, users)
}

// diskConflict is the decision of checkDiskFree for a given map of disk users.
func diskConflict(src, self string, users map[string][]string) error {
	var others []string
	for _, u := range users[src] {
		if strings.TrimSuffix(u, " (as backing image)") == self {
			continue
		}
		others = append(others, u)
	}
	if len(others) == 0 {
		return nil
	}
	return refusedf("%s is already used by %s. Two machines writing one disk image destroy it: copy the image first, or detach it there.", src, strings.Join(others, ", "))
}
