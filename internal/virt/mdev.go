package virt

import (
	"crypto/rand"
	"fmt"
	"slices"
	"strings"

	"github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"
)

// Mediated devices split one physical card (NVIDIA vGPU, Intel GVT-g, …) into slices that
// machines get like their own card. libvirt keeps them as node devices: defined here with
// autostart, so a slice survives a host reboot.

// HostMDevType is one kind of slice a card offers.
type HostMDevType struct {
	ID        string `json:"id"`   // nvidia-63, i915-GVTg_V5_4, …
	Name      string `json:"name"` // GRID P4-2Q, …
	API       string `json:"api"`  // vfio-pci
	Available uint   `json:"available"`
}

// HostMDev is a slice that exists on the host.
type HostMDev struct {
	Name      string   `json:"name"` // libvirt node device name
	UUID      string   `json:"uuid"`
	Parent    string   `json:"parent"` // PCI address of the card
	Type      string   `json:"type"`
	TypeName  string   `json:"typeName,omitempty"`
	Active    bool     `json:"active"`
	Autostart bool     `json:"autostart"`
	UsedBy    []string `json:"usedBy"`
}

// MDevSpec asks for a new slice of the card at Parent (PCI address).
type MDevSpec struct {
	Parent string `json:"parent"`
	Type   string `json:"type"`
}

func mdevTypes(c *libvirtxml.NodeDevicePCICapability) []HostMDevType {
	var out []HostMDevType
	for _, sc := range c.Capabilities {
		if sc.MDevTypes == nil {
			continue
		}
		for _, t := range sc.MDevTypes.Types {
			out = append(out, HostMDevType{ID: t.ID, Name: t.Name, API: t.DeviceAPI, Available: t.AvailableInstances})
		}
	}
	return out
}

func mdevTypeName(pci []HostPCI, parent, typeID string) string {
	for _, p := range pci {
		if p.Address != parent {
			continue
		}
		for _, t := range p.MDevTypes {
			if t.ID == typeID {
				return t.Name
			}
		}
	}
	return ""
}

// nodeNameAddress turns a node device name like pci_0000_01_00_0 into 0000:01:00.0.
func nodeNameAddress(name string) string {
	p := strings.Split(strings.TrimPrefix(name, "pci_"), "_")
	if !strings.HasPrefix(name, "pci_") || len(p) != 4 {
		return ""
	}
	return fmt.Sprintf("%s:%s:%s.%s", p[0], p[1], p[2], p[3])
}

// hostMDevs lists the slices on the host (defined or only running), with the machines in
// users that have them (users may be nil).
func (c *Client) hostMDevs(l *libvirt.Libvirt, users map[string][]string) ([]HostMDev, error) {
	devs, _, err := l.ConnectListAllNodeDevices(1, uint32(libvirt.ConnectListNodeDevicesCapMdev))
	if err != nil {
		return nil, classify(err)
	}
	out := []HostMDev{}
	for _, d := range devs {
		raw, err := l.NodeDeviceGetXMLDesc(d.Name, 0)
		if err != nil {
			continue
		}
		var nd libvirtxml.NodeDevice
		if nd.Unmarshal(raw) != nil || nd.Capability.MDev == nil {
			continue
		}
		m := nd.Capability.MDev
		md := HostMDev{Name: nd.Name, UUID: strings.ToLower(m.UUID), Parent: nodeNameAddress(nd.Parent), UsedBy: []string{}}
		if md.Parent == "" {
			md.Parent = orDefault(m.ParentAddr, nd.Parent)
		}
		if m.Type != nil {
			md.Type = m.Type.ID
		}
		if a, err := l.NodeDeviceIsActive(d.Name); err == nil {
			md.Active = a == 1
		}
		if a, err := l.NodeDeviceGetAutostart(d.Name); err == nil {
			md.Autostart = a == 1
		}
		md.UsedBy = append(md.UsedBy, users[md.UUID]...)
		out = append(out, md)
	}
	slices.SortFunc(out, func(a, b HostMDev) int {
		return strings.Compare(a.Parent+" "+a.UUID, b.Parent+" "+b.UUID)
	})
	return out, nil
}

