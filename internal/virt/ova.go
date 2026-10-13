package virt

import (
	"archive/tar"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"libvirt.org/go/libvirtxml"
)

// OVF is the part of an OVF descriptor VirtoMir uses to rebuild a machine: its name,
// processors, memory, firmware, guest OS and disks (in order).
type OVF struct {
	Name      string
	VCPUs     int
	MemoryMiB uint64
	UEFI      bool
	Windows   bool
	Disks     []string // file names inside the OVA, boot disk first
}

// ovfEnvelope maps just enough of DMTF OVF 1.x/2.x (namespaces ignored).
type ovfEnvelope struct {
	References struct {
		Files []struct {
			ID   string `xml:"id,attr"`
			Href string `xml:"href,attr"`
		} `xml:"File"`
	} `xml:"References"`
	DiskSection struct {
		Disks []struct {
			DiskID  string `xml:"diskId,attr"`
			FileRef string `xml:"fileRef,attr"`
		} `xml:"Disk"`
	} `xml:"DiskSection"`
	VirtualSystem struct {
		ID       string `xml:"id,attr"`
		Name     string `xml:"Name"`
		OSection struct {
			Description string `xml:"Description"`
			OSType      string `xml:"osType,attr"`
		} `xml:"OperatingSystemSection"`
		Hardware struct {
			Items []struct {
				ResourceType    int    `xml:"ResourceType"`
				VirtualQuantity uint64 `xml:"VirtualQuantity"`
				AllocationUnits string `xml:"AllocationUnits"`
				HostResource    string `xml:"HostResource"`
			} `xml:"Item"`
			Configs []struct {
				Key   string `xml:"key,attr"`
				Value string `xml:"value,attr"`
			} `xml:"Config"`
		} `xml:"VirtualHardwareSection"`
	} `xml:"VirtualSystem"`
}

// ParseOVF reads an OVF descriptor.
func ParseOVF(data []byte) (OVF, error) {
	var env ovfEnvelope
	if err := xml.Unmarshal(data, &env); err != nil {
		return OVF{}, invalidf("The OVF descriptor cannot be read: %v", err)
	}
	vs := env.VirtualSystem
	o := OVF{Name: strings.TrimSpace(vs.Name), VCPUs: 1, MemoryMiB: 1024}
	if o.Name == "" {
		o.Name = vs.ID
	}
	osText := strings.ToLower(vs.OSection.OSType + " " + vs.OSection.Description)
	o.Windows = strings.Contains(osText, "windows")
	files := map[string]string{}
	for _, f := range env.References.Files {
		files[f.ID] = f.Href
	}
	disks := map[string]string{} // ovf:/disk/<id> → file
	for _, d := range env.DiskSection.Disks {
		disks["ovf:/disk/"+d.DiskID] = files[d.FileRef]
	}
	for _, it := range vs.Hardware.Items {
		switch it.ResourceType {
		case 3: // processors
			if it.VirtualQuantity > 0 {
				o.VCPUs = int(it.VirtualQuantity)
			}
		case 4: // memory
			if it.VirtualQuantity > 0 {
				o.MemoryMiB = toMiB(it.VirtualQuantity, it.AllocationUnits)
			}
		case 17: // disk drive
			href := strings.TrimPrefix(strings.TrimPrefix(it.HostResource, "/disk/"), "ovf:")
			if f, ok := disks[it.HostResource]; ok {
				href = f
			} else if f, ok := disks["ovf:/disk/"+href]; ok {
				href = f
			}
			if href != "" {
				o.Disks = append(o.Disks, href)
			}
		}
	}
	for _, c := range vs.Hardware.Configs {
		if c.Key == "firmware" && strings.EqualFold(c.Value, "efi") {
			o.UEFI = true
		}
	}
	if len(o.Disks) == 0 { // no hardware items: take the referenced disk files in order
		for _, d := range env.DiskSection.Disks {
			if f := files[d.FileRef]; f != "" {
				o.Disks = append(o.Disks, f)
			}
		}
	}
	return o, nil
}

// toMiB converts an OVF quantity with units like "byte * 2^20" or "MegaBytes".
func toMiB(q uint64, units string) uint64 {
	u := strings.ToLower(strings.ReplaceAll(units, " ", ""))
	switch {
	case u == "" || strings.Contains(u, "2^20") || strings.HasPrefix(u, "mega") || u == "mb":
		return q
	case strings.Contains(u, "2^30") || strings.HasPrefix(u, "giga") || u == "gb":
		return q * 1024
	case strings.Contains(u, "2^10") || strings.HasPrefix(u, "kilo") || u == "kb":
		return q / 1024
	case u == "byte" || u == "bytes":
		return q >> 20
	}
	if i := strings.Index(u, "2^"); i >= 0 {
		if exp, err := strconv.Atoi(u[i+2:]); err == nil && exp >= 20 {
			return q << (exp - 20)
		}
	}
	return q
}

