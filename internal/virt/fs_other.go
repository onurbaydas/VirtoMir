//go:build !linux

package virt

// onNetworkFS only matters on Linux, where virtomird runs.
func onNetworkFS(string) bool { return false }
