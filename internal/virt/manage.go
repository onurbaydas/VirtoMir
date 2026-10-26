package virt

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"slices"
	"strings"

	"github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"
)

// DefineResult tells the UI how a saved definition takes effect.
type DefineResult struct {
	// The machine is running: the new definition is used from its next boot on.
	AppliesOnNextBoot bool `json:"appliesOnNextBoot"`
}

// DefineXML replaces the persistent definition of an existing machine with the given XML,
// validated by libvirt against its schema. The UUID and the name must stay the same: a
// different UUID would silently create a second machine, renaming has its own action.
func (c *Client) DefineXML(uuid, xmlDoc string) (DefineResult, error) {
	var x libvirtxml.Domain
	if err := x.Unmarshal(xmlDoc); err != nil {
		return DefineResult{}, invalidf("The XML is not valid: %v", err)
	}
	l, d, err := c.lookup(uuid)
	if err != nil {
		return DefineResult{}, err
	}
	if !strings.EqualFold(x.UUID, formatUUID(d.UUID)) {
		return DefineResult{}, invalidf("The XML must keep <uuid>%s</uuid>. To make a copy of this machine, use Clone.", formatUUID(d.UUID))
	}
	if x.Name != d.Name {
		return DefineResult{}, invalidf("The XML must keep <name>%s</name>. Use Rename to change the name.", d.Name)
	}
	sum, err := summary(l, d)
	if err != nil {
		return DefineResult{}, classify(err)
	}
	if _, err := l.DomainDefineXMLFlags(xmlDoc, libvirt.DomainDefineValidate); err != nil {
		return DefineResult{}, classify(err)
	}
	c.log.Info("domain redefined", "domain", d.Name)
	return DefineResult{AppliesOnNextBoot: isOn(sum.State)}, nil
}

// SetAutostart makes the machine start (or not) when the host boots.
func (c *Client) SetAutostart(uuid string, on bool) error {
	l, d, err := c.lookup(uuid)
	if err != nil {
		return err
	}
	if err := l.DomainSetAutostart(d, boolInt(on)); err != nil {
		return classify(err)
	}
	return nil
}

// Rename gives a shut-off machine a new name.
func (c *Client) Rename(uuid, name string) error {
	if err := checkName("Machine", name); err != nil {
		return err
	}
	l, d, err := c.lookup(uuid)
	if err != nil {
		return err
	}
	if d.Name == name {
		return nil
	}
	sum, err := summary(l, d)
	if err != nil {
		return classify(err)
	}
	if isOn(sum.State) {
		return refusedf("Shut %s down before renaming it.", d.Name)
	}
	if _, err := l.DomainRename(d, libvirt.OptString{name}, 0); err != nil {
		return classify(err)
	}
	c.log.Info("domain renamed", "from", d.Name, "to", name)
	return nil
}

// DeleteResult reports what happened to the machine's disks.
type DeleteResult struct {
	RemovedVolumes []string     `json:"removedVolumes"`
	KeptVolumes    []KeptVolume `json:"keptVolumes"`
}

