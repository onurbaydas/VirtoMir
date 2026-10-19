package virt

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"
)

// VNetwork is a libvirt virtual network.
type VNetwork struct {
	Name       string `json:"name"`
	UUID       string `json:"uuid"`
	Active     bool   `json:"active"`
	Autostart  bool   `json:"autostart"`
	Persistent bool   `json:"persistent"`
	// nat, route, open, isolated (no forward), bridge (host bridge), macvtap modes, hostdev.
	Mode      string `json:"mode"`
	Bridge    string `json:"bridge,omitempty"`
	Subnet    string `json:"subnet,omitempty"` // e.g. 192.168.122.1/24 (gateway/prefix)
	DHCPStart string `json:"dhcpStart,omitempty"`
	DHCPEnd   string `json:"dhcpEnd,omitempty"`
	Domain    string `json:"domain,omitempty"`
	// IPv6 (ADR-0041): gateway/prefix, e.g. fd12:3456:789a::1/64, and the DHCPv6 range if
	// any (without one, guests configure themselves from router advertisements).
	Subnet6    string   `json:"subnet6,omitempty"`
	DHCP6Start string   `json:"dhcp6Start,omitempty"`
	DHCP6End   string   `json:"dhcp6End,omitempty"`
	UsedBy     []string `json:"usedBy"`
	// Fixed address reservations (static DHCP).
	DHCPHosts []DHCPHost `json:"dhcpHosts"`
	// VXLAN overlay: its VNI and the other hosts' addresses (ADR-0054).
	VNI   uint     `json:"vni,omitempty"`
	Peers []string `json:"peers,omitempty"`
	// ClusterWide: a VXLAN overlay over the whole cluster, kept on every member (#37).
	ClusterWide bool `json:"clusterWide,omitempty"`
}

