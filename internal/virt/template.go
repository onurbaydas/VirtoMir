package virt

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"
)

// VirtoMir keeps its own per-machine facts in the domain's <metadata>, under this namespace,
// so they travel with the definition (virsh dumpxml, backups) and need no separate store.
const (
	metaURI = "https://github.com/onurbaydas/virtomir/xmlns/vm/1"
	metaKey = "virtomir"
)

// vmMeta is <virtomir:vm template="yes" linked-from="<template uuid>"/>.
type vmMeta struct {
	XMLName    xml.Name `xml:"vm"`
	Template   bool     `xml:"template,attr,omitempty"`
	LinkedFrom string   `xml:"linked-from,attr,omitempty"`
}

func (m vmMeta) empty() bool { return !m.Template && m.LinkedFrom == "" }

// metaCache spares the 1-second domain list a metadata RPC per machine. Entries VirtoMir
// writes itself are updated immediately; changes made elsewhere (virsh) show up within metaTTL.
type metaCache struct {
	mu sync.Mutex
	m  map[string]metaEntry
}

type metaEntry struct {
	v  vmMeta
	at time.Time
}

const metaTTL = time.Minute

func (c *Client) meta(l *libvirt.Libvirt, d libvirt.Domain) vmMeta {
	key := formatUUID(d.UUID)
	c.metas.mu.Lock()
	e, ok := c.metas.m[key]
	c.metas.mu.Unlock()
	if ok && time.Since(e.at) < metaTTL {
		return e.v
	}
	v, err := readMeta(l, d)
	if err != nil {
		c.log.Debug("read metadata", "domain", d.Name, "err", err)
		return e.v // keep what we knew
	}
	c.remember(key, v)
	return v
}

func (c *Client) addMeta(l *libvirt.Libvirt, d libvirt.Domain, s *Domain) {
	m := c.meta(l, d)
	s.Template, s.LinkedFrom = m.Template, m.LinkedFrom
}

func (c *Client) remember(uuid string, v vmMeta) {
	c.metas.mu.Lock()
	defer c.metas.mu.Unlock()
	if c.metas.m == nil {
		c.metas.m = map[string]metaEntry{}
	}
	c.metas.m[uuid] = metaEntry{v, time.Now()}
}

func readMeta(l *libvirt.Libvirt, d libvirt.Domain) (vmMeta, error) {
	raw, err := l.DomainGetMetadata(d, int32(libvirt.DomainMetadataElement), libvirt.OptString{metaURI}, libvirt.DomainAffectConfig)
	var v vmMeta
	if err != nil {
		if isErr(err, libvirt.ErrNoDomainMetadata) {
			return v, nil
		}
		return v, err
	}
	if err := xml.Unmarshal([]byte(raw), &v); err != nil {
		return vmMeta{}, fmt.Errorf("parse metadata: %w", err)
	}
	return v, nil
}

func (c *Client) writeMeta(l *libvirt.Libvirt, d libvirt.Domain, v vmMeta) error {
	var doc libvirt.OptString
	if !v.empty() {
		b, err := xml.Marshal(v)
		if err != nil {
			return err
		}
		doc = libvirt.OptString{string(b)}
	}
	if err := l.DomainSetMetadata(d, int32(libvirt.DomainMetadataElement), doc, libvirt.OptString{metaKey}, libvirt.OptString{metaURI}, libvirt.DomainAffectConfig); err != nil {
		return classify(err)
	}
	c.remember(formatUUID(d.UUID), v)
	return nil
}

func isErr(err error, code libvirt.ErrorNumber) bool {
	var le libvirt.Error
	return errors.As(err, &le) && le.Code == uint32(code)
}

// linkedClones names the machines whose disks are overlays on the template's disks.
func (c *Client) linkedClones(l *libvirt.Libvirt, template string) []string {
	var out []string
	doms, _, err := l.ConnectListAllDomains(1, libvirt.ConnectListDomainsActive|libvirt.ConnectListDomainsInactive)
	if err != nil {
		return out
	}
	for _, d := range doms {
		if strings.EqualFold(c.meta(l, d).LinkedFrom, template) {
			out = append(out, d.Name)
		}
	}
	return out
}