type KeptVolume struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Delete removes a stopped machine: its definition, snapshot and checkpoint metadata, UEFI
// variables, TPM state and managed-save image. With removeStorage its writable disks are
// deleted too, unless another machine uses them or they are not in a storage pool.
func (c *Client) Delete(uuid string, removeStorage bool) (DeleteResult, error) {
	res := DeleteResult{RemovedVolumes: []string{}, KeptVolumes: []KeptVolume{}}
	l, d, err := c.lookup(uuid)
	if err != nil {
		return res, err
	}
	release, err := c.leases.take("delete", true, machineKey(formatUUID(d.UUID)))
	if err != nil {
		return res, err
	}
	defer release()
	sum, err := summary(l, d)
	if err != nil {
		return res, classify(err)
	}
	if isOn(sum.State) || sum.State == StatePaused {
		return res, refusedf("Stop %s before deleting it.", d.Name)
	}
	if clones := c.linkedClones(l, uuid); len(clones) > 0 {
		return res, refusedf("%s is the template of linked clones that read its disks: %s. Delete them first.", d.Name, strings.Join(clones, ", "))
	}
	if c.isLXC(l) { // a container: its storage is the root file system VirtoMir unpacked
		if removeStorage {
			if err := c.removeContainerFiles(l, d); err != nil {
				return res, fmt.Errorf("remove the files of %s: %w", d.Name, err)
			}
		}
		if err := l.DomainUndefine(d); err != nil {
			return res, classify(err)
		}
		c.log.Info("container deleted", "domain", d.Name, "removeStorage", removeStorage)
		return res, nil
	}
	var disks []string
	var inUse map[string][]string
	if removeStorage {
		x, err := c.parseDomain(l, d, true)
		if err != nil {
			return res, err
		}
		disks = deletableDisks(x)
		// Checked before undefining: if it cannot be known who else uses the disks, nothing
		// is deleted at all.
		if len(disks) > 0 {
			if inUse, err = c.diskUsers(l); err != nil {
				return res, refusedf("Could not check which other machines use the disks of %s, so nothing was deleted: %v", d.Name, err)
			}
		}
	}
	flags := libvirt.DomainUndefineManagedSave | libvirt.DomainUndefineSnapshotsMetadata |
		libvirt.DomainUndefineCheckpointsMetadata | libvirt.DomainUndefineNvram | libvirt.DomainUndefineTpm
	if err := l.DomainUndefineFlags(d, flags); err != nil {
		return res, classify(err)
	}
	c.log.Info("domain deleted", "domain", d.Name, "removeStorage", removeStorage)

	if len(disks) == 0 {
		return res, nil
	}
	for _, p := range disks {
		users := slices.DeleteFunc(slices.Clone(inUse[p]), func(u string) bool { return u == d.Name })
		if len(users) > 0 {
			res.KeptVolumes = append(res.KeptVolumes, KeptVolume{p, "also used by " + strings.Join(users, ", ")})
			continue
		}
		vol, err := l.StorageVolLookupByPath(p)
		if err != nil {
			res.KeptVolumes = append(res.KeptVolumes, KeptVolume{p, "not in a storage pool"})
			continue
		}
		if backers, err := c.volumeBackers(l, vol, p); err != nil {
			res.KeptVolumes = append(res.KeptVolumes, KeptVolume{p, "could not check whether other images are based on it: " + err.Error()})
			continue
		} else if len(backers) > 0 {
			res.KeptVolumes = append(res.KeptVolumes, KeptVolume{p, "the base image of " + strings.Join(backers, ", ")})
			continue
		}
		if err := l.StorageVolDelete(vol, libvirt.StorageVolDeleteNormal); err != nil {
			res.KeptVolumes = append(res.KeptVolumes, KeptVolume{p, classify(err).Error()})
			continue
		}
		res.RemovedVolumes = append(res.RemovedVolumes, p)
	}
	return res, nil
}

// deletableDisks are the machine's own writable disks plus the cloud-init seed VirtoMir made
// for it. Other CD-ROMs, floppies, read-only and shared disks (and backing images) belong to
// someone else.
func deletableDisks(x *libvirtxml.Domain) []string {
	var out []string
	if x.Devices == nil {
		return out
	}
	for _, v := range x.Devices.Disks {
		if isSeedISO(v) {
			out = append(out, v.Source.File.File)
			continue
		}
		if (v.Device != "" && v.Device != "disk") || v.ReadOnly != nil || v.Shareable != nil || v.Source == nil {
			continue
		}
		switch {
		case v.Source.File != nil && v.Source.File.File != "":
			out = append(out, v.Source.File.File)
		case v.Source.Block != nil && v.Source.Block.Dev != "":
			out = append(out, v.Source.Block.Dev)
		}
	}
	return out
}

