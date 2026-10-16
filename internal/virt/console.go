package virt

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"

	"github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"
)

// ConsoleInfo tells the UI which consoles a machine offers right now.
type ConsoleInfo struct {
	Running bool `json:"running"`
	// VNC is the graphical console, nil when the machine has none (or it is not running).
	VNC *VNCInfo `json:"vnc,omitempty"`
	// Serial lists the text consoles (alias names); the first is the default.
	Serial []string `json:"serial"`
	// Heads is how many monitors the graphical console can show (each on its own VNC server).
	Heads int `json:"heads"`
}

type VNCInfo struct {
	// Password of the VNC server, if one is set. Only sent to users allowed to use the console.
	Password string `json:"password,omitempty"`
	Keymap   string `json:"keymap,omitempty"`
}

// vncTarget is where the VNC server of a running machine listens.
type vncTarget struct {
	network, addr string
	password      string
	keymap        string
}

func (c *Client) liveSecureXML(l *libvirt.Libvirt, d libvirt.Domain) (*libvirtxml.Domain, error) {
	raw, err := l.DomainGetXMLDesc(d, libvirt.DomainXMLSecure)
	if err != nil {
		return nil, classify(err)
	}
	var x libvirtxml.Domain
	if err := x.Unmarshal(raw); err != nil {
		return nil, err
	}
	return &x, nil
}

// findVNC returns the VNC listener of a running machine's live XML.
func findVNC(x *libvirtxml.Domain) (vncTarget, bool) {
	if x.Devices == nil {
		return vncTarget{}, false
	}
	for _, g := range x.Devices.Graphics {
		v := g.VNC
		if v == nil {
			continue
		}
		t := vncTarget{password: v.Passwd, keymap: v.Keymap}
		if v.Socket != "" {
			t.network, t.addr = "unix", v.Socket
			return t, true
		}
		host := v.Listen
		for _, l := range v.Listeners {
			switch {
			case l.Socket != nil && l.Socket.Socket != "":
				t.network, t.addr = "unix", l.Socket.Socket
				return t, true
			case l.Address != nil && l.Address.Address != "":
				host = l.Address.Address
			}
		}
		if v.Port <= 0 {
			continue // not running, or no port assigned yet
		}
		// A server listening on all addresses is reachable on loopback; prefer that.
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "127.0.0.1"
		}
		t.network, t.addr = "tcp", net.JoinHostPort(host, strconv.Itoa(v.Port))
		return t, true
	}
	return vncTarget{}, false
}

func serialAliases(x *libvirtxml.Domain) []string {
	out := []string{}
	if x.Devices == nil {
		return out
	}
	for _, c := range x.Devices.Consoles {
		if c.Alias != nil && c.Alias.Name != "" {
			out = append(out, c.Alias.Name)
		}
	}
	for _, s := range x.Devices.Serials {
		if s.Alias != nil && s.Alias.Name != "" && !contains(out, s.Alias.Name) {
			out = append(out, s.Alias.Name)
		}
	}
	return out
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// ConsoleInfo describes the consoles of a machine.
func (c *Client) ConsoleInfo(uuid string) (ConsoleInfo, error) {
	l, d, err := c.lookup(uuid)
	if err != nil {
		return ConsoleInfo{}, err
	}
	sum, err := summary(l, d)
	if err != nil {
		return ConsoleInfo{}, classify(err)
	}
	info := ConsoleInfo{Serial: []string{}, Running: isOn(sum.State) || sum.State == StatePaused}
	if !info.Running {
		return info, nil
	}
	x, err := c.liveSecureXML(l, d)
	if err != nil {
		return info, err
	}
	if t, ok := findVNC(x); ok {
		info.VNC = &VNCInfo{Password: t.password, Keymap: t.keymap}
		info.Heads = liveHeads(x)
	}
	info.Serial = serialAliases(x)
	return info, nil
}

// DialVNC connects to the VNC server of a running machine (the caller proxies the bytes).
func (c *Client) DialVNC(ctx context.Context, uuid string) (net.Conn, error) {
	l, d, err := c.lookup(uuid)
	if err != nil {
		return nil, err
	}
	x, err := c.liveSecureXML(l, d)
	if err != nil {
		return nil, err
	}
	t, ok := findVNC(x)
	if !ok {
		return nil, refusedf("%s has no graphical console right now. Is it running, with a VNC display?", d.Name)
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, t.network, t.addr)
	if err != nil {
		return nil, fmt.Errorf("connect to the VNC server of %s: %w", d.Name, err)
	}
	return conn, nil
}

// SerialConsole attaches to a text console until ctx ends or the stream closes. Input from in
// goes to the guest, guest output is written to out. An existing console session (e.g. virsh
// console) is taken over, so a reconnecting browser never finds the console "busy".
func (c *Client) SerialConsole(ctx context.Context, uuid, device string, in io.Reader, out io.Writer) error {
	l, d, err := c.lookup(uuid)
	if err != nil {
		return err
	}
	var dev libvirt.OptString
	if device != "" {
		dev = libvirt.OptString{device}
	}
	done := make(chan error, 1)
	go func() {
		done <- l.DomainOpenConsoleBidirectional(d, dev, in, out, uint32(libvirt.DomainConsoleForce))
	}()
	select {
	case err := <-done:
		return classify(err)
	case <-ctx.Done():
		return ctx.Err()
	}
}
