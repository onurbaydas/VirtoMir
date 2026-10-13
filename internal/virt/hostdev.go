package virt

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"
)

// HostHardware is what the host could pass through to machines.
type HostHardware struct {
	// IOMMU: the kernel isolates devices (needed for PCI passthrough).
	IOMMU bool      `json:"iommu"`
	PCI   []HostPCI `json:"pci"`
	USB   []HostUSB `json:"usb"`
}

type HostPCI struct {
	Name       string   `json:"name"`    // libvirt node device name
	Address    string   `json:"address"` // 0000:00:1f.2
	Class      string   `json:"class"`   // 0x010601
	ClassName  string   `json:"className"`
	Vendor     string   `json:"vendor"`
	Product    string   `json:"product"`
	VendorID   string   `json:"vendorId"`
	ProductID  string   `json:"productId"`
	Driver     string   `json:"driver,omitempty"`
	IOMMUGroup *int     `json:"iommuGroup,omitempty"`
	Group      []string `json:"group,omitempty"` // addresses in the same IOMMU group
	UsedBy     []string `json:"usedBy"`          // machines that have it
	// HostUse says why the host needs it (mounted disks, an interface with an address).
	HostUse string `json:"hostUse,omitempty"`
	// Blocked explains why it cannot be passed through ("" = it can).
	Blocked string `json:"blocked,omitempty"`
}

type HostUSB struct {
	Name      string   `json:"name"`
	Bus       int      `json:"bus"`
	Device    int      `json:"device"`
	VendorID  string   `json:"vendorId"`
	ProductID string   `json:"productId"`
	Vendor    string   `json:"vendor"`
	Product   string   `json:"product"`
	UsedBy    []string `json:"usedBy"`
	Blocked   string   `json:"blocked,omitempty"`
}

// pciClassNames are the PCI base classes people recognize.
var pciClassNames = map[string]string{
	"01": "Storage controller", "02": "Network controller", "03": "Display controller",
	"04": "Multimedia controller", "05": "Memory controller", "06": "Bridge",
	"07": "Communication controller", "08": "System peripheral", "09": "Input device",
	"0c": "Serial bus controller (USB, …)", "0d": "Wireless controller", "12": "Processing accelerator",
}

func pciAddress(c *libvirtxml.NodeDevicePCICapability) string {
	u := func(p *uint) uint {
		if p == nil {
			return 0
		}
		return *p
	}
	return fmt.Sprintf("%04x:%02x:%02x.%x", u(c.Domain), u(c.Bus), u(c.Slot), u(c.Function))
}

func addrString(a libvirtxml.NodeDevicePCIAddress) string {
	u := func(p *uint) uint {
		if p == nil {
			return 0
		}
		return *p
	}
	return fmt.Sprintf("%04x:%02x:%02x.%x", u(a.Domain), u(a.Bus), u(a.Slot), u(a.Function))
}