// diskUsers maps disk image paths to the machines that reference them. It fails closed: when
// the machines cannot be listed or one definition cannot be read, the error says so (the map
// then holds what could be read, good enough for display but not for deleting anything).
func (c *Client) diskUsers(l *libvirt.Libvirt) (map[string][]string, error) {
	out := map[string][]string{}
	doms, _, err := l.ConnectListAllDomains(1, libvirt.ConnectListDomainsActive|libvirt.ConnectListDomainsInactive)
	if err != nil {
		return out, classify(err)
	}
	var errs []error
	// Linked clones read their template's disks; libvirt only shows that chain while they run.
	ownDisks := map[string][]string{}
	linked := map[string][]string{}
	for _, d := range doms {
		x, err := c.parseDomain(l, d, false)
		if err != nil {
			errs = append(errs, fmt.Errorf("read the definition of %s: %w", d.Name, err))
			continue
		}
		if x.Devices == nil {
			continue
		}
		if t := c.meta(l, d).LinkedFrom; t != "" {
			linked[strings.ToLower(t)] = append(linked[strings.ToLower(t)], d.Name)
		}
		for _, v := range x.Devices.Disks {
			if v.Device == "" || v.Device == "disk" {
				if src, _ := diskSource(v.Source); src != "" {
					ownDisks[formatUUID(d.UUID)] = append(ownDisks[formatUUID(d.UUID)], src)
				}
			}
			if src, _ := diskSource(v.Source); src != "" {
				out[src] = append(out[src], d.Name)
			}
			if v.BackingStore != nil {
				if src, _ := diskSource(v.BackingStore.Source); src != "" {
					out[src] = append(out[src], d.Name+" (as backing image)")
				}
			}
		}
	}
	for t, clones := range linked {
		for _, src := range ownDisks[t] {
			for _, name := range clones {
				if !slices.Contains(out[src], name+" (as backing image)") {
					out[src] = append(out[src], name+" (as backing image)")
				}
			}
		}
	}
	return out, errors.Join(errs...)
}

func (c *Client) parseDomain(l *libvirt.Libvirt, d libvirt.Domain, inactive bool) (*libvirtxml.Domain, error) {
	var flags libvirt.DomainXMLFlags
	if inactive {
		flags = libvirt.DomainXMLInactive
	}
	raw, err := l.DomainGetXMLDesc(d, flags)
	if err != nil {
		return nil, classify(err)
	}
	var x libvirtxml.Domain
	if err := x.Unmarshal(raw); err != nil {
		return nil, fmt.Errorf("parse domain XML: %w", err)
	}
	return &x, nil
}

// Clone copies a shut-off machine under a new name: new UUID and MAC addresses, fresh UEFI
// variables and TPM, and a full copy of every writable disk in the same storage pool.
// Copying can take minutes; progress is reported through report.
func (c *Client) Clone(ctx context.Context, uuid, name string, report func(string)) (string, error) {
	if err := checkName("Machine", name); err != nil {
		return "", err
	}
	l, d, err := c.lookup(uuid)
	if err != nil {
		return "", err
	}
	release, err := c.leases.take("clone", true, machineKey(formatUUID(d.UUID)), nameKey(name))
	if err != nil {
		return "", err
	}
	defer release()
	if _, err := l.DomainLookupByName(name); err == nil {
		return "", refusedf("A machine named %s already exists.", name)
	}
	sum, err := summary(l, d)
	if err != nil {
		return "", classify(err)
	}
	if sum.State != StateShutoff && sum.State != StateCrashed {
		return "", refusedf("Shut %s down first: a running machine's disks cannot be copied consistently.", d.Name)
	}
	x, err := c.parseDomain(l, d, true)
	if err != nil {
		return "", err
	}
	prepareClone(x, name)

	var created []libvirt.StorageVol
	rollback := func() {
		for _, v := range created {
			dropVolume(c.log, l, v)
		}
	}
	// The copies belong to no machine until the clone is defined: mark them as being written.
	var writing []func()
	defer func() {
		for _, done := range writing {
			done()
		}
	}()
	if x.Devices != nil {
		for i := range x.Devices.Disks {
			disk := &x.Devices.Disks[i]
			if (disk.Device != "" && disk.Device != "disk") || disk.ReadOnly != nil || disk.Shareable != nil {
				continue
			}
			if disk.Source == nil || disk.Source.File == nil || disk.Source.File.File == "" {
				if disk.Source != nil && disk.Source.Volume != nil {
					rollback()
					return "", refusedf("Disk %s refers to a pool volume by name; clone supports file-based disks only for now.", targetOf(disk))
				}
				continue
			}
			if err := ctx.Err(); err != nil {
				rollback()
				return "", err
			}
			src := disk.Source.File.File
			report(fmt.Sprintf("Copying disk %s (%s)…", targetOf(disk), path.Base(src)))
			writing = append(writing, inFlight.add(path.Join(path.Dir(src), cloneVolName(name, disk))))
			vol, newPath, err := copyVolume(l, src, cloneVolName(name, disk))
			if err != nil {
				rollback()
				return "", err
			}
			created = append(created, vol)
			disk.Source.File.File = newPath
			disk.BackingStore = nil // the copy is a standalone image
		}
	}
	out, err := x.Marshal()
	if err != nil {
		rollback()
		return "", err
	}
	report("Defining " + name + "…")
	nd, err := l.DomainDefineXMLFlags(out, libvirt.DomainDefineValidate)
	if err != nil {
		rollback()
		return "", classify(err)
	}
	c.log.Info("domain cloned", "from", d.Name, "to", name)
	return formatUUID(nd.UUID), nil
}

