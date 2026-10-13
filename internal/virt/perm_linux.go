package virt

import (
	"os"
	"syscall"
)

// canPass reports whether uid/gid may traverse the directory st describes.
func canPass(st os.FileInfo, uid, gid int) bool {
	m := st.Mode().Perm()
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		if int(s.Uid) == uid {
			return m&0o100 != 0
		}
		if int(s.Gid) == gid {
			return m&0o010 != 0
		}
	}
	return m&0o001 != 0
}
