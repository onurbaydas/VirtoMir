//go:build linux

package virt

import (
	"os"
	"testing"
)

func TestOnSystemDisk(t *testing.T) {
	if !onSystemDisk("/") {
		t.Error("/ must be on the system disk")
	}
	if !onSystemDisk("/no/such/folder/yet") {
		t.Error("a missing folder is judged by its nearest existing parent (/)")
	}
	if _, err := os.Stat("/proc/self"); err == nil && onSystemDisk("/proc") {
		t.Error("/proc is not the system disk")
	}
}
