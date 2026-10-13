package virt

import "syscall"

// Network file systems a disk can be shared between hosts on.
var networkFS = map[int64]bool{
	0x6969:     true, // NFS
	0xFF534D42: true, // CIFS
	0xFE534D42: true, // SMB2
	0x65735546: true, // FUSE (e.g. GlusterFS, CephFS-fuse)
	0x00C36400: true, // CephFS
}

// onNetworkFS reports whether path lives on a network file system.
func onNetworkFS(path string) bool {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return false
	}
	return networkFS[int64(st.Type)]
}
