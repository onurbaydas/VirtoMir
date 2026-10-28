package storconn

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// SAN storage: Fibre Channel adapters and their LUNs, and multipath for any LUN seen on more
// than one path (FC fabrics, iSCSI with several portals).

// SysRoot is where sysfs is; tests point it to a fake tree.
var SysRoot = "/sys"

// Adapter is a Fibre Channel port of this host.
type Adapter struct {
	Host  string `json:"host"`  // scsi host, e.g. host3
	WWPN  string `json:"wwpn"`  // what the SAN zones LUNs to
	State string `json:"state"` // Online, Linkdown, …
	Speed string `json:"speed,omitempty"`
}

func sysRead(path string) string {
	b, _ := os.ReadFile(path)
	return strings.TrimSpace(string(b))
}

// Adapters lists the Fibre Channel ports (none without an FC HBA).
func Adapters() []Adapter {
	dirs, _ := filepath.Glob(filepath.Join(SysRoot, "class", "fc_host", "host*"))
	out := []Adapter{}
	for _, d := range dirs {
		out = append(out, Adapter{Host: filepath.Base(d), WWPN: sysRead(filepath.Join(d, "port_name")),
			State: sysRead(filepath.Join(d, "port_state")), Speed: sysRead(filepath.Join(d, "speed"))})
	}
	return out
}

// Rescan makes LUNs zoned or mapped to this host since boot appear: a loop initialization
// on every FC port, a scan of its SCSI host, and a rescan of the iSCSI sessions.
func Rescan(ctx context.Context, r Runner) {
	for _, a := range Adapters() {
		_ = os.WriteFile(filepath.Join(SysRoot, "class", "fc_host", a.Host, "issue_lip"), []byte("1"), 0o644)
		_ = os.WriteFile(filepath.Join(SysRoot, "class", "scsi_host", a.Host, "scan"), []byte("- - -"), 0o644)
	}
	if found("iscsiadm") {
		_, _ = r.Run(ctx, "iscsiadm", "-m", "session", "--rescan")
	}
	_, _ = r.Run(ctx, "udevadm", "settle", "--timeout=10")
}

type blockDev struct {
	Name       string     `json:"name"`
	Path       string     `json:"path"`
	Size       uint64     `json:"size"`
	Type       string     `json:"type"`
	Tran       string     `json:"tran"`
	WWN        string     `json:"wwn"`
	FSType     string     `json:"fstype"`
	PTType     string     `json:"pttype"`
	MountPoint string     `json:"mountpoint"`
	Children   []blockDev `json:"children"`
}

func blockDevices(ctx context.Context, r Runner) ([]blockDev, error) {
	out, err := r.Run(ctx, "lsblk", "-J", "-b", "-o", "NAME,PATH,SIZE,TYPE,TRAN,WWN,FSTYPE,PTTYPE,MOUNTPOINT")
	if err != nil {
		return nil, err
	}
	var doc struct {
		BlockDevices []blockDev `json:"blockdevices"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("read lsblk output: %w", err)
	}
	return doc.BlockDevices, nil
}

// needsMultipath: a LUN (one WWN) shows as several disks and no multipath device holds them.
func needsMultipath(devs []blockDev) bool {
	paths := map[string]int{}
	for _, d := range devs {
		if d.Type != "disk" || d.WWN == "" || slices.ContainsFunc(d.Children, func(c blockDev) bool { return c.Type == "mpath" }) {
			continue
		}
		paths[d.WWN]++
		if paths[d.WWN] > 1 {
			return true
		}
	}
	return false
}

// EnableMultipath turns multipath on when a LUN is seen on more than one path, so it is used
// as one disk (and survives losing a path) instead of as several unrelated ones.
func EnableMultipath(ctx context.Context, r Runner) (bool, error) {
	devs, err := blockDevices(ctx, r)
	if err != nil || !needsMultipath(devs) {
		return false, err
	}
	if err := Ensure(ctx, r, "multipath"); err != nil {
		return false, err
	}
	if found("mpathconf") { // Fedora, RHEL: writes /etc/multipath.conf and starts multipathd
		if _, err := r.Run(ctx, "mpathconf", "--enable", "--with_multipathd", "y"); err != nil {
			return false, fmt.Errorf("turn multipath on: %w", err)
		}
	} else if _, err := r.Run(ctx, "systemctl", "enable", "--now", "multipathd"); err != nil {
		return false, fmt.Errorf("turn multipath on: %w", err)
	}
	_, _ = r.Run(ctx, "multipath", "-r")
	_, _ = r.Run(ctx, "udevadm", "settle", "--timeout=10")
	return true, nil
}

// LUN is a SAN disk: a multipath device, or a single-path FC disk.
type LUN struct {
	Path      string `json:"path"`
	SizeBytes uint64 `json:"sizeBytes"`
	Tran      string `json:"tran,omitempty"` // fc, iscsi, sas, …
	Paths     int    `json:"paths"`
	// Empty: no partition table, file system, LVM label, mount or holder on it.
	Empty bool `json:"empty"`
}

// LUNs lists the multipath devices (one per LUN, whatever number of paths) and the FC disks
// multipath does not hold.
func LUNs(ctx context.Context, r Runner) ([]LUN, error) {
	devs, err := blockDevices(ctx, r)
	if err != nil {
		return nil, err
	}
	var out []LUN
	byName := map[string]int{}
	for _, d := range devs {
		if d.Type != "disk" {
			continue
		}
		held := false
		for _, c := range d.Children {
			if c.Type != "mpath" {
				continue
			}
			held = true
			if i, ok := byName[c.Name]; ok {
				out[i].Paths++
				continue
			}
			path := c.Path
			if path == "" {
				path = "/dev/mapper/" + c.Name
			}
			byName[c.Name] = len(out)
			out = append(out, LUN{Path: path, SizeBytes: c.Size, Tran: d.Tran, Paths: 1, Empty: unused(c)})
		}
		if !held && d.Tran == "fc" {
			out = append(out, LUN{Path: d.Path, SizeBytes: d.Size, Tran: "fc", Paths: 1, Empty: unused(d)})
		}
	}
	return out, nil
}

func unused(d blockDev) bool {
	return d.FSType == "" && d.PTType == "" && d.MountPoint == "" && len(d.Children) == 0
}
