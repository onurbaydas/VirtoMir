package virt

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"
)

// MovePlan is what moving a stopped machine to another host takes: its definition and the
// disk images to copy (ADR-0020, cold move). Building it checks everything that would make
// the move fail halfway.
type MovePlan struct {
	UUID     string     `json:"uuid"`
	Name     string     `json:"name"`
	XML      string     `json:"-"`
	Disks    []MoveDisk `json:"disks"`
	Networks []string   `json:"networks"` // libvirt networks the interfaces use
	Bridges  []string   `json:"bridges"`  // host bridges the interfaces use (not checked)
	// Notes are things that do not travel (UEFI variables, TPM state, inserted media).
	Notes []string `json:"notes"`
}

type MoveDisk struct {
	Target string `json:"target"`
	Path   string `json:"path"`
	Size   int64  `json:"size"` // bytes on disk, what is copied
}

// PlanMove checks that a machine can be moved and lists what has to be copied.
func (c *Client) PlanMove(uuid string) (MovePlan, error) {
	l, d, err := c.lookup(uuid)
	if err != nil {
		return MovePlan{}, err
	}
	sum, err := summary(l, d)
	if err != nil {
		return MovePlan{}, classify(err)
	}
	if sum.State != StateShutoff {
		return MovePlan{}, refusedf("Shut %s down first: only stopped machines can be moved this way.", d.Name)
	}
	if clones := c.linkedClones(l, formatUUID(d.UUID)); len(clones) > 0 {
		return MovePlan{}, refusedf("%s is the template of linked clones on this host (%s); they need its disks here.", d.Name, strings.Join(clones, ", "))
	}
	if c.meta(l, d).LinkedFrom != "" {
		return MovePlan{}, refusedf("%s is a linked clone: its disks need the template on this host. Make a full copy and move that instead.", d.Name)
	}
	if n, err := l.DomainSnapshotListNames(d, 1, 0); err == nil && len(n) > 0 {
		return MovePlan{}, refusedf("%s has snapshots, which cannot move with it. Delete them first (or back the machine up).", d.Name)
	}
	x, err := c.parseDomain(l, d, true)
	if err != nil {
		return MovePlan{}, err
	}
	p := MovePlan{UUID: formatUUID(d.UUID), Name: d.Name, Disks: []MoveDisk{}, Networks: []string{}, Bridges: []string{}, Notes: []string{}}
	for _, disk := range backupDisks(x) {
		if disk.Source.File == nil {
			return MovePlan{}, refusedf("Disk %s of %s is not an image file (a block device or network disk); it cannot be copied to another host.", disk.Target.Dev, d.Name)
		}
		path := disk.Source.File.File
		if backing := backingFile(path, diskFormat(disk)); backing != "" {
			return MovePlan{}, refusedf("Disk %s of %s is an overlay on %s; make a full copy of the machine and move that.", disk.Target.Dev, d.Name, backing)
		}
		st, err := os.Stat(path)
		if err != nil {
			return MovePlan{}, refusedf("Cannot read disk %s (%s): %v", disk.Target.Dev, path, err)
		}
		p.Disks = append(p.Disks, MoveDisk{Target: disk.Target.Dev, Path: path, Size: st.Size()})
	}
	if x.Devices != nil {
		for _, ifc := range x.Devices.Interfaces {
			switch {
			case ifc.Source != nil && ifc.Source.Network != nil:
				p.Networks = appendUnique(p.Networks, ifc.Source.Network.Network)
			case ifc.Source != nil && ifc.Source.Bridge != nil:
				p.Bridges = appendUnique(p.Bridges, ifc.Source.Bridge.Bridge)
			}
		}
		if len(x.Devices.TPMs) > 0 {
			p.Notes = append(p.Notes, "The TPM starts empty on the new host (its state stays here).")
		}
	}
	if x.OS != nil && x.OS.NVRam != nil {
		p.Notes = append(p.Notes, "UEFI settings (boot entries) start from defaults on the new host.")
	}
	p.XML, err = x.Marshal()
	return p, err
}

// RewriteForMove points the definition at the copied disks (old path → new path) and ejects
// CD/DVD media, which stay on the old host. It returns notes for the user.
func RewriteForMove(doc string, paths map[string]string) (string, []string, error) {
	var x libvirtxml.Domain
	if err := x.Unmarshal(doc); err != nil {
		return "", nil, err
	}
	var notes []string
	if x.Devices != nil {
		for i := range x.Devices.Disks {
			disk := &x.Devices.Disks[i]
			if disk.Source == nil || disk.Source.File == nil {
				continue
			}
			if np, ok := paths[disk.Source.File.File]; ok {
				disk.Source.File.File = np
				continue
			}
			if disk.Device == "cdrom" || disk.Device == "floppy" {
				notes = append(notes, "Ejected "+filepath.Base(disk.Source.File.File)+" (it stays on the old host).")
				disk.Source = nil
			}
		}
	}
	if x.OS != nil && x.OS.NVRam != nil {
		// Keep the template (e.g. Secure Boot keys); libvirt creates a fresh variables file.
		x.OS.NVRam.NVRam = ""
	}
	out, err := x.Marshal()
	return out, notes, err
}

// AdoptDomain defines a machine moved here from another host. Name and UUID must be free.
func (c *Client) AdoptDomain(doc string) (string, error) {
	var x libvirtxml.Domain
	if err := x.Unmarshal(doc); err != nil {
		return "", invalidf("The XML is not valid: %v", err)
	}
	l, err := c.conn()
	if err != nil {
		return "", err
	}
	if u, err := parseUUID(x.UUID); err == nil {
		if d, err := l.DomainLookupByUUID(u); err == nil {
			return "", refusedf("A machine with this UUID (%s) already exists here.", d.Name)
		}
	}
	if _, err := l.DomainLookupByName(x.Name); err == nil {
		return "", refusedf("A machine named %s already exists here.", x.Name)
	}
	d, err := l.DomainDefineXMLFlags(doc, libvirt.DomainDefineValidate)
	if err != nil {
		return "", classify(err)
	}
	c.log.Info("domain adopted", "domain", d.Name)
	return formatUUID(d.UUID), nil
}

// backingFile is the backing image of a disk opened as format ("" for none or when it cannot
// be read). The format comes from the domain definition: probing would let a raw disk's
// contents pretend to be an overlay (see imagefmt.go).
func backingFile(path, format string) string {
	chain, err := qemuImgInfo(context.Background(), path, format)
	if err != nil || len(chain) == 0 {
		return ""
	}
	return chain[0].Backing
}

func appendUnique(xs []string, s string) []string {
	for _, x := range xs {
		if x == s {
			return xs
		}
	}
	return append(xs, s)
}