// HostHardware lists PCI and USB devices with what uses them and whether they can be
// passed through, and why not.
func (c *Client) HostHardware() (HostHardware, error) {
	l, err := c.conn()
	if err != nil {
		return HostHardware{}, err
	}
	devs, _, err := l.ConnectListAllNodeDevices(1, uint32(libvirt.ConnectListNodeDevicesCapPciDev|libvirt.ConnectListNodeDevicesCapUsbDev))
	if err != nil {
		return HostHardware{}, classify(err)
	}
	usedPCI, usedUSB := c.hostdevUsers(l)
	hw := HostHardware{PCI: []HostPCI{}, USB: []HostUSB{}}
	drivers := map[string]string{} // address → driver
	for _, d := range devs {
		raw, err := l.NodeDeviceGetXMLDesc(d.Name, 0)
		if err != nil {
			continue
		}
		var nd libvirtxml.NodeDevice
		if nd.Unmarshal(raw) != nil {
			continue
		}
		driver := ""
		if nd.Driver != nil {
			driver = nd.Driver.Name
		}
		switch cp := nd.Capability; {
		case cp.PCI != nil:
			p := HostPCI{Name: nd.Name, Address: pciAddress(cp.PCI), Class: cp.PCI.Class, Driver: driver,
				Vendor: cp.PCI.Vendor.Name, Product: cp.PCI.Product.Name, VendorID: cp.PCI.Vendor.ID, ProductID: cp.PCI.Product.ID, UsedBy: []string{}}
			if len(p.Class) >= 4 {
				p.ClassName = pciClassNames[strings.ToLower(p.Class[2:4])]
			}
			if g := cp.PCI.IOMMUGroup; g != nil {
				n := g.Number
				p.IOMMUGroup = &n
				hw.IOMMU = true
				for _, a := range g.Address {
					if s := addrString(a); s != p.Address {
						p.Group = append(p.Group, s)
					}
				}
			}
			p.UsedBy = append(p.UsedBy, usedPCI[p.Address]...)
			p.HostUse = pciHostUse(p.Address)
			drivers[p.Address] = driver
			hw.PCI = append(hw.PCI, p)
		case cp.USBDevice != nil:
			u := HostUSB{Name: nd.Name, Bus: cp.USBDevice.Bus, Device: cp.USBDevice.Device,
				VendorID: cp.USBDevice.Vendor.ID, ProductID: cp.USBDevice.Product.ID,
				Vendor: cp.USBDevice.Vendor.Name, Product: cp.USBDevice.Product.Name, UsedBy: []string{}}
			u.UsedBy = append(u.UsedBy, usedUSB[usbKey(u.VendorID, u.ProductID)]...)
			if u.VendorID == "0x1d6b" { // Linux Foundation: the host's own root hubs
				u.Blocked = "This is a USB root hub of the host."
			}
			hw.USB = append(hw.USB, u)
		}
	}
	isBridge := func(addr string) bool {
		for _, p := range hw.PCI {
			if p.Address == addr {
				return strings.HasPrefix(strings.ToLower(p.Class), "0x06")
			}
		}
		return false
	}
	for i := range hw.PCI {
		hw.PCI[i].Blocked = pciBlocked(hw.PCI[i], hw.IOMMU, drivers, isBridge)
	}
	slices.SortFunc(hw.PCI, func(a, b HostPCI) int { return strings.Compare(a.Address, b.Address) })
	return hw, nil
}

// pciBlocked decides whether p can be passed through: the IOMMU must be on, it must not be
// a bridge or something the host needs, and every other device in its IOMMU group must be
// free too (libvirt hands the whole group to the machine).
func pciBlocked(p HostPCI, iommu bool, drivers map[string]string, isBridge func(string) bool) string {
	switch {
	case !iommu:
		return "The IOMMU is off on this host. Turn on VT-d/AMD-Vi in the firmware and add intel_iommu=on (or amd_iommu=on) to the kernel command line."
	case p.IOMMUGroup == nil:
		return "It is not in an IOMMU group, so it cannot be isolated."
	case strings.HasPrefix(strings.ToLower(p.Class), "0x06"):
		return "It is a bridge; pass through the devices behind it instead."
	case p.HostUse != "":
		return "The host needs it: " + p.HostUse + "."
	}
	var busy []string
	for _, g := range p.Group {
		if isBridge(g) {
			continue
		}
		if d := drivers[g]; d != "" && d != "vfio-pci" {
			busy = append(busy, fmt.Sprintf("%s (%s)", g, d))
		}
	}
	if len(busy) > 0 {
		return "Its IOMMU group also holds " + strings.Join(busy, ", ") + ", which the host still uses; the whole group goes to the machine."
	}
	return ""
}

func usbKey(vendor, product string) string {
	return strings.ToLower(strings.TrimPrefix(vendor, "0x") + ":" + strings.TrimPrefix(product, "0x"))
}

// hostdevUsers maps PCI addresses and USB vendor:product to the machines that have them.
func (c *Client) hostdevUsers(l *libvirt.Libvirt) (map[string][]string, map[string][]string) {
	pci, usb := map[string][]string{}, map[string][]string{}
	doms, _, err := l.ConnectListAllDomains(1, libvirt.ConnectListDomainsActive|libvirt.ConnectListDomainsInactive)
	if err != nil {
		return pci, usb
	}
	for _, d := range doms {
		x, err := c.parseDomain(l, d, true)
		if err != nil || x.Devices == nil {
			continue
		}
		for _, h := range x.Devices.Hostdevs {
			switch {
			case h.SubsysPCI != nil && h.SubsysPCI.Source != nil && h.SubsysPCI.Source.Address != nil:
				a := pciAddr(h.SubsysPCI.Source.Address)
				pci[a] = appendUnique(pci[a], d.Name)
			case h.SubsysUSB != nil && h.SubsysUSB.Source != nil && h.SubsysUSB.Source.Vendor != nil && h.SubsysUSB.Source.Product != nil:
				k := usbKey(h.SubsysUSB.Source.Vendor.ID, h.SubsysUSB.Source.Product.ID)
				usb[k] = appendUnique(usb[k], d.Name)
			}
		}
	}
	return pci, usb
}

