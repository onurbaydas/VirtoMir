//go:build linux

package virt

import (
	"path/filepath"
	"syscall"
)

// diskSpace is the free (for unprivileged writers excluded: root may use the reserve) and
// total bytes of the file system holding path, or of its nearest existing parent.
func diskSpace(path string) (free, total uint64, ok bool) {
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		var st syscall.Statfs_t
		if err := syscall.Statfs(p, &st); err == nil {
			return st.Bavail * uint64(st.Bsize), st.Blocks * uint64(st.Bsize), true
		}
		if parent := filepath.Dir(p); parent == p {
			return 0, 0, false
		}
	}
}
