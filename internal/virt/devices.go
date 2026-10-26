package virt

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"
)

// deviceFlags applies a change to the running machine and to its saved definition, so
// hot-plugged devices survive a reboot and changes to a stopped machine just work.
func (c *Client) deviceFlags(l *libvirt.Libvirt, d libvirt.Domain) (uint32, bool, error) {
	sum, err := summary(l, d)
	if err != nil {
		return 0, false, classify(err)
	}
	var f uint32
	on := isOn(sum.State) || sum.State == StatePaused
	if on {
		f |= uint32(libvirt.DomainAffectLive)
	}
	if sum.Persistent {
		f |= uint32(libvirt.DomainAffectConfig)
	}
	return f, on, nil
}

// ChangeMedia inserts an image into (or with source "" ejects it from) a CD/DVD or floppy drive.
func (c *Client) ChangeMedia(uuid, target, source string) error {
	l, d, err := c.lookup(uuid)
	if err != nil {
		return err
	}
	flags, on, err := c.deviceFlags(l, d)
	if err != nil {
		return err
	}
	x, err := c.parseDomain(l, d, !on)
	if err != nil {
		return err
	}
	disk := findDisk(x, target)
	if disk == nil {
		return fmt.Errorf("%w: no drive %s", ErrNotFound, target)
	}
	if disk.Device != "cdrom" && disk.Device != "floppy" {
		return invalidf("%s is a %s, not a removable drive.", target, orDefault(disk.Device, "disk"))
	}
	if source == "" {
		disk.Source = nil
	} else {
		disk.Source = &libvirtxml.DomainDiskSource{File: &libvirtxml.DomainDiskSourceFile{File: source}}
	}
	disk.Alias, disk.Address = nil, nil
	doc, err := disk.Marshal()
	if err != nil {
		return err
	}
	if err := l.DomainUpdateDeviceFlags(d, doc, libvirt.DomainDeviceModifyFlags(flags)); err != nil {
		return classify(err)
	}
	return nil
}

// DiskSpec describes a disk to add. Either Path (an existing image, e.g. an ISO for a CD-ROM),
// Pool+Volume (an existing volume) or Pool+SizeGiB (a new volume) is set.
type DiskSpec struct {
	Device  string      `json:"device"` // disk (default) or cdrom
	Bus     string      `json:"bus"`    // virtio (default for disks), sata, scsi, usb
	Path    string      `json:"path,omitempty"`
	Pool    string      `json:"pool,omitempty"`
	Volume  string      `json:"volume,omitempty"`
	SizeGiB uint64      `json:"sizeGiB,omitempty"`
	Format  string      `json:"format,omitempty"` // qcow2 (default) or raw, for new volumes
	IoTune  *DiskIoTune `json:"iotune,omitempty"`
	// Serial is what the guest sees as the disk's serial number: Linux names it
	// /dev/disk/by-id/virtio-<serial> (the Kubernetes CSI driver finds its volumes so).
	Serial string `json:"serial,omitempty"`
}

