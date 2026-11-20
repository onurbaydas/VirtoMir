//go:build !linux

package virt

func xattrLabel(string) string { return "" }
