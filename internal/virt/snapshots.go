package virt

import (
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
	// Machine state when taken: running, paused, shutoff, disk-snapshot, …
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

// CreateSnapshot takes a snapshot with libvirt's defaults: for a running machine its memory
// is saved too, so reverting brings it back exactly as it was.
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
	x := libvirtxml.DomainSnapshot{Name: spec.Name, Description: spec.Description}
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
