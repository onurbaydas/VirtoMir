package virt

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/digitalocean/go-libvirt"
)

// Live migration between cluster members (ADR-0020) uses libvirt's "client-managed" v3
// protocol: each host only talks to its own libvirtd and the two virtomirds carry the
// phases (begin → prepare → perform → finish → confirm) and their cookies between them.
// Memory (and disks that are not shared) flow QEMU → QEMU directly.

// MigrateFlags are what every VirtoMir live migration uses; copying disks adds
// MigrateNonSharedDisk.
const MigrateFlags = libvirt.MigrateLive | libvirt.MigratePersistDest | libvirt.MigrateUndefineSource |
	libvirt.MigrateChangeProtection | libvirt.MigrateAutoConverge

// LiveDisk is a disk of a running machine as the migration sees it.
type LiveDisk struct {
	Target   string `json:"target"`
	Path     string `json:"path"`
	CDROM    bool   `json:"cdrom"`    // read-only media: must exist on the target too
	Network  bool   `json:"network"`  // on network storage (NFS) here
	Writable bool   `json:"writable"` // the machine's own disk
}

// MigrationSource describes a running machine about to be migrated.
type MigrationSource struct {
	UUID  string     `json:"uuid"`
	Name  string     `json:"name"`
	Disks []LiveDisk `json:"disks"`
	// Networks the interfaces use; they must exist on the target.
	Networks []string `json:"networks"`
	// CPUMode of the machine: host-passthrough needs an identical CPU on the target.
	CPUMode string `json:"cpuMode"`
}

// PlanMigration checks that a machine can be live-migrated and describes its disks.
func (c *Client) PlanMigration(uuid string) (MigrationSource, error) {
	l, d, err := c.lookup(uuid)
	if err != nil {
		return MigrationSource{}, err
	}
	sum, err := summary(l, d)
	if err != nil {
		return MigrationSource{}, classify(err)
	}
	if !isOn(sum.State) && sum.State != StatePaused {
		return MigrationSource{}, refusedf("%s is not running; stopped machines are moved by copying.", d.Name)
	}
	if clones := c.linkedClones(l, formatUUID(d.UUID)); len(clones) > 0 {
		return MigrationSource{}, refusedf("%s is the template of linked clones here (%s).", d.Name, joinNames(clones))
	}
	if c.meta(l, d).LinkedFrom != "" {
		return MigrationSource{}, refusedf("%s is a linked clone: its disks need the template on this host.", d.Name)
	}
	x, err := c.parseDomain(l, d, false)
	if err != nil {
		return MigrationSource{}, err
	}
	src := MigrationSource{UUID: formatUUID(d.UUID), Name: d.Name, Disks: []LiveDisk{}, Networks: []string{}}
	if x.CPU != nil {
		src.CPUMode = x.CPU.Mode
	}
	if x.Devices == nil {
		return src, nil
	}
	for _, disk := range x.Devices.Disks {
		if disk.Source == nil || disk.Target == nil {
			continue
		}
		path, _ := diskSource(disk.Source)
		if path == "" {
			continue // empty CD-ROM drive
		}
		if disk.Source.File == nil {
			return MigrationSource{}, refusedf("Disk %s is not an image file; VirtoMir only migrates file-based disks.", disk.Target.Dev)
		}
		ld := LiveDisk{Target: disk.Target.Dev, Path: path, CDROM: disk.Device == "cdrom" || disk.ReadOnly != nil,
			Network: onNetworkFS(path), Writable: disk.ReadOnly == nil && disk.Shareable == nil && orDefault(disk.Device, "disk") == "disk"}
		if ld.Writable && !ld.Network && backingFile(path, diskFormat(disk)) != "" {
			return MigrationSource{}, refusedf("Disk %s is an overlay on another image; that chain cannot be copied live.", disk.Target.Dev)
		}
		src.Disks = append(src.Disks, ld)
	}
	for _, ifc := range x.Devices.Interfaces {
		if ifc.Source != nil && ifc.Source.Network != nil {
			src.Networks = appendUnique(src.Networks, ifc.Source.Network.Network)
		}
	}
	return src, nil
}

// PathCheck is what the target says about one disk path of an incoming machine.
type PathCheck struct {
	Path    string `json:"path"`
	Exists  bool   `json:"exists"`
	Network bool   `json:"network"` // exists on network storage (NFS) here
	// Pool: a storage pool here keeps files in that directory, so libvirt can create the
	// disk for a live copy.
	Pool string `json:"pool,omitempty"`
}

// CheckPaths answers PathCheck for each path (target side).
func (c *Client) CheckPaths(paths []string) ([]PathCheck, error) {
	pools, err := c.ListPools()
	if err != nil {
		return nil, err
	}
	out := make([]PathCheck, len(paths))
	for i, p := range paths {
		pc := PathCheck{Path: p}
		if _, err := os.Stat(p); err == nil {
			pc.Exists, pc.Network = true, onNetworkFS(p)
		}
		for _, pool := range pools {
			if pool.Active && pool.Path != "" && filepath.Clean(pool.Path) == filepath.Dir(p) {
				pc.Pool = pool.Name
			}
		}
		out[i] = pc
	}
	return out, nil
}

