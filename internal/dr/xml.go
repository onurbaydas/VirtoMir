package dr

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/kdomanski/iso9660" // BSD-2-Clause
	"libvirt.org/go/libvirtxml"
)

// BuildDRDomainXML turns a source machine's definition into one that can run on
// the DR host from the replicated (or cloned) disks: a new name and UUID, disks
// repointed at the DR copies, networks remapped, old installation media ejected,
// the cloud-init seed (re)attached, and machine-specific state (UEFI variables,
// TPM) left behind so a fresh instance starts here.
func BuildDRDomainXML(origDoc string, drName string, diskMap map[string]string, netMap map[string]string, seedISOPath string) (string, error) {
	var x libvirtxml.Domain
	if err := x.Unmarshal(origDoc); err != nil {
		return "", err
	}
	x.Name = drName
	x.UUID = newUUID()

	if x.Devices != nil {
		remapDisksData(&x, diskMap, seedISOPath)
		remapInterfacesData(&x, netMap)
		clearTPMState(&x)
	}

	if x.OS != nil && x.OS.NVRam != nil {
		// Keep the template (e.g. Secure Boot keys); libvirt creates a fresh
		// variables file on this host, mirroring virt.RewriteForMove.
		x.OS.NVRam.NVRam = ""
	}
	return x.Marshal()
}

// remapDisksData points every disk at its DR copy and ejects removable media,
// then appends the cloud-init seed when one was handed in and no disk already
// carries it.
func remapDisksData(x *libvirtxml.Domain, diskMap map[string]string, seedISOPath string) {
	hasSeed := false
	for i := range x.Devices.Disks {
		disk := &x.Devices.Disks[i]
		if path, ok := lookupDiskMap(disk, diskMap); ok {
			setDiskSource(disk, path)
			continue
		}
		if disk.Device != "cdrom" && disk.Device != "floppy" {
			continue
		}
		if seedISOPath != "" && disk.Source != nil && disk.Source.File != nil && disk.Source.File.File == seedISOPath {
			hasSeed = true
			continue
		}
		disk.Source = nil // installation media stays on the source host
	}
	if seedISOPath == "" || hasSeed {
		return
	}
	x.Devices.Disks = append(x.Devices.Disks, libvirtxml.DomainDisk{
		Device:   "cdrom",
		Driver:   &libvirtxml.DomainDiskDriver{Name: "qemu", Type: "raw"},
		Source:   &libvirtxml.DomainDiskSource{File: &libvirtxml.DomainDiskSourceFile{File: seedISOPath}},
		Target:   &libvirtxml.DomainDiskTarget{Dev: nextCDTarget(x), Bus: "sata"},
		ReadOnly: &libvirtxml.DomainDiskReadOnly{},
	})
}

// lookupDiskMap finds a disk in the map by its target device name (vda) or, as a
// fallback, by its original source path.
func lookupDiskMap(disk *libvirtxml.DomainDisk, diskMap map[string]string) (string, bool) {
	if disk.Target != nil {
		if path, ok := diskMap[disk.Target.Dev]; ok {
			return path, true
		}
	}
	if disk.Source != nil && disk.Source.File != nil {
		if path, ok := diskMap[disk.Source.File.File]; ok {
			return path, true
		}
	}
	return "", false
}

// setDiskSource points disk at path: a /dev device becomes a block source,
// anything else a file source (the type attr follows from the source kind).
func setDiskSource(disk *libvirtxml.DomainDisk, path string) {
	if isBlockPath(path) {
		disk.Source = &libvirtxml.DomainDiskSource{Block: &libvirtxml.DomainDiskSourceBlock{Dev: path}}
		return
	}
	disk.Source = &libvirtxml.DomainDiskSource{File: &libvirtxml.DomainDiskSourceFile{File: path}}
}

// isBlockPath reports whether a disk path is a block device (a zvol appears as
// /dev/zvol/<pool>/<dataset>).
func isBlockPath(path string) bool {
	return strings.HasPrefix(path, "/dev/")
}