// SetTemplate turns a shut-off machine into a template or back. A template never starts:
// linked clones read its disks, and a single boot would change what they see.
func (c *Client) SetTemplate(uuid string, on bool) error {
	l, d, err := c.lookup(uuid)
	if err != nil {
		return err
	}
	m, err := readMeta(l, d)
	if err != nil {
		return classify(err)
	}
	if m.Template == on {
		return nil
	}
	if on {
		sum, err := summary(l, d)
		if err != nil {
			return classify(err)
		}
		if sum.State != StateShutoff && sum.State != StateCrashed {
			return refusedf("Shut %s down first: a template is a machine at rest.", d.Name)
		}
		if sum.Autostart {
			if err := l.DomainSetAutostart(d, 0); err != nil {
				return classify(err)
			}
		}
	} else if clones := c.linkedClones(l, uuid); len(clones) > 0 {
		return refusedf("%s cannot become a normal machine while linked clones depend on its disks: %s. Delete them first, or make full clones instead.", d.Name, strings.Join(clones, ", "))
	}
	m.Template = on
	if err := c.writeMeta(l, d, m); err != nil {
		return err
	}
	c.log.Info("template flag changed", "domain", d.Name, "template", on)
	return nil
}

// guardTemplate refuses changes that would alter a template's disks under its clones.
func (c *Client) guardTemplate(l *libvirt.Libvirt, d libvirt.Domain, what string) error {
	if c.meta(l, d).Template {
		return refusedf("%s is a template, so it cannot %s. Create a machine from it instead, or turn it back into a normal machine first.", d.Name, what)
	}
	return nil
}

// DeploySpec is a new machine made from a template.
type DeploySpec struct {
	Name string `json:"name"`
	// Linked clones get thin overlays on the template's disks: instant, small, but tied to
	// the template. Otherwise every disk is copied in full.
	Linked    bool       `json:"linked"`
	CloudInit *CloudInit `json:"cloudInit,omitempty"`
	Start     bool       `json:"start"`
}

// Deploy creates a machine from a template and returns its UUID. Long copies report progress.
func (c *Client) Deploy(ctx context.Context, template string, spec DeploySpec, report func(string)) (string, error) {
	if err := checkName("Machine", spec.Name); err != nil {
		return "", err
	}
	if spec.CloudInit != nil {
		if err := spec.CloudInit.normalize(spec.Name); err != nil {
			return "", err
		}
	}
	l, d, err := c.lookup(template)
	if err != nil {
		return "", err
	}
	if !c.meta(l, d).Template {
		return "", refusedf("%s is not a template.", d.Name)
	}
	if _, err := l.DomainLookupByName(spec.Name); err == nil {
		return "", refusedf("A machine named %s already exists.", spec.Name)
	}
	x, err := c.parseDomain(l, d, true)
	if err != nil {
		return "", err
	}
	prepareClone(x, spec.Name)

	var created []libvirt.StorageVol
	rollback := func() {
		for _, v := range created {
			_ = l.StorageVolDelete(v, libvirt.StorageVolDeleteNormal)
		}
	}
	seedPool := ""
	for _, disk := range templateDisks(x) {
		if err := ctx.Err(); err != nil {
			rollback()
			return "", err
		}
		if disk.Source == nil || disk.Source.File == nil || disk.Source.File.File == "" {
			rollback()
			return "", refusedf("Disk %s of %s is not a file image; templates support file-based disks only.", targetOf(disk), d.Name)
		}
		src := disk.Source.File.File
		var vol libvirt.StorageVol
		var newPath string
		if spec.Linked {
			report(fmt.Sprintf("Linking disk %s to %s…", targetOf(disk), path.Base(src)))
			vol, newPath, err = overlayVolume(l, src, fmt.Sprintf("%s-%s.qcow2", spec.Name, targetOf(disk)))
			disk.Driver = qcow2Driver(disk.Driver)
		} else {
			report(fmt.Sprintf("Copying disk %s (%s)…", targetOf(disk), path.Base(src)))
			vol, newPath, err = copyVolume(l, src, cloneVolName(spec.Name, disk))
		}
		if err != nil {
			rollback()
			return "", err
		}
		created = append(created, vol)
		if seedPool == "" {
			seedPool = vol.Pool
		}
		disk.Source.File.File = newPath
		disk.BackingStore = nil
	}

	if spec.CloudInit != nil {
		if seedPool == "" {
			seedPool = "default"
		}
		report("Preparing first-boot settings…")
		iso, err := spec.CloudInit.seedISO()
		if err != nil {
			rollback()
			return "", fmt.Errorf("build cloud-init seed: %w", err)
		}
		vol, p, err := uploadBytes(l, seedPool, spec.Name+"-cidata.iso", iso)
		if err != nil {
			rollback()
			return "", err
		}
		created = append(created, vol)
		setSeed(x, p)
	}

	out, err := x.Marshal()
	if err != nil {
		rollback()
		return "", err
	}
	report("Defining " + spec.Name + "…")
	nd, err := l.DomainDefineXMLFlags(out, libvirt.DomainDefineValidate)
	if err != nil {
		rollback()
		return "", classify(err)
	}
	// The definition came from a template: drop its template flag, record the link.
	m := vmMeta{}
	if spec.Linked {
		m.LinkedFrom = formatUUID(d.UUID)
	}
	if err := c.writeMeta(l, nd, m); err != nil {
		_ = l.DomainUndefineFlags(nd, libvirt.DomainUndefineNvram|libvirt.DomainUndefineTpm)
		rollback()
		return "", err
	}
	c.log.Info("machine deployed from template", "template", d.Name, "machine", spec.Name, "linked", spec.Linked)
	if spec.Start {
		report("Starting " + spec.Name + "…")
		if err := l.DomainCreate(nd); err != nil {
			return formatUUID(nd.UUID), fmt.Errorf("%s was created but did not start: %w", spec.Name, classify(err))
		}
	}
	return formatUUID(nd.UUID), nil
}

