package virt

import (
	"encoding/xml"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"
)

// VXLAN networks (ADR-0054): libvirt creates the bridge (vxbr<VNI>, MTU 1450 to leave room
// for the 50-byte VXLAN header); virtomird adds the tunnel device and the peers to it
// (internal/overlay). The VNI and peers are kept in the network's <metadata>.

const (
	vxlanURI = "https://github.com/onurbaydas/virtomir/xmlns/vxlan/1"
	// VXLANMTU is the bridge and guest MTU on a 1500-byte underlay.
	VXLANMTU = 1450
	maxVNI   = 1<<24 - 1
)

type vxlanMeta struct {
	XMLName xml.Name `xml:"https://github.com/onurbaydas/virtomir/xmlns/vxlan/1 vxlan"`
	VNI     uint     `xml:"vni,attr"`
	Cluster bool     `xml:"cluster,attr,omitempty"`
	Peer    []struct {
		Address string `xml:"address,attr"`
	} `xml:"peer"`
}

func (m vxlanMeta) peers() []string {
	out := make([]string, 0, len(m.Peer))
	for _, p := range m.Peer {
		out = append(out, p.Address)
	}
	return out
}

// VXLANBridge is the bridge of the VXLAN network with vni.
func VXLANBridge(vni uint) string { return fmt.Sprintf("vxbr%d", vni) }

// vxlanNetwork fills in the bridge, MTU and metadata of a VXLAN network.
func vxlanNetwork(x *libvirtxml.Network, spec NetworkSpec) error {
	raw, err := vxlanMetaXML(spec.VNI, spec.Peers, spec.Cluster)
	if err != nil {
		return err
	}
	x.Bridge = &libvirtxml.NetworkBridge{Name: VXLANBridge(spec.VNI), STP: "off", Delay: "0"}
	x.MTU = &libvirtxml.NetworkMTU{Size: VXLANMTU}
	x.Metadata = &libvirtxml.NetworkMetadata{XML: raw}
	return nil
}

// vxlanMetaXML validates a VXLAN network's VNI and peers and returns its <metadata> element.
func vxlanMetaXML(vni uint, peers []string, cluster bool) (string, error) {
	if vni < 1 || vni > maxVNI {
		return "", invalidf("A VXLAN network identifier (VNI) is between 1 and %d.", maxVNI)
	}
	if len(peers) > 64 {
		return "", invalidf("At most 64 peers.")
	}
	m := vxlanMeta{VNI: vni, Cluster: cluster}
	seen := map[netip.Addr]bool{}
	for _, p := range peers {
		a, err := netip.ParseAddr(strings.TrimSpace(p))
		if err != nil || a.IsUnspecified() || a.IsMulticast() || a.IsLoopback() {
			return "", invalidf("%q is not a peer address; give the other hosts' IP addresses.", p)
		}
		a = a.Unmap()
		if !seen[a] {
			seen[a] = true
			m.Peer = append(m.Peer, struct {
				Address string `xml:"address,attr"`
			}{a.String()})
		}
	}
	raw, err := xml.Marshal(m)
	return string(raw), err
}

// SetVXLANPeers replaces the peers of VXLAN network name, live and in its definition; the
// tunnel follows within seconds (keepTunnels).
func (c *Client) SetVXLANPeers(name string, peers []string) error {
	l, err := c.conn()
	if err != nil {
		return err
	}
	n, err := l.NetworkLookupByName(name)
	if err != nil {
		return classify(err)
	}
	raw, err := l.NetworkGetXMLDesc(n, uint32(libvirt.NetworkXMLInactive))
	if err != nil {
		return classify(err)
	}
	var x libvirtxml.Network
	if err := x.Unmarshal(raw); err != nil {
		return err
	}
	m, ok := parseVXLANMeta(x.Metadata)
	if !ok {
		return invalidf("%s is not a VXLAN network.", name)
	}
	doc, err := vxlanMetaXML(m.VNI, peers, m.Cluster)
	if err != nil {
		return err
	}
	flags := libvirt.NetworkUpdateAffectConfig
	if a, err := l.NetworkIsActive(n); err == nil && a == 1 {
		flags |= libvirt.NetworkUpdateAffectLive
	}
	if err := l.NetworkSetMetadata(n, int32(libvirt.NetworkMetadataElement), libvirt.OptString{doc}, libvirt.OptString{"vxlan"}, libvirt.OptString{vxlanURI}, uint32(flags)); err != nil {
		return classify(err)
	}
	c.log.Info("vxlan peers updated", "network", name, "peers", peers)
	return nil
}

func parseVXLANMeta(md *libvirtxml.NetworkMetadata) (vxlanMeta, bool) {
	if md == nil || !strings.Contains(md.XML, vxlanURI) {
		return vxlanMeta{}, false
	}
	// innerxml may hold other applications' elements too: find ours.
	dec := xml.NewDecoder(strings.NewReader(md.XML))
	for {
		tok, err := dec.Token()
		if err != nil {
			return vxlanMeta{}, false
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Space == vxlanURI && se.Name.Local == "vxlan" {
			var m vxlanMeta
			if dec.DecodeElement(&m, &se) != nil || m.VNI < 1 || m.VNI > maxVNI {
				return vxlanMeta{}, false
			}
			return m, true
		}
	}
}

// Tunnel is a VXLAN tunnel an active network needs on this host.
type Tunnel struct {
	VNI    uint
	Bridge string
	Peers  []netip.Addr
}

// VXLANTunnels lists the tunnels of the active VXLAN networks.
func (c *Client) VXLANTunnels() ([]Tunnel, error) {
	nets, err := c.ListNetworks()
	if err != nil {
		return nil, err
	}
	var out []Tunnel
	for _, n := range nets {
		if n.Mode != "vxlan" || !n.Active || n.Bridge == "" {
			continue
		}
		t := Tunnel{VNI: n.VNI, Bridge: n.Bridge}
		for _, p := range n.Peers {
			if a, err := netip.ParseAddr(p); err == nil {
				t.Peers = append(t.Peers, a)
			}
		}
		slices.SortFunc(t.Peers, func(a, b netip.Addr) int { return a.Compare(b) })
		out = append(out, t)
	}
	return out, nil
}
