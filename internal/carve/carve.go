// Package carve makes a storage pool of a chosen size out of the host's free space: an
// empty disk, the free space of an LVM volume group, or a ZFS pool. The space becomes a
// file system mounted below Base on every boot, and a directory pool on it keeps qcow2
// images (thin disks, snapshots) like any other.
package carve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Runner runs a command on the host (ceph.Exec does).
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Kinds of source.
const (
	KindDisk = "disk" // an empty whole disk: becomes an LVM volume group of its own
	KindVG   = "vg"   // free space of an LVM volume group
	KindZFS  = "zfs"  // a ZFS pool: a dataset with a quota
)

// Source is free space a pool can be carved from.
type Source struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"` // disk path, volume group or zpool name
	SizeBytes uint64 `json:"sizeBytes"`
	FreeBytes uint64 `json:"freeBytes"`
	Detail    string `json:"detail,omitempty"` // e.g. "nvme", "ssd"
}

// Disk is an empty whole disk (from ceph.ListDisks, after ceph.MarkUsed).
type Disk struct {
	Path, Type string
	SizeBytes  uint64
}

// MinBytes is the smallest pool worth making.
const MinBytes = 1 << 30

// extent is LVM's default extent size; sizes are rounded down to it so a pool that asks for
// all the free space fits.
const extent = 4 << 20

