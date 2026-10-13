package virt

import (
	"net"
	"os"
	"path/filepath"
	"strings"
)

// pciHostUse says why the host itself needs a PCI device: a disk behind it is mounted (or
// swap), or a network interface on it has an address. "" when nothing obvious uses it.
func pciHostUse(addr string) string {
	dev := "/sys/bus/pci/devices/" + addr
	if _, err := os.Stat(dev); err != nil {
		return ""
	}
	// Disks: /sys/block/<disk> links to …/<pci address>/…/block/<disk>.
	mounts, _ := os.ReadFile("/proc/mounts")
	swaps, _ := os.ReadFile("/proc/swaps")
	blocks, _ := filepath.Glob("/sys/block/*")
	for _, b := range blocks {
		target, err := filepath.EvalSymlinks(b)
		if err != nil || !strings.Contains(target, "/"+addr+"/") {
			continue
		}
		disk := "/dev/" + filepath.Base(b)
		for _, line := range strings.Split(string(mounts)+"\n"+string(swaps), "\n") {
			f := strings.Fields(line)
			if len(f) >= 2 && strings.HasPrefix(f[0], disk) {
				return "disk " + filepath.Base(b) + " is in use (" + f[1] + ")"
			}
		}
		// LVM, RAID or crypt on top of the disk also count.
		if holders, _ := filepath.Glob(b + "/*/holders/*"); len(holders) > 0 {
			return "disk " + filepath.Base(b) + " holds " + filepath.Base(holders[0])
		}
		if holders, _ := filepath.Glob(b + "/holders/*"); len(holders) > 0 {
			return "disk " + filepath.Base(b) + " holds " + filepath.Base(holders[0])
		}
	}
	// Network interfaces with an address.
	nets, _ := filepath.Glob(dev + "/net/*")
	for _, n := range nets {
		ifc, err := net.InterfaceByName(filepath.Base(n))
		if err != nil {
			continue
		}
		if addrs, _ := ifc.Addrs(); len(addrs) > 0 {
			return "network interface " + ifc.Name + " has an address (" + addrs[0].String() + ")"
		}
		if ifc.Flags&net.FlagUp != 0 {
			if masters, _ := filepath.Glob(n + "/master"); len(masters) > 0 {
				return "network interface " + ifc.Name + " is part of a bridge or bond"
			}
		}
	}
	return ""
}
