package virt

import (
	"strings"

	"golang.org/x/sys/unix"
)

func xattrLabel(path string) string {
	buf := make([]byte, 256)
	n, err := unix.Lgetxattr(path, "security.selinux", buf)
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(buf[:n]), "\x00")
}