// checkMDevCreate finds the card for spec and checks it still offers a slice of that type.
func checkMDevCreate(pci []HostPCI, spec MDevSpec) (HostPCI, error) {
	i := slices.IndexFunc(pci, func(p HostPCI) bool { return p.Address == strings.ToLower(spec.Parent) })
	if i < 0 {
		return HostPCI{}, invalidf("There is no PCI device %s on this host.", spec.Parent)
	}
	p := pci[i]
	if len(p.MDevTypes) == 0 {
		return p, invalidf("%s cannot be split into mediated devices (its driver offers no types).", p.Address)
	}
	j := slices.IndexFunc(p.MDevTypes, func(t HostMDevType) bool { return t.ID == spec.Type })
	if j < 0 {
		return p, invalidf("%s does not offer the type %q.", p.Address, spec.Type)
	}
	if p.MDevTypes[j].Available == 0 {
		return p, refusedf("%s has no free %s slices left.", p.Address, orDefault(p.MDevTypes[j].Name, spec.Type))
	}
	return p, nil
}

func mdevXML(parentNode, typeID, uuid string) (string, error) {
	nd := libvirtxml.NodeDevice{Parent: parentNode, Capability: libvirtxml.NodeDeviceCapability{
		MDev: &libvirtxml.NodeDeviceMDevCapability{Type: &libvirtxml.NodeDeviceMDevCapabilityType{ID: typeID}, UUID: uuid}}}
	return nd.Marshal()
}

// newUUID returns a random (version 4) UUID.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// CreateMDev creates a slice, starts it and keeps it across host reboots. It returns its UUID.
func (c *Client) CreateMDev(spec MDevSpec) (string, error) {
	hw, err := c.HostHardware()
	if err != nil {
		return "", err
	}
	p, err := checkMDevCreate(hw.PCI, spec)
	if err != nil {
		return "", err
	}
	l, err := c.conn()
	if err != nil {
		return "", err
	}
	uuid := newUUID()
	doc, err := mdevXML(p.Name, spec.Type, uuid)
	if err != nil {
		return "", err
	}
	nd, err := l.NodeDeviceDefineXML(doc, 0)
	if err != nil {
		return "", classify(err)
	}
	if err := l.NodeDeviceCreate(nd.Name, 0); err != nil {
		_ = l.NodeDeviceUndefine(nd.Name, 0)
		return "", classify(err)
	}
	if err := l.NodeDeviceSetAutostart(nd.Name, 1); err != nil {
		c.log.Warn("mdev autostart", "name", nd.Name, "err", err)
	}
	c.log.Info("mediated device created", "parent", p.Address, "type", spec.Type, "uuid", uuid)
	return uuid, nil
}

// DeleteMDev stops and removes a slice no machine uses.
func (c *Client) DeleteMDev(uuid string) error {
	l, err := c.conn()
	if err != nil {
		return err
	}
	mdevs, err := c.hostMDevs(l, c.hostdevUsers(l).mdev)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(mdevs, func(m HostMDev) bool { return m.UUID == strings.ToLower(uuid) })
	if i < 0 {
		return fmt.Errorf("%w: no mediated device %s", ErrNotFound, uuid)
	}
	m := mdevs[i]
	if len(m.UsedBy) > 0 {
		return refusedf("The mediated device is given to %s; remove it there first.", strings.Join(m.UsedBy, ", "))
	}
	if m.Active {
		if err := l.NodeDeviceDestroy(m.Name); err != nil {
			return classify(err)
		}
	}
	// A slice made outside libvirt (only running, never defined) is gone once destroyed.
	if err := l.NodeDeviceUndefine(m.Name, 0); err != nil {
		if _, lerr := l.NodeDeviceLookupByName(m.Name); lerr == nil {
			return classify(err)
		}
	}
	c.log.Info("mediated device deleted", "uuid", m.UUID, "parent", m.Parent)
	return nil
}