// serialRE is what QEMU passes to a virtio disk unchanged (20 characters at most).
var serialRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,20}$`)

// AttachDisk adds a disk or CD/DVD drive, creating a new volume if requested.
// It returns the device name in the guest (e.g. "vdb").
func (c *Client) AttachDisk(uuid string, spec DiskSpec) (string, error) {
	spec.Device = orDefault(spec.Device, "disk")
	if spec.Device != "disk" && spec.Device != "cdrom" {
		return "", invalidf("Unsupported device type %q.", spec.Device)
	}
	if spec.Bus == "" {
		spec.Bus = map[string]string{"disk": "virtio", "cdrom": "sata"}[spec.Device]
	}
	prefix, ok := busPrefix[spec.Bus]
	if !ok {
		return "", invalidf("Unsupported bus %q.", spec.Bus)
	}
	if spec.Serial != "" && !serialRE.MatchString(spec.Serial) {
		return "", invalidf("A disk serial is 1 to 20 letters, digits, '_' or '-'.")
	}
	l, d, err := c.lookup(uuid)
	if err != nil {
		return "", err
	}
	flags, on, err := c.deviceFlags(l, d)
	if err != nil {
		return "", err
	}
	x, err := c.parseDomain(l, d, !on)
	if err != nil {
		return "", err
	}
	target := nextTarget(x, prefix)

	disk := libvirtxml.DomainDisk{
		Device: spec.Device,
		Driver: &libvirtxml.DomainDiskDriver{Name: "qemu", Type: "raw"},
		Target: &libvirtxml.DomainDiskTarget{Dev: target, Bus: spec.Bus},
		Serial: spec.Serial,
	}
	if orDefault(spec.Device, "disk") == "disk" {
		disk.Driver.Discard = "unmap" // the guest's TRIM frees the space on the host (ADR-0074)
	}
	if t := spec.IoTune; t != nil {
		disk.IOTune = &libvirtxml.DomainDiskIOTune{
			TotalBytesSec:    t.TotalBytesSec,
			ReadBytesSec:     t.ReadBytesSec,
			WriteBytesSec:    t.WriteBytesSec,
			TotalIopsSec:     t.TotalIopsSec,
			ReadIopsSec:      t.ReadIopsSec,
			WriteIopsSec:     t.WriteIopsSec,
			TotalBytesSecMax: t.TotalBytesSecMax,
			TotalIopsSecMax:  t.TotalIopsSecMax,
		}
	}
	var created *libvirt.StorageVol
	switch {
	case spec.Path != "":
		disk.Source = &libvirtxml.DomainDiskSource{File: &libvirtxml.DomainDiskSourceFile{File: spec.Path}}
		if strings.HasSuffix(spec.Path, ".qcow2") {
			disk.Driver.Type = "qcow2"
		}
	case spec.Pool != "" && spec.Volume != "" && !poolKeepsFiles(l, spec.Pool):
		// iSCSI LUN, Ceph image, LVM volume: libvirt resolves pool/volume itself.
		if _, _, err := volumePath(l, spec.Pool, spec.Volume); err != nil {
			return "", err
		}
		disk.Source = &libvirtxml.DomainDiskSource{Volume: &libvirtxml.DomainDiskSourceVolume{Pool: spec.Pool, Volume: spec.Volume}}
	case spec.Pool != "" && spec.Volume != "":
		p, format, err := volumePath(l, spec.Pool, spec.Volume)
		if err != nil {
			return "", err
		}
		disk.Source = &libvirtxml.DomainDiskSource{File: &libvirtxml.DomainDiskSourceFile{File: p}}
		disk.Driver.Type = orDefault(format, "raw")
	case spec.Pool != "" && spec.SizeGiB > 0:
		if spec.Device != "disk" {
			return "", invalidf("A new empty volume only makes sense for a disk.")
		}
		files := poolKeepsFiles(l, spec.Pool)
		format := orDefault(spec.Format, "qcow2")
		if !files {
			format = "raw" // Ceph images and LVM volumes are raw block storage
		}
		name := newVolName(d.Name, target, format)
		vol, p, err := createVolume(l, spec.Pool, name, spec.SizeGiB<<30, format)
		if err != nil {
			return "", err
		}
		created = &vol
		disk.Source = &libvirtxml.DomainDiskSource{File: &libvirtxml.DomainDiskSourceFile{File: p}}
		if !files {
			disk.Source = &libvirtxml.DomainDiskSource{Volume: &libvirtxml.DomainDiskSourceVolume{Pool: spec.Pool, Volume: name}}
		}
		disk.Driver.Type = format
	case spec.Device == "cdrom":
		// An empty drive; media can be inserted later.
	default:
		return "", invalidf("Choose an existing image or volume, or a pool and a size for a new disk.")
	}
	if spec.Device == "cdrom" {
		disk.ReadOnly = &libvirtxml.DomainDiskReadOnly{}
	}
	doc, err := disk.Marshal()
	if err != nil {
		return "", err
	}
	if err := l.DomainAttachDeviceFlags(d, doc, flags); err != nil {
		if created != nil {
			dropVolume(c.log, l, *created)
		}
		return "", classify(err)
	}
	c.log.Info("disk attached", "domain", d.Name, "target", target)
	return target, nil
}

// NICSpec describes a network adapter to add: connected to a libvirt network or a host bridge.
type NICSpec struct {
	Network string `json:"network,omitempty"`
	Bridge  string `json:"bridge,omitempty"`
	Model   string `json:"model,omitempty"` // virtio (default), e1000e, rtl8139
	MAC     string `json:"mac,omitempty"`   // generated when empty
	// VLAN tags this adapter's traffic (Open vSwitch networks, macvtap passthrough, SR-IOV);
	// 0 = as the network says.
	VLAN uint `json:"vlan,omitempty"`

	// OVN (SDN, ADR-0072): the adapter plugs into the integration bridge (Bridge, default
	// br-int) as the logical switch port IfaceID of subnet OVNSubnet. The API allocates the
	// port; the MAC must be the port's.
	OVNSubnet      string   `json:"ovnSubnet,omitempty"`
	IfaceID        string   `json:"ifaceId,omitempty"`
	SecurityGroups []string `json:"securityGroups,omitempty"`
	IPAddress      string   `json:"ipAddress,omitempty"`
}

func (c *Client) AttachInterface(uuid string, spec NICSpec) error {
	nic, err := buildNIC(spec)
	if err != nil {
		return err
	}
	l, d, err := c.lookup(uuid)
	if err != nil {
		return err
	}
	flags, _, err := c.deviceFlags(l, d)
	if err != nil {
		return err
	}
	doc, err := nic.Marshal()
	if err != nil {
		return err
	}
	if err := l.DomainAttachDeviceFlags(d, doc, flags); err != nil {
		return classify(err)
	}
	c.log.Info("interface attached", "domain", d.Name)
	return nil
}

func buildNIC(spec NICSpec) (libvirtxml.DomainInterface, error) {
	nic := libvirtxml.DomainInterface{Model: &libvirtxml.DomainInterfaceModel{Type: orDefault(spec.Model, "virtio")}}
	if spec.IfaceID != "" {
		if spec.MAC == "" {
			return nic, invalidf("An OVN port has a fixed MAC address; give it.")
		}
		nic.Source = &libvirtxml.DomainInterfaceSource{Bridge: &libvirtxml.DomainInterfaceSourceBridge{Bridge: orDefault(spec.Bridge, "br-int")}}
		nic.VirtualPort = &libvirtxml.DomainInterfaceVirtualPort{Params: &libvirtxml.DomainInterfaceVirtualPortParams{
			OpenVSwitch: &libvirtxml.DomainInterfaceVirtualPortParamsOpenVSwitch{InterfaceID: spec.IfaceID}}}
		nic.MAC = &libvirtxml.DomainInterfaceMAC{Address: spec.MAC}
		return nic, nil
	}
	switch {
	case spec.Network != "" && spec.Bridge == "":
		nic.Source = &libvirtxml.DomainInterfaceSource{Network: &libvirtxml.DomainInterfaceSourceNetwork{Network: spec.Network}}
	case spec.Bridge != "" && spec.Network == "":
		nic.Source = &libvirtxml.DomainInterfaceSource{Bridge: &libvirtxml.DomainInterfaceSourceBridge{Bridge: spec.Bridge}}
	default:
		return nic, invalidf("Connect the adapter to either a virtual network or a host bridge.")
	}
	if spec.MAC != "" {
		nic.MAC = &libvirtxml.DomainInterfaceMAC{Address: spec.MAC}
	}
	if spec.VLAN > 4094 {
		return nic, invalidf("A VLAN ID is between 1 and 4094.")
	}
	if spec.VLAN != 0 {
		nic.VLan = &libvirtxml.DomainInterfaceVLan{Tags: []libvirtxml.DomainInterfaceVLanTag{{ID: spec.VLAN}}}
	}
	return nic, nil
}

// DeviceRef identifies a device to remove: a disk by its guest device name, an adapter by
// MAC, a host device by address (PCI), vendor:product (USB) or UUID (mdev), a shared folder
// by its mount tag. watchdog, tpm, rng and clipboard need no target (a machine has one).
type DeviceRef struct {
	Kind   string `json:"kind"`
	Target string `json:"target,omitempty"`
	MAC    string `json:"mac,omitempty"`
}

// DetachDevice removes a device. A disk image itself is kept.
func (c *Client) DetachDevice(uuid string, ref DeviceRef) (DeviceResult, error) {
	if slices.Contains(extraDeviceKinds, ref.Kind) {
		return c.detachExtraDevice(uuid, ref)
	}
	l, d, err := c.lookup(uuid)
	if err != nil {
		return DeviceResult{}, err
	}
	flags, on, err := c.deviceFlags(l, d)
	if err != nil {
		return DeviceResult{}, err
	}
	x, err := c.parseDomain(l, d, !on)
	if err != nil {
		return DeviceResult{}, err
	}
	var doc string
	switch ref.Kind {
	case "disk":
		disk := findDisk(x, ref.Target)
		if disk == nil {
			return DeviceResult{}, fmt.Errorf("%w: no disk %s", ErrNotFound, ref.Target)
		}
		doc, err = disk.Marshal()
	case "interface":
		nic := findNIC(x, ref.MAC)
		if nic == nil {
			return DeviceResult{}, fmt.Errorf("%w: no adapter %s", ErrNotFound, ref.MAC)
		}
		doc, err = nic.Marshal()
	case "hostdev-pci", "hostdev-usb":
		hd := findHostdev(x, ref.Kind, ref.Target)
		if hd == nil {
			return DeviceResult{}, fmt.Errorf("%w: no host device %s", ErrNotFound, ref.Target)
		}
		doc, err = hd.Marshal()
	default:
		return DeviceResult{}, invalidf("This kind of device cannot be removed here; use the XML editor.")
	}
	if err != nil {
		return DeviceResult{}, err
	}
	if err := l.DomainDetachDeviceFlags(d, doc, flags); err != nil {
		return DeviceResult{}, classify(err)
	}
	c.log.Info("device detached", "domain", d.Name, "kind", ref.Kind, "target", ref.Target, "mac", ref.MAC)
	return DeviceResult{}, nil
}

// detachExtraDevice removes one of the extra devices from the definition and, where QEMU
// can unplug it, from the running machine.
func (c *Client) detachExtraDevice(uuid string, ref DeviceRef) (DeviceResult, error) {
	var res DeviceResult
	l, d, err := c.lookup(uuid)
	if err != nil {
		return res, err
	}
	sum, err := summary(l, d)
	if err != nil {
		return res, classify(err)
	}
	x, err := c.parseDomain(l, d, true)
	if err != nil {
		return res, err
	}
	if _, err := removeExtraDevice(x, ref); err != nil {
		return res, err
	}
	doc, err := x.Marshal()
	if err != nil {
		return res, err
	}
	if _, err := l.DomainDefineXMLFlags(doc, libvirt.DomainDefineValidate); err != nil {
		return res, classify(err)
	}
	c.log.Info("device detached", "domain", d.Name, "kind", ref.Kind, "target", ref.Target)
	if !isOn(sum.State) && sum.State != StatePaused {
		return res, nil
	}
	// Unplug what the running machine has, as it has it: libvirt matches the live device
	// (virtiofs gains a binary path and an alias there, for one).
	lx, err := c.parseDomain(l, d, false)
	if err != nil {
		return DeviceResult{AppliesOnNextBoot: true, LiveError: err.Error()}, nil
	}
	live, err := removeExtraDevice(lx, ref)
	switch {
	case errors.Is(err, ErrNotFound): // it never made it into the running machine
		return res, nil
	case err != nil:
		return DeviceResult{AppliesOnNextBoot: true, LiveError: err.Error()}, nil
	}
	return applyLive(live, func(doc string) error {
		return l.DomainDetachDeviceFlags(d, doc, uint32(libvirt.DomainAffectLive))
	}), nil
}

// findHostdev finds a passed-through device by host PCI address or USB vendor:product.
func findHostdev(x *libvirtxml.Domain, kind, target string) *libvirtxml.DomainHostdev {
	if x.Devices == nil {
		return nil
	}
	for i, h := range x.Devices.Hostdevs {
		switch {
		case kind == "hostdev-pci" && h.SubsysPCI != nil && h.SubsysPCI.Source != nil && h.SubsysPCI.Source.Address != nil:
			if pciAddr(h.SubsysPCI.Source.Address) == strings.ToLower(target) {
				return &x.Devices.Hostdevs[i]
			}
		case kind == "hostdev-usb" && h.SubsysUSB != nil && h.SubsysUSB.Source != nil && h.SubsysUSB.Source.Vendor != nil && h.SubsysUSB.Source.Product != nil:
			if usbKey(h.SubsysUSB.Source.Vendor.ID, h.SubsysUSB.Source.Product.ID) == strings.ToLower(target) {
				return &x.Devices.Hostdevs[i]
			}
		}
	}
	return nil
}

var busPrefix = map[string]string{"virtio": "vd", "sata": "sd", "scsi": "sd", "usb": "sd", "ide": "hd"}

func findDisk(x *libvirtxml.Domain, target string) *libvirtxml.DomainDisk {
	if x.Devices == nil {
		return nil
	}
	for i := range x.Devices.Disks {
		if t := x.Devices.Disks[i].Target; t != nil && t.Dev == target {
			return &x.Devices.Disks[i]
		}
	}
	return nil
}

func findNIC(x *libvirtxml.Domain, mac string) *libvirtxml.DomainInterface {
	if x.Devices == nil {
		return nil
	}
	for i := range x.Devices.Interfaces {
		if m := x.Devices.Interfaces[i].MAC; m != nil && strings.EqualFold(m.Address, mac) {
			return &x.Devices.Interfaces[i]
		}
	}
	return nil
}

// nextTarget returns the first free device name with the prefix: vda, vdb, …, vdz, vdaa, …
func nextTarget(x *libvirtxml.Domain, prefix string) string {
	used := map[string]bool{}
	if x.Devices != nil {
		for _, d := range x.Devices.Disks {
			if d.Target != nil {
				used[d.Target.Dev] = true
			}
		}
	}
	for i := 0; ; i++ {
		name := prefix + diskLetters(i)
		if !used[name] {
			return name
		}
	}
}

// diskLetters is the Linux disk suffix for index i: 0→a, 25→z, 26→aa.
func diskLetters(i int) string {
	s := ""
	for i++; i > 0; i = (i - 1) / 26 {
		s = string(rune('a'+(i-1)%26)) + s
	}
	return s
}

func newVolName(machine, target, format string) string {
	ext := "img"
	if format == "qcow2" {
		ext = "qcow2"
	}
	return fmt.Sprintf("%s-%s.%s", machine, target, ext)
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// maxMbps keeps a typo from becoming a meaningless limit (400 Gbit/s).
const maxMbps = 400_000

// SetInterfaceBandwidth limits an adapter's average traffic, live and in the definition;
// 0 removes a limit.
func (c *Client) SetInterfaceBandwidth(uuid, mac string, bw NICBandwidth) error {
	if bw.InMbps > maxMbps || bw.OutMbps > maxMbps {
		return invalidf("A limit is at most %d Mbit/s.", maxMbps)
	}
	l, d, err := c.lookup(uuid)
	if err != nil {
		return err
	}
	flags, on, err := c.deviceFlags(l, d)
	if err != nil {
		return err
	}
	params := []libvirt.TypedParam{
		{Field: "inbound.average", Value: *libvirt.NewTypedParamValueUint(uint32(mbpsToKiBps(bw.InMbps)))},
		{Field: "outbound.average", Value: *libvirt.NewTypedParamValueUint(uint32(mbpsToKiBps(bw.OutMbps)))},
	}
	if err := l.DomainSetInterfaceParameters(d, strings.ToLower(mac), params, flags); err != nil {
		return classify(err)
	}
	c.log.Info("adapter bandwidth updated", "domain", d.Name, "mac", mac, "inMbps", bw.InMbps, "outMbps", bw.OutMbps, "live", on)
	return nil
}

// SetDiskQoS updates the I/O limits of a VM disk (QEMU iotune) both live and in the configuration.
func (c *Client) SetDiskQoS(uuid, target string, qos DiskIoTune) error {
	l, d, err := c.lookup(uuid)
	if err != nil {
		return err
	}
	flags, on, err := c.deviceFlags(l, d)
	if err != nil {
		return err
	}

	qosParam := func(field string, val uint64) libvirt.TypedParam {
		return libvirt.TypedParam{
			Field: field,
			Value: *libvirt.NewTypedParamValueUllong(val),
		}
	}

	params := []libvirt.TypedParam{
		qosParam("total_bytes_sec", qos.TotalBytesSec),
		qosParam("read_bytes_sec", qos.ReadBytesSec),
		qosParam("write_bytes_sec", qos.WriteBytesSec),
		qosParam("total_iops_sec", qos.TotalIopsSec),
		qosParam("read_iops_sec", qos.ReadIopsSec),
		qosParam("write_iops_sec", qos.WriteIopsSec),
		qosParam("total_bytes_sec_max", qos.TotalBytesSecMax),
		qosParam("total_iops_sec_max", qos.TotalIopsSecMax),
	}

	if err := l.DomainSetBlockIOTune(d, target, params, flags); err != nil {
		return classify(err)
	}
	c.log.Info("disk qos updated", "domain", d.Name, "target", target, "totalIops", qos.TotalIopsSec, "totalBytes", qos.TotalBytesSec, "live", on)
	return nil
}