func (c *Client) ListNetworks() ([]VNetwork, error) {
	l, err := c.conn()
	if err != nil {
		return nil, err
	}
	nets, _, err := l.ConnectListAllNetworks(1, 0)
	if err != nil {
		return nil, classify(err)
	}
	users := c.networkUsers(l)
	out := make([]VNetwork, 0, len(nets))
	for _, n := range nets {
		raw, err := l.NetworkGetXMLDesc(n, 0)
		if err != nil {
			continue
		}
		v, err := parseNetwork(raw)
		if err != nil {
			continue
		}
		v.UUID = formatUUID(n.UUID)
		if a, err := l.NetworkIsActive(n); err == nil {
			v.Active = a == 1
		}
		if a, err := l.NetworkGetAutostart(n); err == nil {
			v.Autostart = a == 1
		}
		if p, err := l.NetworkIsPersistent(n); err == nil {
			v.Persistent = p == 1
		}
		v.UsedBy = users[n.Name]
		if v.UsedBy == nil {
			v.UsedBy = []string{}
		}
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b VNetwork) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func parseNetwork(raw string) (VNetwork, error) {
	var x libvirtxml.Network
	if err := x.Unmarshal(raw); err != nil {
		return VNetwork{}, err
	}
	v := VNetwork{Name: x.Name, Mode: "isolated", DHCPHosts: dhcpHostsFromXML(&x)}
	if x.Forward != nil {
		v.Mode = orDefault(x.Forward.Mode, "nat")
	}
	if x.Bridge != nil {
		v.Bridge = x.Bridge.Name
	}
	if vx, ok := parseVXLANMeta(x.Metadata); ok {
		v.Mode, v.VNI, v.Peers, v.ClusterWide = "vxlan", vx.VNI, vx.peers(), vx.Cluster
	}

	if x.Domain != nil {
		v.Domain = x.Domain.Name
	}
	for _, ip := range x.IPs {
		if ip.Family == "ipv6" && ip.Address != "" && v.Subnet6 == "" {
			v.Subnet6 = fmt.Sprintf("%s/%d", ip.Address, ip.Prefix)
			if ip.DHCP != nil && len(ip.DHCP.Ranges) > 0 {
				v.DHCP6Start, v.DHCP6End = ip.DHCP.Ranges[0].Start, ip.DHCP.Ranges[0].End
			}
		}
	}
	for _, ip := range x.IPs {
		if ip.Family == "ipv6" || ip.Address == "" {
			continue
		}
		prefix := int(ip.Prefix)
		if prefix == 0 && ip.Netmask != "" {
			prefix = maskBits(ip.Netmask)
		}
		v.Subnet = fmt.Sprintf("%s/%d", ip.Address, prefix)
		if ip.DHCP != nil && len(ip.DHCP.Ranges) > 0 {
			v.DHCPStart, v.DHCPEnd = ip.DHCP.Ranges[0].Start, ip.DHCP.Ranges[0].End
		}
		break
	}
	return v, nil
}

func maskBits(mask string) int {
	a, err := netip.ParseAddr(mask)
	if err != nil || !a.Is4() {
		return 0
	}
	n := 0
	for _, b := range a.As4() {
		for ; b != 0; b <<= 1 {
			if b&0x80 == 0 {
				return n
			}
			n++
		}
	}
	return n
}

// networkUsers maps network names to the machines with an adapter on them.
func (c *Client) networkUsers(l *libvirt.Libvirt) map[string][]string {
	out := map[string][]string{}
	doms, _, err := l.ConnectListAllDomains(1, libvirt.ConnectListDomainsActive|libvirt.ConnectListDomainsInactive)
	if err != nil {
		return out
	}
	for _, d := range doms {
		x, err := c.parseDomain(l, d, false)
		if err != nil || x.Devices == nil {
			continue
		}
		for _, n := range x.Devices.Interfaces {
			if n.Source != nil && n.Source.Network != nil {
				name := n.Source.Network.Network
				if !slices.Contains(out[name], d.Name) {
					out[name] = append(out[name], d.Name)
				}
			}
		}
	}
	return out
}

// Lease is one DHCP lease handed out by a libvirt network.
type Lease struct {
	MAC      string `json:"mac"`
	IP       string `json:"ip"` // with prefix
	Hostname string `json:"hostname,omitempty"`
	Expires  int64  `json:"expires"` // unix seconds
	Machine  string `json:"machine,omitempty"`
}

func (c *Client) NetworkLeases(name string) ([]Lease, error) {
	l, err := c.conn()
	if err != nil {
		return nil, err
	}
	n, err := l.NetworkLookupByName(name)
	if err != nil {
		return nil, classify(err)
	}
	leases, _, err := l.NetworkGetDhcpLeases(n, nil, 1, 0)
	if err != nil {
		return nil, classify(err)
	}
	byMAC := c.machinesByMAC(l)
	out := make([]Lease, 0, len(leases))
	for _, le := range leases {
		x := Lease{IP: fmt.Sprintf("%s/%d", le.Ipaddr, le.Prefix), Expires: le.Expirytime}
		if len(le.Mac) > 0 {
			x.MAC = strings.ToLower(le.Mac[0])
			x.Machine = byMAC[x.MAC]
		}
		if len(le.Hostname) > 0 {
			x.Hostname = le.Hostname[0]
		}
		out = append(out, x)
	}
	return out, nil
}

func (c *Client) machinesByMAC(l *libvirt.Libvirt) map[string]string {
	out := map[string]string{}
	doms, _, err := l.ConnectListAllDomains(1, libvirt.ConnectListDomainsActive|libvirt.ConnectListDomainsInactive)
	if err != nil {
		return out
	}
	for _, d := range doms {
		x, err := c.parseDomain(l, d, false)
		if err != nil || x.Devices == nil {
			continue
		}
		for _, n := range x.Devices.Interfaces {
			if n.MAC != nil {
				out[strings.ToLower(n.MAC.Address)] = d.Name
			}
		}
	}
	return out
}

// NetworkSpec describes a new network.
//   - nat / route / isolated: a new bridge with its own subnet (DHCP optional)
//   - bridge: machines join an existing host bridge (e.g. br0) and the physical LAN
type NetworkSpec struct {
	Name   string `json:"name"`
	Mode   string `json:"mode"`
	Subnet string `json:"subnet,omitempty"` // e.g. 192.168.100.0/24
	DHCP   bool   `json:"dhcp"`
	DHCPStart string `json:"dhcpStart,omitempty"`
	DHCPEnd   string `json:"dhcpEnd,omitempty"`
	Bridge string `json:"bridge,omitempty"` // host bridge, bridge mode only
	// OVS: the host bridge is an Open vSwitch bridge (bridge mode). VLAN tags every
	// machine on this network (bridge mode with OVS); 0 = untagged.
	OVS  bool `json:"ovs,omitempty"`
	VLAN uint `json:"vlan,omitempty"`
	// Subnet6 adds IPv6 (nat / route / isolated), a /64 such as fd12:3456:789a::/64; the
	// host takes ::1 and sends router advertisements, so guests configure themselves
	// (SLAAC). DHCP6 also hands out addresses ::100 – ::ffff. Either Subnet or Subnet6 (or
	// both) is needed. In nat mode IPv6 is translated too (NAT66, libvirt ≥ 6.5).
	Subnet6 string `json:"subnet6,omitempty"`
	DHCP6   bool   `json:"dhcp6,omitempty"`
	DHCP6Start string `json:"dhcp6Start,omitempty"`
	DHCP6End   string `json:"dhcp6End,omitempty"`
	// VXLAN: the VNI (1 – 16777215, the same on every host) and the other hosts' IP
	// addresses. With a subnet this host is the overlay's gateway (NAT, DHCP); without one
	// the network is plain layer 2 here, served by the gateway elsewhere.
	VNI   uint     `json:"vni,omitempty"`
	Peers []string `json:"peers,omitempty"`
	// Cluster marks an overlay over the whole cluster: members that join later get it too,
	// and every member's peer list follows the member list.
	Cluster bool `json:"cluster,omitempty"`
}

// buildNetwork turns a spec into network XML: gateway .1, DHCP .2 to the last usable address.
func buildNetwork(spec NetworkSpec) (*libvirtxml.Network, error) {
	if err := checkName("Network", spec.Name); err != nil {
		return nil, err
	}
	x := &libvirtxml.Network{Name: spec.Name}
	switch spec.Mode {
	case "bridge":
		if spec.Bridge == "" {
			return nil, invalidf("Name the host bridge the machines should join, e.g. br0.")
		}
		x.Forward = &libvirtxml.NetworkForward{Mode: "bridge"}
		x.Bridge = &libvirtxml.NetworkBridge{Name: spec.Bridge}
		if spec.OVS {
			x.VirtualPort = &libvirtxml.NetworkVirtualPort{Params: &libvirtxml.NetworkVirtualPortParams{OpenVSwitch: &libvirtxml.NetworkVirtualPortParamsOpenVSwitch{}}}
		}
		if spec.VLAN != 0 {
			if !spec.OVS {
				return nil, invalidf("VLAN tags on a bridge network need an Open vSwitch bridge; on a plain Linux bridge, use a bridge on the VLAN interface (e.g. br0.10) instead.")
			}
			if spec.VLAN > 4094 {
				return nil, invalidf("A VLAN ID is between 1 and 4094.")
			}
			x.VLAN = &libvirtxml.NetworkVLAN{Tags: []libvirtxml.NetworkVLANTag{{ID: spec.VLAN}}}
		}
		return x, nil
	case "nat", "route", "isolated", "open", "vxlan":
		if spec.VLAN != 0 || spec.OVS {
			return nil, invalidf("VLAN tags and Open vSwitch apply to bridge networks only.")
		}
	}
	switch spec.Mode {
	case "nat", "route":
		x.Forward = &libvirtxml.NetworkForward{Mode: spec.Mode}
	case "open":
		x.Forward = &libvirtxml.NetworkForward{Mode: "open"}
	case "vxlan":
		if err := vxlanNetwork(x, spec); err != nil {
			return nil, err
		}
		if spec.Subnet == "" && spec.Subnet6 == "" {
			return x, nil // plain layer 2 on this host
		}
		x.Forward = &libvirtxml.NetworkForward{Mode: "nat"}
	case "isolated":
	default:
		return nil, invalidf("Unknown network mode %q.", spec.Mode)
	}

	if spec.Subnet == "" && spec.Subnet6 == "" {
		return nil, invalidf("Give the network an IPv4 subnet, an IPv6 subnet, or both.")
	}
	if spec.Subnet != "" {
		if (spec.DHCPStart != "" || spec.DHCPEnd != "") && !spec.DHCP {
			spec.DHCP = true
		}
		ip, err := ipv4Net(spec)
		if err != nil {
			return nil, err
		}
		x.IPs = append(x.IPs, ip)
	} else if spec.DHCP {
		return nil, invalidf("DHCP (IPv4) needs an IPv4 subnet.")
	}
	if spec.Subnet6 != "" {
		if (spec.DHCP6Start != "" || spec.DHCP6End != "") && !spec.DHCP6 {
			spec.DHCP6 = true
		}
		ip, err := ipv6Net(spec)
		if err != nil {
			return nil, err
		}
		x.IPs = append(x.IPs, ip)
		if spec.Mode == "nat" {
			x.Forward.NAT = &libvirtxml.NetworkForwardNAT{IPv6: "yes"}
		}
	} else if spec.DHCP6 {
		return nil, invalidf("DHCPv6 needs an IPv6 subnet.")
	}
	return x, nil
}

// ipv6Net: gateway ::1 of a /64, DHCPv6 ::100 – ::ffff.
func ipv6Net(spec NetworkSpec) (libvirtxml.NetworkIP, error) {
	pfx, err := netip.ParsePrefix(spec.Subnet6)
	if err != nil || !pfx.Addr().Is6() || pfx.Addr().Is4In6() {
		return libvirtxml.NetworkIP{}, invalidf("The IPv6 subnet is a /64 like fd12:3456:789a::/64.")
	}
	if pfx.Bits() != 64 {
		return libvirtxml.NetworkIP{}, invalidf("Use a /64 for IPv6: guests configure themselves (SLAAC) only on a /64.")
	}
	pfx = pfx.Masked()
	if pfx.Addr().IsLinkLocalUnicast() || pfx.Addr().IsMulticast() || pfx.Addr().IsLoopback() {
		return libvirtxml.NetworkIP{}, invalidf("Use a unique local (fd…) or global prefix, not a link-local or multicast one.")
	}
	b := pfx.Addr().As16()
	at := func(hi, lo byte) netip.Addr {
		c := b
		c[14], c[15] = hi, lo
		return netip.AddrFrom16(c)
	}
	ip := libvirtxml.NetworkIP{Family: "ipv6", Address: at(0, 1).String(), Prefix: 64}
	if spec.DHCP6 {
		start := at(1, 0)
		end := at(0xff, 0xff)
		if spec.DHCP6Start != "" {
			s, err := netip.ParseAddr(spec.DHCP6Start)
			if err != nil || !s.Is6() || !pfx.Contains(s) {
				return libvirtxml.NetworkIP{}, invalidf("dhcp6Start must be a valid IPv6 address in subnet %s.", spec.Subnet6)
			}
			start = s
		}
		if spec.DHCP6End != "" {
			e, err := netip.ParseAddr(spec.DHCP6End)
			if err != nil || !e.Is6() || !pfx.Contains(e) {
				return libvirtxml.NetworkIP{}, invalidf("dhcp6End must be a valid IPv6 address in subnet %s.", spec.Subnet6)
			}
			end = e
		}
		if end.Less(start) {
			return libvirtxml.NetworkIP{}, invalidf("DHCPv6 start address %s must be before end address %s.", start, end)
		}
		ip.DHCP = &libvirtxml.NetworkDHCP{Ranges: []libvirtxml.NetworkDHCPRange{{Start: start.String(), End: end.String()}}}
	}
	return ip, nil
}

func ipv4Net(spec NetworkSpec) (libvirtxml.NetworkIP, error) {
	pfx, err := netip.ParsePrefix(spec.Subnet)
	if err != nil || !pfx.Addr().Is4() {
		return libvirtxml.NetworkIP{}, invalidf("Subnet must be an IPv4 network like 192.168.100.0/24.")
	}
	pfx = pfx.Masked()
	if pfx.Bits() < 16 || pfx.Bits() > 29 {
		return libvirtxml.NetworkIP{}, invalidf("Use a subnet between /16 and /29.")
	}
	gw := pfx.Addr().Next()
	ip := libvirtxml.NetworkIP{Address: gw.String(), Prefix: uint(pfx.Bits())}
	if spec.DHCP {
		last := lastUsable(pfx)
		start := gw.Next()
		if spec.DHCPStart != "" {
			s, err := netip.ParseAddr(spec.DHCPStart)
			if err != nil || !s.Is4() || !pfx.Contains(s) {
				return libvirtxml.NetworkIP{}, invalidf("dhcpStart must be a valid IPv4 address in subnet %s.", spec.Subnet)
			}
			start = s
		}
		if spec.DHCPEnd != "" {
			e, err := netip.ParseAddr(spec.DHCPEnd)
			if err != nil || !e.Is4() || !pfx.Contains(e) {
				return libvirtxml.NetworkIP{}, invalidf("dhcpEnd must be a valid IPv4 address in subnet %s.", spec.Subnet)
			}
			last = e
		}
		if !start.Less(last) && start != last {
			return libvirtxml.NetworkIP{}, invalidf("The subnet is too small for DHCP.")
		}
		if last.Less(start) {
			return libvirtxml.NetworkIP{}, invalidf("DHCP start address %s must be before end address %s.", start, last)
		}
		ip.DHCP = &libvirtxml.NetworkDHCP{Ranges: []libvirtxml.NetworkDHCPRange{{Start: start.String(), End: last.String()}}}
	}
	return ip, nil
}

// lastUsable is the address before the broadcast address.
func lastUsable(p netip.Prefix) netip.Addr {
	b := p.Addr().As4()
	host := uint32(1)<<(32-p.Bits()) - 1
	v := (uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])) | host
	v-- // broadcast → last usable
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}

