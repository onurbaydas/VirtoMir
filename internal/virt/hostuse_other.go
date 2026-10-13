//go:build !linux

package virt

// pciHostUse only matters on Linux, where virtomird runs.
func pciHostUse(string) string { return "" }