// HostdevSpec names a host device to give a machine.
type HostdevSpec struct {
	Type      string `json:"type"`                // pci or usb
	Address   string `json:"address,omitempty"`   // pci: 0000:01:00.0
	VendorID  string `json:"vendorId,omitempty"`  // usb
	ProductID string `json:"productId,omitempty"` // usb
}

func parsePCIAddress(s string) (*libvirtxml.DomainAddressPCI, error) {
	var dom, bus, slot, fn uint64
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == ':' || r == '.' })
	if len(parts) != 4 {
		return nil, invalidf("%q is not a PCI address like 0000:01:00.0.", s)
	}
	var err error
	for i, dst := range []*uint64{&dom, &bus, &slot, &fn} {
		if *dst, err = strconv.ParseUint(parts[i], 16, 32); err != nil {
			return nil, invalidf("%q is not a PCI address like 0000:01:00.0.", s)
		}
	}
	u := func(v uint64) *uint { x := uint(v); return &x }
	return &libvirtxml.DomainAddressPCI{Domain: u(dom), Bus: u(bus), Slot: u(slot), Function: u(fn)}, nil
}

// AttachHostdev passes a host PCI or USB device to a machine (in its definition; also live
// when it runs and the device allows hot-plug). The same safety checks as the hardware
// list apply.
func (c *Client) AttachHostdev(uuid string, spec HostdevSpec) (liveErr string, err error) {
	hw, err := c.HostHardware()
	if err != nil {
		return "", err
	}
	var hd libvirtxml.DomainHostdev
	switch spec.Type {
	case "pci":
		i := slices.IndexFunc(hw.PCI, func(p HostPCI) bool { return p.Address == strings.ToLower(spec.Address) })
		if i < 0 {
			return "", invalidf("There is no PCI device %s on this host.", spec.Address)
		}
		p := hw.PCI[i]
		if p.Blocked != "" {
			return "", refusedf("%s cannot be passed through. %s", p.Address, p.Blocked)
		}
		if len(p.UsedBy) > 0 {
			return "", refusedf("%s is already given to %s.", p.Address, strings.Join(p.UsedBy, ", "))
		}
		addr, err := parsePCIAddress(p.Address)
		if err != nil {
			return "", err
		}
		hd = libvirtxml.DomainHostdev{Managed: "yes", SubsysPCI: &libvirtxml.DomainHostdevSubsysPCI{
			Source: &libvirtxml.DomainHostdevSubsysPCISource{Address: addr}}}
	case "usb":
		key := usbKey(spec.VendorID, spec.ProductID)
		i := slices.IndexFunc(hw.USB, func(u HostUSB) bool { return usbKey(u.VendorID, u.ProductID) == key })
		if i < 0 {
			return "", invalidf("There is no USB device %s on this host.", key)
		}
		if b := hw.USB[i].Blocked; b != "" {
			return "", refusedf("%s", b)
		}
		if u := hw.USB[i].UsedBy; len(u) > 0 {
			return "", refusedf("This USB device is already given to %s.", strings.Join(u, ", "))
		}
		hd = libvirtxml.DomainHostdev{SubsysUSB: &libvirtxml.DomainHostdevSubsysUSB{Source: &libvirtxml.DomainHostdevSubsysUSBSource{
			Vendor:  &libvirtxml.DomainHostDevProductVendorID{ID: hw.USB[i].VendorID},
			Product: &libvirtxml.DomainHostDevProductVendorID{ID: hw.USB[i].ProductID},
		}}}
	default:
		return "", invalidf("Device type must be pci or usb.")
	}
	doc, err := hd.Marshal()
	if err != nil {
		return "", err
	}
	l, d, err := c.lookup(uuid)
	if err != nil {
		return "", err
	}
	if err := l.DomainAttachDeviceFlags(d, doc, uint32(libvirt.DomainAffectConfig)); err != nil {
		return "", classify(err)
	}
	if sum, err := summary(l, d); err == nil && (isOn(sum.State) || sum.State == StatePaused) {
		if err := l.DomainAttachDeviceFlags(d, doc, uint32(libvirt.DomainAffectLive)); err != nil {
			liveErr = classify(err).Error()
		}
	}
	c.log.Info("host device attached", "domain", d.Name, "type", spec.Type, "address", spec.Address, "usb", spec.VendorID+":"+spec.ProductID)
	return liveErr, nil
}