// CreateNetwork defines, starts and autostarts a network.
func (c *Client) CreateNetwork(spec NetworkSpec) error {
	x, err := buildNetwork(spec)
	if err != nil {
		return err
	}
	doc, err := x.Marshal()
	if err != nil {
		return err
	}
	l, err := c.conn()
	if err != nil {
		return err
	}
	n, err := l.NetworkDefineXML(doc)
	if err != nil {
		return classify(err)
	}
	if err := l.NetworkCreate(n); err != nil {
		_ = l.NetworkUndefine(n)
		return classify(err)
	}
	_ = l.NetworkSetAutostart(n, 1)
	c.log.Info("network created", "network", spec.Name, "mode", spec.Mode)
	return nil
}

func (c *Client) NetworkAction(name, action string) error {
	l, err := c.conn()
	if err != nil {
		return err
	}
	n, err := l.NetworkLookupByName(name)
	if err != nil {
		return classify(err)
	}
	switch action {
	case "start":
		err = l.NetworkCreate(n)
	case "stop":
		err = l.NetworkDestroy(n)
	default:
		return fmt.Errorf("%w: unknown action %s", ErrNotFound, action)
	}
	return classify(err)
}

func (c *Client) SetNetworkAutostart(name string, on bool) error {
	l, err := c.conn()
	if err != nil {
		return err
	}
	n, err := l.NetworkLookupByName(name)
	if err != nil {
		return classify(err)
	}
	return classify(l.NetworkSetAutostart(n, boolInt(on)))
}

// DeleteNetwork stops and removes a network no machine is connected to.
func (c *Client) DeleteNetwork(name string) error {
	l, err := c.conn()
	if err != nil {
		return err
	}
	n, err := l.NetworkLookupByName(name)
	if err != nil {
		return classify(err)
	}
	if users := c.networkUsers(l)[name]; len(users) > 0 {
		return refusedf("%s is used by %s. Move their adapters to another network first.", name, strings.Join(users, ", "))
	}
	if a, err := l.NetworkIsActive(n); err == nil && a == 1 {
		if err := l.NetworkDestroy(n); err != nil {
			return classify(err)
		}
	}
	if p, err := l.NetworkIsPersistent(n); err == nil && p == 1 {
		if err := l.NetworkUndefine(n); err != nil {
			return classify(err)
		}
	}
	c.log.Info("network deleted", "network", name)
	return nil
}
