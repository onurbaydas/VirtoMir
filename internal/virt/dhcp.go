package virt

import (
	"fmt"
	"net"
	"net/netip"
	"strings"

	"github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"
)

// DHCPHost is a fixed address reservation: the machine with MAC always gets IP.
type DHCPHost struct {
	MAC  string `json:"mac"`
	IP   string `json:"ip"`
	Name string `json:"name,omitempty"`
}

// dhcpHostsFromXML lists the IPv4 reservations of a network definition.
func dhcpHostsFromXML(x *libvirtxml.Network) []DHCPHost {
	out := []DHCPHost{}
	for _, ip := range x.IPs {
		if ip.Family == "ipv6" || ip.DHCP == nil {
			continue
		}
		for _, h := range ip.DHCP.Hosts {
			out = append(out, DHCPHost{MAC: strings.ToLower(h.MAC), IP: h.IP, Name: h.Name})
		}
	}
	return out
}

// checkReservation validates a reservation against the network's subnet and existing entries.
func checkReservation(x *libvirtxml.Network, h DHCPHost) error {
	if _, err := net.ParseMAC(h.MAC); err != nil {
		return invalidf("%q is not a MAC address.", h.MAC)
	}
	addr, err := netip.ParseAddr(h.IP)
	if err != nil || !addr.Is4() {
		return invalidf("%q is not an IPv4 address.", h.IP)
	}
	if h.Name != "" && !hostnameRE.MatchString(h.Name) {
		return invalidf("Host name %q is not valid.", h.Name)
	}
	var subnet netip.Prefix
	var gateway netip.Addr
	for _, ip := range x.IPs {
		if ip.Family == "ipv6" || ip.Address == "" {
			continue
		}
		gw, err := netip.ParseAddr(ip.Address)
		if err != nil {
			continue
		}
		prefix := int(ip.Prefix)
		if prefix == 0 && ip.Netmask != "" {
			prefix = maskBits(ip.Netmask)
		}
		subnet, gateway = netip.PrefixFrom(gw, prefix).Masked(), gw
		if ip.DHCP == nil {
			return refusedf("Network %s has no DHCP server, so it cannot hand out reserved addresses.", x.Name)
		}
		break
	}
	if !subnet.IsValid() {
		return refusedf("Network %s has no IPv4 subnet of its own.", x.Name)
	}
	if !subnet.Contains(addr) || addr == gateway || addr == subnet.Addr() || addr == lastUsable(subnet).Next() {
		return invalidf("%s is not a usable address in %s.", h.IP, subnet)
	}
	for _, e := range dhcpHostsFromXML(x) {
		if strings.EqualFold(e.MAC, h.MAC) {
			return refusedf("%s already has a reservation (%s).", h.MAC, e.IP)
		}
		if e.IP == h.IP {
			return refusedf("%s is already reserved for %s.", h.IP, e.MAC)
		}
	}
	return nil
}

func (c *Client) networkXML(l *libvirt.Libvirt, name string, inactive bool) (libvirt.Network, *libvirtxml.Network, error) {
	n, err := l.NetworkLookupByName(name)
	if err != nil {
		return n, nil, classify(err)
	}
	var flags uint32
	if inactive {
		flags = 1 // VIR_NETWORK_XML_INACTIVE
	}
	raw, err := l.NetworkGetXMLDesc(n, flags)
	if err != nil {
		return n, nil, classify(err)
	}
	var x libvirtxml.Network
	if err := x.Unmarshal(raw); err != nil {
		return n, nil, fmt.Errorf("parse network XML: %w", err)
	}
	return n, &x, nil
}

// updateFlags affects the running network and its saved definition.
func updateFlags(l *libvirt.Libvirt, n libvirt.Network) libvirt.NetworkUpdateFlags {
	var f libvirt.NetworkUpdateFlags
	if a, err := l.NetworkIsActive(n); err == nil && a == 1 {
		f |= libvirt.NetworkUpdateAffectLive
	}
	if p, err := l.NetworkIsPersistent(n); err == nil && p == 1 {
		f |= libvirt.NetworkUpdateAffectConfig
	}
	return f
}

// AddDHCPHost reserves an address. Takes effect for the next lease the machine requests.
func (c *Client) AddDHCPHost(network string, h DHCPHost) error {
	h.MAC = strings.ToLower(strings.TrimSpace(h.MAC))
	h.IP = strings.TrimSpace(h.IP)
	l, err := c.conn()
	if err != nil {
		return err
	}
	n, x, err := c.networkXML(l, network, false)
	if err != nil {
		return err
	}
	if err := checkReservation(x, h); err != nil {
		return err
	}
	entry := libvirtxml.NetworkDHCPHost{MAC: h.MAC, IP: h.IP, Name: h.Name}
	doc, err := entry.Marshal()
	if err != nil {
		return err
	}
	if err := l.NetworkUpdateCompat(n, libvirt.NetworkUpdateCommandAddLast, libvirt.NetworkSectionIPDhcpHost, -1, doc, updateFlags(l, n)); err != nil {
		return classify(err)
	}
	c.log.Info("dhcp reservation added", "network", network, "mac", h.MAC, "ip", h.IP)
	return nil
}

// DeleteDHCPHost removes the reservation for mac.
func (c *Client) DeleteDHCPHost(network, mac string) error {
	l, err := c.conn()
	if err != nil {
		return err
	}
	n, x, err := c.networkXML(l, network, false)
	if err != nil {
		return err
	}
	for _, e := range dhcpHostsFromXML(x) {
		if !strings.EqualFold(e.MAC, mac) {
			continue
		}
		entry := libvirtxml.NetworkDHCPHost{MAC: e.MAC, IP: e.IP, Name: e.Name}
		doc, err := entry.Marshal()
		if err != nil {
			return err
		}
		if err := l.NetworkUpdateCompat(n, libvirt.NetworkUpdateCommandDelete, libvirt.NetworkSectionIPDhcpHost, -1, doc, updateFlags(l, n)); err != nil {
			return classify(err)
		}
		c.log.Info("dhcp reservation removed", "network", network, "mac", mac)
		return nil
	}
	return fmt.Errorf("%w: no reservation for %s", ErrNotFound, mac)
}
