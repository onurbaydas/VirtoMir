//go:build !linux

package virt

// diskSpace is unknown off Linux (VirtoMir runs on Linux hosts).
func diskSpace(string) (free, total uint64, ok bool) { return 0, 0, false }

// onSystemDisk is unknown off Linux.
func onSystemDisk(string) bool { return false }
