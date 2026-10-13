//go:build !linux

package virt

import "os"

// canPass only matters on Linux, where virtomird runs; elsewhere use the "other" bit.
func canPass(st os.FileInfo, _, _ int) bool { return st.Mode().Perm()&0o001 != 0 }