// templateDisks are the disks a new machine needs its own copy (or overlay) of.
func templateDisks(x *libvirtxml.Domain) []*libvirtxml.DomainDisk {
	var out []*libvirtxml.DomainDisk
	if x.Devices == nil {
		return out
	}
	for i := range x.Devices.Disks {
		disk := &x.Devices.Disks[i]
		if (disk.Device != "" && disk.Device != "disk") || disk.ReadOnly != nil || disk.Shareable != nil {
			continue
		}
		out = append(out, disk)
	}
	return out
}

func qcow2Driver(d *libvirtxml.DomainDiskDriver) *libvirtxml.DomainDiskDriver {
	if d == nil {
		return &libvirtxml.DomainDiskDriver{Name: "qemu", Type: "qcow2"}
	}
	nd := *d
	nd.Type = "qcow2"
	return &nd
}

// setSeed points the machine's cloud-init CD-ROM at seed, replacing the template's own seed
// (which carries the template's identity) or adding a new drive.
func setSeed(x *libvirtxml.Domain, seed string) {
	if x.Devices == nil {
		x.Devices = &libvirtxml.DomainDeviceList{}
	}
	for i := range x.Devices.Disks {
		disk := &x.Devices.Disks[i]
		if isSeedISO(*disk) {
			disk.Source.File.File = seed
			return
		}
	}
	x.Devices.Disks = append(x.Devices.Disks, libvirtxml.DomainDisk{
		Device:   "cdrom",
		Driver:   &libvirtxml.DomainDiskDriver{Name: "qemu", Type: "raw"},
		Source:   &libvirtxml.DomainDiskSource{File: &libvirtxml.DomainDiskSourceFile{File: seed}},
		Target:   &libvirtxml.DomainDiskTarget{Dev: nextTarget(x, "sd"), Bus: "sata"},
		ReadOnly: &libvirtxml.DomainDiskReadOnly{},
	})
}

// overlayVolume creates a qcow2 volume next to base whose unwritten blocks read from base.
func overlayVolume(l *libvirt.Libvirt, base, name string) (libvirt.StorageVol, string, error) {
	srcVol, err := l.StorageVolLookupByPath(base)
	if err != nil {
		return libvirt.StorageVol{}, "", refusedf("%s is not in a storage pool, so VirtoMir cannot link to it. Add its directory as a pool first.", base)
	}
	pool, err := l.StoragePoolLookupByName(srcVol.Pool)
	if err != nil {
		return libvirt.StorageVol{}, "", classify(err)
	}
	if _, err := l.StorageVolLookupByName(pool, name); err == nil {
		return libvirt.StorageVol{}, "", refusedf("Pool %s already has a volume named %s.", srcVol.Pool, name)
	}
	info, err := volumeInfo(l, srcVol)
	if err != nil {
		return libvirt.StorageVol{}, "", classify(err)
	}
	doc, err := overlayXML(name, base, info.Format, info.CapacityBytes)
	if err != nil {
		return libvirt.StorageVol{}, "", err
	}
	vol, err := l.StorageVolCreateXML(pool, doc, 0)
	if err != nil {
		return libvirt.StorageVol{}, "", classify(err)
	}
	p, err := l.StorageVolGetPath(vol)
	if err != nil {
		_ = l.StorageVolDelete(vol, libvirt.StorageVolDeleteNormal)
		return libvirt.StorageVol{}, "", classify(err)
	}
	return vol, p, nil
}

func overlayXML(name, base, baseFormat string, capacity uint64) (string, error) {
	if baseFormat == "" {
		baseFormat = "raw"
	}
	v := libvirtxml.StorageVolume{
		Name:     name,
		Capacity: &libvirtxml.StorageVolumeSize{Unit: "bytes", Value: capacity},
		Target:   &libvirtxml.StorageVolumeTarget{Format: &libvirtxml.StorageVolumeTargetFormat{Type: "qcow2"}},
		BackingStore: &libvirtxml.StorageVolumeBackingStore{
			Path:   base,
			Format: &libvirtxml.StorageVolumeTargetFormat{Type: baseFormat},
		},
	}
	return v.Marshal()
}
