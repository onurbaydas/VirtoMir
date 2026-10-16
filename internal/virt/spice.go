package virt

import (
	"context"
	"fmt"
	"net"
	"slices"
	"strconv"

	"github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"
)

// SPICE console (ADR-0046): the same model as VNC — QEMU's SPICE server listens on
// loopback only and virtomird bridges each WebSocket of spice-html5 (one per SPICE channel)
// to it. SPICE brings the guest agent's clipboard and automatic resolution through its own
// spicevmc channel.

// SpiceInfo describes a running machine's SPICE console.
type SpiceInfo struct {
	Password string `json:"password,omitempty"`
}

// findSpice returns where the SPICE server of a running machine listens (plain port only;
// a TLS-only server is not reachable for the proxy).
func findSpice(x *libvirtxml.Domain) (vncTarget, bool) {
	if x.Devices == nil {
		return vncTarget{}, false
	}
	for _, g := range x.Devices.Graphics {
		s := g.Spice
		if s == nil || s.Port <= 0 {
			continue
		}
		host := s.Listen
		for _, l := range s.Listeners {
			if l.Address != nil && l.Address.Address != "" {
				host = l.Address.Address
			}
		}
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "127.0.0.1"
		}
		return vncTarget{network: "tcp", addr: net.JoinHostPort(host, strconv.Itoa(s.Port)), password: s.Passwd}, true
	}
	return vncTarget{}, false
}

// DialSPICE connects to the SPICE server of a running machine.
func (c *Client) DialSPICE(ctx context.Context, uuid string) (net.Conn, error) {
	l, d, err := c.lookup(uuid)
	if err != nil {
		return nil, err
	}
	x, err := c.liveSecureXML(l, d)
	if err != nil {
		return nil, err
	}
	t, ok := findSpice(x)
	if !ok {
		return nil, refusedf("%s has no SPICE console right now. Is it running, with a SPICE display?", d.Name)
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, t.network, t.addr)
	if err != nil {
		return nil, fmt.Errorf("connect to the SPICE server of %s: %w", d.Name, err)
	}
	return conn, nil
}

// spiceVMCChannel is SPICE's agent channel: clipboard and resolution follow the browser
// window (the guest needs spice-vdagent).
func spiceVMCChannel() libvirtxml.DomainChannel {
	return libvirtxml.DomainChannel{
		Source: &libvirtxml.DomainChardevSource{SpiceVMC: &libvirtxml.DomainChardevSourceSpiceVMC{}},
		Target: &libvirtxml.DomainChannelTarget{VirtIO: &libvirtxml.DomainChannelTargetVirtIO{Name: "com.redhat.spice.0"}},
	}
}

func isSpiceVMC(ch libvirtxml.DomainChannel) bool {
	return ch.Source != nil && ch.Source.SpiceVMC != nil
}

// displayProtocol is "vnc", "spice" or "" (no graphical console).
func displayProtocol(x *libvirtxml.Domain) string {
	if x.Devices == nil {
		return ""
	}
	for _, g := range x.Devices.Graphics {
		switch {
		case g.VNC != nil:
			return "vnc"
		case g.Spice != nil:
			return "spice"
		}
	}
	return ""
}

// setDisplay switches the graphical console of a definition between VNC and SPICE. Both
// listen on loopback with an automatic port; a password and keyboard layout stay. The
// clipboard channel follows: qemu-vdagent belongs to VNC, spicevmc to SPICE.
func setDisplay(x *libvirtxml.Domain, protocol string) error {
	if protocol != "vnc" && protocol != "spice" {
		return invalidf("The display is vnc or spice.")
	}
	if x.Devices == nil {
		x.Devices = &libvirtxml.DomainDeviceList{}
	}
	if protocol == "spice" && liveHeads(x) > 1 {
		return refusedf("Extra monitors are separate VNC servers; remove them (Hardware → video) before switching to SPICE.")
	}
	passwd, keymap := "", ""
	for _, g := range x.Devices.Graphics {
		if g.VNC != nil {
			passwd, keymap = g.VNC.Passwd, g.VNC.Keymap
		}
		if g.Spice != nil {
			passwd, keymap = g.Spice.Passwd, g.Spice.Keymap
		}
	}
	listen := []libvirtxml.DomainGraphicListener{{Address: &libvirtxml.DomainGraphicListenerAddress{Address: "127.0.0.1"}}}
	if protocol == "spice" {
		x.Devices.Graphics = []libvirtxml.DomainGraphic{{Spice: &libvirtxml.DomainGraphicSpice{Port: -1, AutoPort: "yes", Listen: "127.0.0.1",
			Passwd: passwd, Keymap: keymap, Listeners: listen,
			Image: &libvirtxml.DomainGraphicSpiceImage{Compression: "auto_glz"}}}}
	} else {
		x.Devices.Graphics = []libvirtxml.DomainGraphic{{VNC: &libvirtxml.DomainGraphicVNC{Port: -1, AutoPort: "yes", Listen: "127.0.0.1",
			Passwd: passwd, Keymap: keymap, Listeners: listen}}}
	}
	// The clipboard/agent channel follows the protocol.
	had := slices.ContainsFunc(x.Devices.Channels, isVDAgent) || slices.ContainsFunc(x.Devices.Channels, isSpiceVMC)
	x.Devices.Channels = slices.DeleteFunc(x.Devices.Channels, func(ch libvirtxml.DomainChannel) bool { return isVDAgent(ch) || isSpiceVMC(ch) })
	switch {
	case protocol == "spice":
		x.Devices.Channels = append(x.Devices.Channels, spiceVMCChannel()) // SPICE always wants its agent
	case had:
		x.Devices.Channels = append(x.Devices.Channels, vdagentChannel())
	}
	return nil
}

// SetDisplay switches a machine's console between VNC and SPICE, from its next start.
func (c *Client) SetDisplay(uuid, protocol string) error {
	l, d, err := c.lookup(uuid)
	if err != nil {
		return err
	}
	x, err := c.parseDomain(l, d, true)
	if err != nil {
		return err
	}
	if displayProtocol(x) == protocol {
		return nil
	}
	if err := setDisplay(x, protocol); err != nil {
		return err
	}
	doc, err := x.Marshal()
	if err != nil {
		return err
	}
	if _, err := l.DomainDefineXMLFlags(doc, libvirt.DomainDefineValidate); err != nil {
		return classify(err)
	}
	c.log.Info("display", "domain", d.Name, "protocol", protocol)
	return nil
}