// prepareClone strips everything that must be unique per machine.
func prepareClone(x *libvirtxml.Domain, name string) {
	x.Name, x.UUID, x.ID, x.HWUUID, x.GenID = name, "", nil, "", nil
	if x.Title != "" {
		x.Title = name
	}
	if x.OS != nil && x.OS.NVRam != nil {
		// Keep the template: libvirt creates fresh variables for the new machine.
		x.OS.NVRam.NVRam, x.OS.NVRam.Source = "", nil
	}
	if x.Devices == nil {
		return
	}
	for i := range x.Devices.Interfaces {
		x.Devices.Interfaces[i].MAC = nil // libvirt generates new addresses
		x.Devices.Interfaces[i].Target = nil
	}
	for i := range x.Devices.Graphics {
		g := &x.Devices.Graphics[i]
		if g.VNC != nil && g.VNC.Port > 0 {
			g.VNC.Port, g.VNC.AutoPort = -1, "yes"
		}
		if g.Spice != nil && g.Spice.Port > 0 {
			g.Spice.Port, g.Spice.AutoPort = -1, "yes"
		}
	}
}

func targetOf(d *libvirtxml.DomainDisk) string {
	if d.Target != nil {
		return d.Target.Dev
	}
	return "?"
}

// cloneVolName is "<machine>-<target>.<ext>", e.g. web2-vda.qcow2.
func cloneVolName(machine string, d *libvirtxml.DomainDisk) string {
	ext := "img"
	if d.Driver != nil && d.Driver.Type == "qcow2" {
		ext = "qcow2"
	}
	return fmt.Sprintf("%s-%s.%s", machine, targetOf(d), ext)
}

// copyVolume creates a full copy of the pool volume at src in the same pool.
func copyVolume(l *libvirt.Libvirt, src, newName string) (libvirt.StorageVol, string, error) {
	srcVol, err := l.StorageVolLookupByPath(src)
	if err != nil {
		return libvirt.StorageVol{}, "", refusedf("%s is not in a storage pool, so VirtoMir cannot copy it. Add its directory as a pool first.", src)
	}
	pool, err := l.StoragePoolLookupByName(srcVol.Pool)
	if err != nil {
		return libvirt.StorageVol{}, "", classify(err)
	}
	raw, err := l.StorageVolGetXMLDesc(srcVol, 0)
	if err != nil {
		return libvirt.StorageVol{}, "", classify(err)
	}
	var sv libvirtxml.StorageVolume
	if err := sv.Unmarshal(raw); err != nil {
		return libvirt.StorageVol{}, "", err
	}
	nv := libvirtxml.StorageVolume{Name: newName, Capacity: sv.Capacity}
	if sv.Target != nil && sv.Target.Format != nil {
		nv.Target = &libvirtxml.StorageVolumeTarget{Format: &libvirtxml.StorageVolumeTargetFormat{Type: sv.Target.Format.Type}}
	}
	doc, err := nv.Marshal()
	if err != nil {
		return libvirt.StorageVol{}, "", err
	}
	vol, err := l.StorageVolCreateXMLFrom(pool, doc, srcVol, 0)
	if err != nil {
		return libvirt.StorageVol{}, "", classify(err)
	}
	p, err := l.StorageVolGetPath(vol)
	if err != nil {
		dropVolume(slog.Default(), l, vol)
		return libvirt.StorageVol{}, "", classify(err)
	}
	return vol, p, nil
}

func isOn(s DomainState) bool {
	return s == StateRunning || s == StateBlocked || s == StateShuttingDown
}

func boolInt(b bool) int32 {
	if b {
		return 1
	}
	return 0
}