// ImportOVA reads an OVA (a tar of an OVF descriptor and disk images) from r, converts its
// disks to qcow2 in a directory pool and creates the machine (not started). name overrides
// the name from the OVF.
func (c *Client) ImportOVA(ctx context.Context, pool, name string, r io.Reader, report func(string)) (CreateResult, error) {
	l, err := c.conn()
	if err != nil {
		return CreateResult{}, err
	}
	sp, err := l.StoragePoolLookupByName(pool)
	if err != nil {
		return CreateResult{}, classify(err)
	}
	praw, err := l.StoragePoolGetXMLDesc(sp, 0)
	if err != nil {
		return CreateResult{}, classify(err)
	}
	var px libvirtxml.StoragePool
	if err := px.Unmarshal(praw); err != nil || px.Target == nil || px.Target.Path == "" || (px.Type != "dir" && px.Type != "netfs") {
		return CreateResult{}, invalidf("Import into a directory or NFS pool (pool %s is not one).", pool)
	}
	tmp, err := os.MkdirTemp(px.Target.Path, ".virtomir-import-")
	if err != nil {
		return CreateResult{}, err
	}
	defer os.RemoveAll(tmp)

	// Unpack: the descriptor and every file (disks are large, so straight to disk).
	report("Unpacking…")
	var ovfData []byte
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return CreateResult{}, invalidf("This is not a valid OVA file (a tar archive): %v", err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		base := path.Base(h.Name)
		if base == "." || base == "/" || strings.HasPrefix(base, ".") {
			continue
		}
		if strings.HasSuffix(strings.ToLower(base), ".ovf") {
			if ovfData, err = io.ReadAll(io.LimitReader(tr, 4<<20)); err != nil {
				return CreateResult{}, err
			}
			continue
		}
		f, err := os.OpenFile(filepath.Join(tmp, base), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return CreateResult{}, err
		}
		_, err = io.Copy(f, &ctxReader{ctx: ctx, r: tr})
		f.Close()
		if err != nil {
			return CreateResult{}, err
		}
	}
	if ovfData == nil {
		return CreateResult{}, invalidf("The OVA has no .ovf descriptor.")
	}
	ovf, err := ParseOVF(ovfData)
	if err != nil {
		return CreateResult{}, err
	}
	if name == "" {
		name = ovf.Name
	}
	if err := checkName("Machine", name); err != nil {
		return CreateResult{}, err
	}
	if len(ovf.Disks) == 0 {
		return CreateResult{}, invalidf("The OVF lists no disks.")
	}

	// Convert every disk to qcow2 next to the pool's other images.
	var paths []string
	cleanup := func() {
		for _, p := range paths {
			_ = os.Remove(p)
		}
		_ = l.StoragePoolRefresh(sp, 0)
	}
	for i, d := range ovf.Disks {
		src := filepath.Join(tmp, path.Base(d))
		if _, err := os.Stat(src); err != nil {
			cleanup()
			return CreateResult{}, invalidf("The OVF names disk %s, but the OVA does not contain it.", d)
		}
		dst := filepath.Join(px.Target.Path, fmt.Sprintf("%s-disk%d.qcow2", name, i))
		if _, err := os.Stat(dst); err == nil {
			cleanup()
			return CreateResult{}, refusedf("%s already exists in pool %s.", filepath.Base(dst), pool)
		}
		report(fmt.Sprintf("Converting disk %d of %d…", i+1, len(ovf.Disks)))
		out, err := exec.CommandContext(ctx, "qemu-img", "convert", "-O", "qcow2", src, dst).CombinedOutput()
		if err != nil {
			cleanup()
			return CreateResult{}, fmt.Errorf("convert %s: %v: %s", d, err, strings.TrimSpace(string(out)))
		}
		paths = append(paths, dst)
	}
	_ = l.StoragePoolRefresh(sp, 0)

	report("Creating " + name + "…")
	osName, firmware := "linux", "bios"
	if ovf.Windows {
		osName = "windows"
	}
	if ovf.UEFI {
		firmware = "uefi"
	}
	res, err := c.CreateDomain(CreateSpec{
		Name: name, OS: osName, VCPUs: ovf.VCPUs, MemoryMiB: ovf.MemoryMiB, Firmware: firmware,
		DiskPath: paths[0], Network: &NICSpec{Network: "default"},
	})
	if err != nil {
		cleanup()
		return CreateResult{}, err
	}
	for _, p := range paths[1:] {
		if _, err := c.AttachDisk(res.UUID, DiskSpec{Path: p, Bus: map[bool]string{true: "sata", false: "virtio"}[ovf.Windows]}); err != nil {
			c.log.Warn("attach imported disk", "disk", p, "err", err)
		}
	}
	c.log.Info("OVA imported", "domain", name, "disks", len(paths))
	return res, nil
}
