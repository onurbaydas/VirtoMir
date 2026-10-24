package ceph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

// blockDevRE: a whole disk by a stable path; nothing outside /dev (as internal/virt).
var blockDevRE = regexp.MustCompile(`^/dev/[A-Za-z0-9][A-Za-z0-9/_.:+-]*$`)

// DiskInfo is a host disk and whether Ceph may take it.
type DiskInfo struct {
	Path       string   `json:"path"`
	Name       string   `json:"name"`
	SizeBytes  uint64   `json:"sizeBytes"`
	Rotational bool     `json:"rotational"`
	DeviceType string   `json:"deviceType"` // ssd, nvme or hdd
	MountPoint string   `json:"mountPoint,omitempty"`
	FSType     string   `json:"fsType,omitempty"`
	Partitions []string `json:"partitions,omitempty"`
	// Empty: no partition table, partitions, file system, LVM/ZFS/RAID label or mount.
	// Only empty disks become OSDs; RefusalReason says what is on the others.
	Empty         bool   `json:"empty"`
	RefusalReason string `json:"refusalReason,omitempty"`
}

// lsblkDev is one device of `lsblk -J -b`; older util-linux prints numbers and flags as
// strings, newer ones as JSON numbers and booleans.
type lsblkDev struct {
	Name       string     `json:"name"`
	Path       string     `json:"path"`
	Size       flexNum    `json:"size"`
	Rota       flexBool   `json:"rota"`
	Type       string     `json:"type"`
	MountPoint *string    `json:"mountpoint"`
	FSType     *string    `json:"fstype"`
	PTType     *string    `json:"pttype"`
	Children   []lsblkDev `json:"children"`
}

type flexNum uint64

func (n *flexNum) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "null" || s == "" {
		return nil
	}
	v, err := strconv.ParseUint(s, 10, 64)
	*n = flexNum(v)
	return err
}

type flexBool bool

func (f *flexBool) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	*f = s == "true" || s == "1"
	return nil
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

var lsblkArgs = []string{"-J", "-b", "-o", "NAME,PATH,SIZE,ROTA,TYPE,MOUNTPOINT,FSTYPE,PTTYPE"}

func lsblk(ctx context.Context, r Runner, path string) ([]lsblkDev, error) {
	args := lsblkArgs
	if path != "" {
		args = append(append([]string{}, lsblkArgs...), path)
	}
	out, err := r.Run(ctx, "lsblk", args...)
	if err != nil {
		return nil, err
	}
	var doc struct {
		BlockDevices []lsblkDev `json:"blockdevices"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("read lsblk output: %w", err)
	}
	return doc.BlockDevices, nil
}

func diskInfo(d lsblkDev) DiskInfo {
	info := DiskInfo{Path: d.Path, Name: d.Name, SizeBytes: uint64(d.Size), Rotational: bool(d.Rota),
		MountPoint: str(d.MountPoint), FSType: str(d.FSType)}
	if info.Path == "" {
		info.Path = "/dev/" + d.Name
	}
	switch {
	case strings.HasPrefix(d.Name, "nvme"):
		info.DeviceType = "nvme"
	case info.Rotational:
		info.DeviceType = "hdd"
	default:
		info.DeviceType = "ssd"
	}
	var reasons []string
	for _, c := range d.Children {
		info.Partitions = append(info.Partitions, c.Name)
		if m := str(c.MountPoint); m != "" && info.MountPoint == "" {
			info.MountPoint = m
		}
	}
	if info.MountPoint != "" {
		reasons = append(reasons, "mounted at "+info.MountPoint)
	}
	if len(info.Partitions) > 0 {
		reasons = append(reasons, "has partitions "+strings.Join(info.Partitions, ", "))
	}
	if info.FSType != "" {
		reasons = append(reasons, signature(info.FSType))
	}
	if pt := str(d.PTType); pt != "" && len(info.Partitions) == 0 {
		reasons = append(reasons, "has a "+pt+" partition table")
	}
	if len(reasons) == 0 && Busy(info.Path) {
		reasons = append(reasons, "is in use by the system (held open: mounted, LVM, RAID, ZFS or an iSCSI target)")
	}
	info.Empty = len(reasons) == 0
	info.RefusalReason = strings.Join(reasons, "; ")
	return info
}

// Busy reports whether the kernel holds a disk exclusively, which catches what leaves no
// signature on it (an iSCSI target's backing disk, a device-mapper user). Tests replace it.
var Busy = func(path string) bool {
	f, err := os.OpenFile(path, os.O_RDONLY|os.O_EXCL, 0)
	if err != nil {
		return errors.Is(err, syscall.EBUSY)
	}
	_ = f.Close()
	return false
}

// MarkUsed refuses the disks that are something else's (a libvirt pool's volume, a
// machine's disk); used says by what ("" = nothing).
func MarkUsed(disks []DiskInfo, used func(path string) string) {
	for i := range disks {
		if by := used(disks[i].Path); by != "" && disks[i].Empty {
			disks[i].Empty, disks[i].RefusalReason = false, by
		}
	}
}

// signature names what a file system type in lsblk means.
func signature(fs string) string {
	switch fs {
	case "LVM2_member":
		return "is an LVM physical volume"
	case "zfs_member":
		return "belongs to a ZFS pool"
	case "linux_raid_member":
		return "belongs to a software RAID"
	case "ceph_bluestore":
		return "is already a Ceph OSD"
	}
	return "has a " + fs + " file system"
}

// InspectDisk describes one disk.
func InspectDisk(ctx context.Context, r Runner, path string) (DiskInfo, error) {
	if !blockDevRE.MatchString(path) || strings.Contains(path, "..") {
		return DiskInfo{}, invalidf("%q is not a disk path like /dev/sdb", path)
	}
	devs, err := lsblk(ctx, r, path)
	if err != nil {
		return DiskInfo{}, err
	}
	if len(devs) == 0 {
		return DiskInfo{}, fmt.Errorf("%w: no disk %s", ErrNotFound, path)
	}
	if devs[0].Type != "disk" {
		return DiskInfo{}, invalidf("%s is a %s, not a whole disk", path, devs[0].Type)
	}
	return diskInfo(devs[0]), nil
}

// EnsureDiskEmpty refuses a disk that holds anything.
func EnsureDiskEmpty(ctx context.Context, r Runner, path string) error {
	info, err := InspectDisk(ctx, r, path)
	if err != nil {
		return err
	}
	if !info.Empty {
		return fmt.Errorf("%w: disk %s is not empty (%s), so it was left untouched. Wipe it yourself if you are sure (wipefs -a)", ErrDiskNotEmpty, path, info.RefusalReason)
	}
	return nil
}

// ListDisks lists this host's whole disks (no loop, RAM, RBD or NBD devices).
func ListDisks(ctx context.Context, r Runner) ([]DiskInfo, error) {
	devs, err := lsblk(ctx, r, "")
	if err != nil {
		return nil, err
	}
	out := []DiskInfo{}
	for _, d := range devs {
		if d.Type != "disk" {
			continue
		}
		skip := false
		for _, p := range []string{"loop", "ram", "zram", "rbd", "nbd"} {
			skip = skip || strings.HasPrefix(d.Name, p)
		}
		if !skip {
			out = append(out, diskInfo(d))
		}
	}
	return out, nil
}