var (
	// Base is where carved pools are mounted (/srv/virtomir/<pool>).
	Base = "/srv/virtomir"
	// UnitDir holds the mount units that mount them on every boot.
	UnitDir = "/etc/systemd/system"
)

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`) // LVM, ZFS and unit-safe

// Sources lists what pools can be carved from: the empty disks given, volume groups with
// free space, and ZFS pools. A host without LVM or ZFS tools simply has none of those.
func Sources(ctx context.Context, r Runner, disks []Disk) []Source {
	out := []Source{}
	for _, d := range disks {
		if d.SizeBytes >= MinBytes {
			out = append(out, Source{Kind: KindDisk, ID: d.Path, SizeBytes: d.SizeBytes, FreeBytes: d.SizeBytes, Detail: d.Type})
		}
	}
	if b, err := r.Run(ctx, "vgs", "--reportformat", "json", "--units", "b", "--nosuffix", "-o", "vg_name,vg_size,vg_free"); err == nil {
		var doc struct {
			Report []struct {
				VG []struct {
					Name string `json:"vg_name"`
					Size string `json:"vg_size"`
					Free string `json:"vg_free"`
				} `json:"vg"`
			} `json:"report"`
		}
		if json.Unmarshal(b, &doc) == nil {
			for _, rep := range doc.Report {
				for _, vg := range rep.VG {
					size, _ := strconv.ParseUint(vg.Size, 10, 64)
					free, _ := strconv.ParseUint(vg.Free, 10, 64)
					if free >= MinBytes {
						out = append(out, Source{Kind: KindVG, ID: vg.Name, SizeBytes: size, FreeBytes: free})
					}
				}
			}
		}
	}
	if b, err := r.Run(ctx, "zfs", "list", "-Hp", "-d", "0", "-o", "name,used,avail"); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			f := strings.Fields(line)
			if len(f) != 3 {
				continue
			}
			used, _ := strconv.ParseUint(f[1], 10, 64)
			avail, _ := strconv.ParseUint(f[2], 10, 64)
			if avail >= MinBytes {
				out = append(out, Source{Kind: KindZFS, ID: f[0], SizeBytes: used + avail, FreeBytes: avail})
			}
		}
	}
	return out
}

// Request asks for a pool of SizeBytes from a source.
type Request struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Source    string `json:"source"`
	SizeBytes uint64 `json:"sizeBytes"`
}

// ErrInvalid marks a request that cannot be carved as asked.
var ErrInvalid = errors.New("invalid request")

func invalidf(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Check validates a request against the sources there are now.
func Check(req Request, sources []Source) error {
	if !nameRE.MatchString(req.Name) || len(req.Name) > 64 {
		return invalidf("%q cannot name a carved pool: use letters, digits, dot, dash and underscore", req.Name)
	}
	var src *Source
	for i := range sources {
		if sources[i].Kind == req.Kind && sources[i].ID == req.Source {
			src = &sources[i]
		}
	}
	if src == nil {
		return invalidf("%s %s has no free space for a pool (or is no longer free)", req.Kind, req.Source)
	}
	if req.SizeBytes < MinBytes {
		return invalidf("a pool needs at least 1 GiB")
	}
	if req.SizeBytes > src.FreeBytes {
		return invalidf("%s has %d bytes free, %d asked", req.Source, src.FreeBytes, req.SizeBytes)
	}
	return nil
}

// Carve makes the file system for the pool and mounts it below Base; it returns the
// directory for the pool. report gets each step.
func Carve(ctx context.Context, r Runner, req Request, report func(string)) (string, error) {
	dir := filepath.Join(Base, req.Name)
	if _, err := os.Stat(dir); err == nil {
		return "", invalidf("%s already exists", dir)
	}
	size := req.SizeBytes / extent * extent
	run := func(step string, name string, args ...string) error {
		report(step)
		if _, err := r.Run(ctx, name, args...); err != nil {
			return fmt.Errorf("%s: %w", step, err)
		}
		return nil
	}
	if req.Kind == KindZFS {
		dataset := req.Source + "/" + req.Name
		if err := run("Creating ZFS dataset "+dataset, "zfs", "create", "-o", "quota="+strconv.FormatUint(size, 10),
			"-o", "mountpoint="+dir, dataset); err != nil {
			return "", err
		}
		relabel(ctx, r, dir)
		return dir, nil
	}

	vg := req.Source
	if req.Kind == KindDisk {
		vg = "virtomir-" + req.Name
		if err := run("Preparing "+req.Source+" for LVM", "pvcreate", "-y", req.Source); err != nil {
			return "", err
		}
		if err := run("Creating volume group "+vg, "vgcreate", vg, req.Source); err != nil {
			return "", err
		}
	}
	dev := "/dev/" + vg + "/" + req.Name
	if err := run("Creating logical volume "+dev, "lvcreate", "-y", "-W", "y", "-n", req.Name,
		"-L", strconv.FormatUint(size, 10)+"b", vg); err != nil {
		return "", err
	}
	if err := run("Making an XFS file system on "+dev, "mkfs.xfs", "-q", dev); err != nil {
		return "", err
	}
	b, err := r.Run(ctx, "blkid", "-s", "UUID", "-o", "value", dev)
	uuid := strings.TrimSpace(string(b))
	if err != nil || uuid == "" {
		return "", fmt.Errorf("read the file system UUID of %s: %v", dev, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	b, err = r.Run(ctx, "systemd-escape", "-p", "--suffix=mount", dir)
	unit := strings.TrimSpace(string(b))
	if err != nil || unit == "" {
		return "", fmt.Errorf("name the mount unit for %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(UnitDir, unit), []byte(mountUnit(uuid, dir, dev)), 0o644); err != nil {
		return "", err
	}
	if err := run("Reloading systemd", "systemctl", "daemon-reload"); err != nil {
		return "", err
	}
	if err := run("Mounting it at "+dir, "systemctl", "enable", "--now", unit); err != nil {
		return "", err
	}
	relabel(ctx, r, dir)
	return dir, nil
}

// mountUnit mounts the file system on every boot; nofail: a missing disk must not stop the
// host from booting (the pool then just does not start).
func mountUnit(uuid, dir, dev string) string {
	return fmt.Sprintf(`# Storage pool carved by VirtoMir from %s.
[Unit]
Description=VirtoMir storage pool %s

[Mount]
What=/dev/disk/by-uuid/%s
Where=%s
Type=xfs
Options=defaults,nofail

[Install]
WantedBy=local-fs.target
`, dev, filepath.Base(dir), uuid, dir)
}

// relabel gives the new file system the SELinux label for VM images (virtomir.cil); a host
// without SELinux has no restorecon or ignores it.
func relabel(ctx context.Context, r Runner, dir string) {
	_, _ = r.Run(ctx, "restorecon", filepath.Dir(dir))
	_, _ = r.Run(ctx, "restorecon", "-R", dir)
}
