package virt

import (
	"fmt"
	"path"
	"slices"
	"strconv"
	"time"

	"github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"
)

// Snapshot is one point in a machine's snapshot tree.
type Snapshot struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parent      string `json:"parent,omitempty"`
	// Machine state when taken: running, paused, shutoff, disk-snapshot, â€¦
	State   string `json:"state"`
	Created int64  `json:"created"` // unix seconds
	Current bool   `json:"current"`
	// Memory: the running state (RAM) was saved too, so reverting resumes where it was.
	Memory   bool   `json:"memory"`
	Location string `json:"location"` // internal (inside the qcow2) or external (overlay files)
}

func (c *Client) ListSnapshots(uuid string) ([]Snapshot, error) {
	l, d, err := c.lookup(uuid)
	if err != nil {
		return nil, err
	}
	snaps, _, err := l.DomainListAllSnapshots(d, 1, 0)
	if err != nil {
		return nil, classify(err)
	}
	current := ""
	if cur, err := l.DomainSnapshotCurrent(d, 0); err == nil {
		current = cur.Name
	}
	out := make([]Snapshot, 0, len(snaps))
	for _, s := range snaps {
		raw, err := l.DomainSnapshotGetXMLDesc(s, 0)
		if err != nil {
			continue
		}
		snap, err := parseSnapshot(raw)
		if err != nil {
			continue
		}
		snap.Current = snap.Name == current
		out = append(out, snap)
	}
	return out, nil
}

func parseSnapshot(raw string) (Snapshot, error) {
	var x libvirtxml.DomainSnapshot
	if err := x.Unmarshal(raw); err != nil {
		return Snapshot{}, err
	}
	s := Snapshot{Name: x.Name, Description: x.Description, State: x.State, Location: "internal"}
	if x.Parent != nil {
		s.Parent = x.Parent.Name
	}
	s.Created, _ = strconv.ParseInt(x.CreationTime, 10, 64)
	if x.Memory != nil {
		s.Memory = x.Memory.Snapshot == "internal" || x.Memory.Snapshot == "external"
		if x.Memory.Snapshot == "external" {
			s.Location = "external"
		}
	} else {
		s.Memory = x.State == "running" || x.State == "paused"
	}
	if x.Disks != nil {
		for _, d := range x.Disks.Disks {
			if d.Snapshot == "external" {
				s.Location = "external"
			}
		}
	}
	return s, nil
}

// SnapshotSpec names a new snapshot; an empty name gets a timestamp.
type SnapshotSpec struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// defaultImageDir holds the overlay and memory files of a machine without file-backed disks.
const defaultImageDir = "/var/lib/libvirt/images"

// snapshotDoc describes a new snapshot. A machine whose disks are all qcow2 files keeps
// libvirt's default (internal snapshots inside the qcow2). Any other disk (raw, LVM, ZFS,
// Ceph, …) cannot hold an internal snapshot, so the whole snapshot is external: every
// writable disk gets a qcow2 overlay and the memory goes to a file.
// Once a machine has an external snapshot, later ones are external too: its disks are then
// qcow2 overlays, and mixing internal snapshots into that chain breaks revert and delete.
// libvirt reverts and deletes external snapshots itself (block commit on delete).
func snapshotDoc(x *libvirtxml.Domain, name, desc string, hasExternal bool) libvirtxml.DomainSnapshot {
	s := libvirtxml.DomainSnapshot{Name: name, Description: desc}
	if x.Devices == nil {
		return s
	}
	internal, dir := !hasExternal, ""
	for _, d := range x.Devices.Disks {
		if !snapshotsDisk(d) {
			continue
		}
		file := d.Source != nil && d.Source.File != nil
		if file && dir == "" {
			dir = path.Dir(d.Source.File.File)
		}
		if !file || d.Driver == nil || d.Driver.Type != "qcow2" {
			internal = false
		}
	}
	if internal {
		return s
	}
	if dir == "" {
		dir = defaultImageDir
	}
	s.Memory = &libvirtxml.DomainSnapshotMemory{Snapshot: "external", File: path.Join(dir, x.Name+"."+name+".mem")}
	s.Disks = &libvirtxml.DomainSnapshotDisks{}
	for _, d := range x.Devices.Disks {
		if d.Target == nil {
			continue
		}
		sd := libvirtxml.DomainSnapshotDisk{Name: d.Target.Dev, Snapshot: "no"}
		if snapshotsDisk(d) {
			sd.Snapshot = "external"
			// File disks: libvirt names the overlay itself, next to the image.
			if d.Source == nil || d.Source.File == nil {
				sd.Driver = &libvirtxml.DomainDiskDriver{Type: "qcow2"}
				sd.Source = &libvirtxml.DomainDiskSource{File: &libvirtxml.DomainDiskSourceFile{
					File: path.Join(dir, x.Name+"-"+d.Target.Dev+"."+name+".qcow2")}}
			}
		}
		s.Disks.Disks = append(s.Disks.Disks, sd)
	}
	return s
}