func typed(field, v string) libvirt.TypedParam {
	return libvirt.TypedParam{Field: field, Value: *libvirt.NewTypedParamValueString(v)}
}

// migrateParams are the typed parameters every phase gets.
func migrateParams(name, uri string, disks []string) []libvirt.TypedParam {
	ps := []libvirt.TypedParam{typed(libvirt.MigrateParamDestName, name)}
	if uri != "" {
		ps = append(ps, typed(libvirt.MigrateParamURI, uri))
	}
	for _, d := range disks {
		ps = append(ps, typed(libvirt.MigrateParamMigrateDisks, d))
	}
	return ps
}

// MigrateBegin is phase 1 (source): the domain XML and cookie for the target.
func (c *Client) MigrateBegin(uuid string, flags libvirt.DomainMigrateFlags, disks []string) ([]byte, string, error) {
	l, d, err := c.lookup(uuid)
	if err != nil {
		return nil, "", err
	}
	cookie, xml, err := l.DomainMigrateBegin3Params(d, migrateParams(d.Name, "", disks), uint32(flags))
	return cookie, xml, classify(err)
}

// MigratePrepare is phase 2 (target): QEMU starts listening at uri. Returns the cookie and
// the URI the source must send to.
func (c *Client) MigratePrepare(name, xml, uri string, cookie []byte, flags libvirt.DomainMigrateFlags, disks []string) ([]byte, string, error) {
	l, err := c.conn()
	if err != nil {
		return nil, "", err
	}
	ps := append(migrateParams(name, uri, disks), typed(libvirt.MigrateParamDestXML, xml))
	out, uriOut, err := l.DomainMigratePrepare3Params(ps, cookie, uint32(flags))
	if err != nil {
		return nil, "", classify(err)
	}
	if len(uriOut) > 0 && uriOut[0] != "" {
		uri = uriOut[0]
	}
	return out, uri, nil
}

// MigratePerform is phase 3 (source): sends memory (and copied disks) and blocks until done.
// progress gets the share transferred so far.
func (c *Client) MigratePerform(ctx context.Context, uuid, uri string, cookie []byte, flags libvirt.DomainMigrateFlags, disks []string, progress func(string)) ([]byte, error) {
	l, d, err := c.lookup(uuid)
	if err != nil {
		return nil, err
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				_ = l.DomainAbortJob(d)
				return
			case <-t.C:
			}
			_, params, err := l.DomainGetJobStats(d, 0)
			if err != nil {
				continue
			}
			var processed, total uint64
			for _, p := range params {
				switch p.Field {
				case "data_processed":
					processed = paramUint(p.Value)
				case "data_total":
					total = paramUint(p.Value)
				}
			}
			if total > 0 {
				progress(fmt.Sprintf("Migrating live: %d %% of %s", min(processed*100/total, 100), humanBytes(total)))
			}
		}
	}()
	out, err := l.DomainMigratePerform3Params(d, nil, migrateParams(d.Name, uri, disks), cookie, flags)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, classify(err)
	}
	return out, nil
}

// MigrateFinish is phase 4 (target): resumes the machine here (or, cancelled, cleans up).
func (c *Client) MigrateFinish(name, uri string, cookie []byte, flags libvirt.DomainMigrateFlags, disks []string, cancelled bool) ([]byte, error) {
	l, err := c.conn()
	if err != nil {
		return nil, err
	}
	_, out, err := l.DomainMigrateFinish3Params(migrateParams(name, uri, disks), cookie, uint32(flags), boolInt32(cancelled))
	if err != nil {
		return nil, classify(err)
	}
	return out, nil
}

// MigrateConfirm is phase 5 (source): ends the job; on success the source is undefined,
// cancelled resumes it here.
func (c *Client) MigrateConfirm(uuid string, cookie []byte, flags libvirt.DomainMigrateFlags, disks []string, cancelled bool) error {
	l, d, err := c.lookup(uuid)
	if err != nil {
		return err
	}
	err = l.DomainMigrateConfirm3Params(d, migrateParams(d.Name, "", disks), cookie, uint32(flags), boolInt32(cancelled))
	return classify(err)
}

// RemoveImage deletes a disk image that was copied to another host by a live migration
// (libvirt only undefines the source). Only images no machine here uses are removed.
func (c *Client) RemoveImage(path string) error {
	l, err := c.conn()
	if err != nil {
		return err
	}
	if users := c.diskUsers(l)[path]; len(users) > 0 {
		return refusedf("%s is still used by %s", path, joinNames(users))
	}
	vol, err := l.StorageVolLookupByPath(path)
	if err != nil {
		return classify(err)
	}
	return classify(l.StorageVolDelete(vol, libvirt.StorageVolDeleteNormal))
}

func boolInt32(b bool) int32 {
	if b {
		return 1
	}
	return 0
}

func joinNames(xs []string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += ", "
		}
		out += x
	}
	return out
}