// remapInterfacesData sends each interface to its mapped network or bridge; an
// interface the map does not mention keeps its source.
func remapInterfacesData(x *libvirtxml.Domain, netMap map[string]string) {
	for i := range x.Devices.Interfaces {
		ifc := &x.Devices.Interfaces[i]
		if ifc.Source == nil {
			continue
		}
		if ifc.Source.Network != nil {
			if target, ok := netMap[ifc.Source.Network.Network]; ok && target != "" {
				ifc.Source.Network.Network = target
			}
		}
		if ifc.Source.Bridge != nil {
			if target, ok := netMap[ifc.Source.Bridge.Bridge]; ok && target != "" {
				ifc.Source.Bridge.Bridge = target
			}
		}
	}
}

// clearTPMState drops the swtpm state of every TPM so a failover copy starts
// with a fresh one instead of the source machine's secrets.
func clearTPMState(x *libvirtxml.Domain) {
	for i := range x.Devices.TPMs {
		b := x.Devices.TPMs[i].Backend
		if b == nil || b.Emulator == nil {
			continue
		}
		b.Emulator.PersistentState = ""
		if src := b.Emulator.Source; src != nil {
			if src.File != nil {
				src.File.Path = ""
			}
			if src.Dir != nil {
				src.Dir.Path = ""
			}
		}
	}
}

// nextCDTarget returns the first free sd* device name for the seed drive.
func nextCDTarget(x *libvirtxml.Domain) string {
	used := map[string]bool{}
	for _, d := range x.Devices.Disks {
		if d.Target != nil {
			used[d.Target.Dev] = true
		}
	}
	for i := 0; ; i++ {
		name := "sd" + diskLetters(i)
		if !used[name] {
			return name
		}
	}
}

// diskLetters is the Linux disk suffix for index i: 0→a, 25→z, 26→aa.
func diskLetters(i int) string {
	s := ""
	for i++; i > 0; i = (i - 1) / 26 {
		s = string(rune('a'+(i-1)%26)) + s
	}
	return s
}

// newUUID is a random version-4 UUID.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// GenerateCloudInitNetworkISO writes a NoCloud seed image (ISO 9660, volume
// label "cidata") to targetPath: minimal user-data, a DR-flavoured meta-data and
// a network-config that re-addresses the replica's interface. The replica keeps
// the source MAC, so it must not answer on the real network under its old
// address.
func GenerateCloudInitNetworkISO(hostname string, ipConf IPConfig, targetPath string) error {
	w, err := iso9660.NewWriter()
	if err != nil {
		return err
	}
	defer func() { _ = w.Cleanup() }()

	const userData = "#cloud-config\nmanage_etc_hosts: true\npackage_update: false\n"
	metaData := fmt.Sprintf("instance-id: virtomir-dr-%s\nlocal-hostname: %s\n", newID()[:8], hostname)

	files := []struct {
		name    string
		content string
	}{
		{"user-data", userData},
		{"meta-data", metaData},
		{"network-config", networkConfig(ipConf)},
	}
	for _, f := range files {
		if err := w.AddFile(strings.NewReader(f.content), f.name); err != nil {
			return err
		}
	}

	f, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := w.WriteTo(f, "cidata"); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// networkConfig renders a cloud-init version-2 network-config that gives id0 the
// new address/gateway/DNS.
func networkConfig(ipConf IPConfig) string {
	var b strings.Builder
	b.WriteString("version: 2\nethernets:\n  id0:\n    match:\n")
	fmt.Fprintf(&b, "      macaddress: %s\n", yamlQ(ipConf.InterfaceMAC))
	fmt.Fprintf(&b, "    addresses:\n      - %s\n", yamlQ(ipConf.IPv4Address))
	if ipConf.IPv4Gateway != "" {
		fmt.Fprintf(&b, "    gateway4: %s\n", yamlQ(ipConf.IPv4Gateway))
	}
	if len(ipConf.Nameservers) > 0 {
		b.WriteString("    nameservers:\n      addresses:\n")
		for _, ns := range ipConf.Nameservers {
			fmt.Fprintf(&b, "        - %s\n", yamlQ(ns))
		}
	}
	return b.String()
}

// yamlQ quotes a value for YAML (a JSON string is a valid YAML scalar).
func yamlQ(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