// snapshotsDisk reports whether a disk is part of a snapshot: writable disks, not CD-ROMs,
// floppies or disks shared with other machines.
func snapshotsDisk(d libvirtxml.DomainDisk) bool {
	return (d.Device == "" || d.Device == "disk") && d.ReadOnly == nil && d.Shareable == nil && d.Target != nil
}

// CreateSnapshot takes a snapshot: for a running machine its memory is saved too, so
// reverting brings it back exactly as it was (see snapshotDoc for where it is kept).
func (c *Client) CreateSnapshot(uuid string, spec SnapshotSpec) (string, error) {
	if spec.Name == "" {
		spec.Name = "snapshot-" + time.Now().Format("20060102-150405")
	}
	if err := checkName("Snapshot", spec.Name); err != nil {
		return "", err
	}
	l, d, err := c.lookup(uuid)
	if err != nil {
		return "", err
	}
	raw, err := l.DomainGetXMLDesc(d, 0)
	if err != nil {
		return "", classify(err)
	}
	var dom libvirtxml.Domain
	if err := dom.Unmarshal(raw); err != nil {
		return "", fmt.Errorf("parse domain XML: %w", err)
	}
	active, err := l.DomainIsActive(d)
	if err != nil {
		return "", classify(err)
	}
	existing, err := c.ListSnapshots(uuid)
	if err != nil {
		return "", err
	}
	hasExternal := slices.ContainsFunc(existing, func(s Snapshot) bool { return s.Location == "external" })
	x := snapshotDoc(&dom, spec.Name, spec.Description, hasExternal)
	// libvirt (11.x) cannot delete an external snapshot taken while the machine was off: its
	// overlay is created root-owned and the later block commit cannot open it.
	if x.Disks != nil && active != 1 {
		return "", refusedf("Start %s to take a snapshot: its snapshots are kept in files next to its disks (its disks are not all qcow2), and those can only be taken while it runs.", dom.Name)
	}
	doc, err := x.Marshal()
	if err != nil {
		return "", err
	}
	if _, err := l.DomainSnapshotCreateXML(d, doc, 0); err != nil {
		return "", classify(err)
	}
	c.log.Info("snapshot created", "domain", d.Name, "snapshot", spec.Name)
	return spec.Name, nil
}

// RevertSnapshot returns the machine to the snapshot. Changes since then are lost.
func (c *Client) RevertSnapshot(uuid, name string) error {
	l, d, err := c.lookup(uuid)
	if err != nil {
		return err
	}
	if err := c.guardTemplate(l, d, "go back to a snapshot"); err != nil {
		return err
	}
	s, err := l.DomainSnapshotLookupByName(d, name, 0)
	if err != nil {
		return classify(err)
	}
	if err := l.DomainRevertToSnapshot(s, 0); err != nil {
		return classify(err)
	}
	c.log.Info("snapshot reverted", "domain", d.Name, "snapshot", name)
	return nil
}

// DeleteSnapshot removes one snapshot; its children are re-attached to its parent.
func (c *Client) DeleteSnapshot(uuid, name string) error {
	l, d, err := c.lookup(uuid)
	if err != nil {
		return err
	}
	s, err := l.DomainSnapshotLookupByName(d, name, 0)
	if err != nil {
		return classify(err)
	}
	if err := l.DomainSnapshotDelete(s, libvirt.DomainSnapshotDeleteFlags(0)); err != nil {
		return classify(err)
	}
	c.log.Info("snapshot deleted", "domain", d.Name, "snapshot", name)
	return nil
}
