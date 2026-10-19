package virt

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"
)

// GuestInfo is what the guest agent inside a running machine reports about it.
type GuestInfo struct {
	// Connected: an agent answers. Everything else is empty when it does not.
	Connected   bool              `json:"connected"`
	OS          string            `json:"os,omitempty"` // pretty name, e.g. "Debian GNU/Linux 13 (trixie)"
	Kernel      string            `json:"kernel,omitempty"`
	Hostname    string            `json:"hostname,omitempty"`
	Timezone    string            `json:"timezone,omitempty"`
	Users       []string          `json:"users"`
	Filesystems []GuestFilesystem `json:"filesystems"`
}

type GuestFilesystem struct {
	Mountpoint string `json:"mountpoint"`
	Type       string `json:"type"`
	TotalBytes uint64 `json:"totalBytes"`
	UsedBytes  uint64 `json:"usedBytes"`
}

// agentConnected reports whether the machine's guest agent channel is connected. Asking a
// machine without a connected agent can stall, so nothing is asked then.
func agentConnected(x *libvirtxml.Domain) bool {
	if x.Devices == nil {
		return false
	}
	for _, ch := range x.Devices.Channels {
		if t := ch.Target; t != nil && t.VirtIO != nil && t.VirtIO.Name == "org.qemu.guest_agent.0" {
			return t.VirtIO.State == "connected"
		}
	}
	return false
}

// parseGuestInfo reads the typed parameters of virDomainGetGuestInfo.
func parseGuestInfo(params []libvirt.TypedParam) GuestInfo {
	g := GuestInfo{Connected: true, Users: []string{}, Filesystems: []GuestFilesystem{}}
	fs := map[int]*GuestFilesystem{}
	users := map[int]string{}
	var name, version string
	for _, p := range params {
		switch p.Field {
		case "os.pretty-name":
			g.OS = paramString(p.Value)
		case "os.name":
			name = paramString(p.Value)
		case "os.version":
			version = paramString(p.Value)
		case "os.kernel-release":
			g.Kernel = paramString(p.Value)
		case "hostname":
			g.Hostname = paramString(p.Value)
		case "timezone.name":
			g.Timezone = paramString(p.Value)
		}
		// user.<n>.name and fs.<n>.<field>
		kind, rest, _ := strings.Cut(p.Field, ".")
		idx, field, ok := strings.Cut(rest, ".")
		n, err := strconv.Atoi(idx)
		if !ok || err != nil {
			continue
		}
		switch {
		case kind == "user" && field == "name":
			users[n] = paramString(p.Value)
		case kind == "fs":
			f := fs[n]
			if f == nil {
				f = &GuestFilesystem{}
				fs[n] = f
			}
			switch field {
			case "mountpoint":
				f.Mountpoint = paramString(p.Value)
			case "fstype":
				f.Type = paramString(p.Value)
			case "total-bytes":
				f.TotalBytes = paramUint(p.Value)
			case "used-bytes":
				f.UsedBytes = paramUint(p.Value)
			}
		}
	}
	if g.OS == "" {
		g.OS = strings.TrimSpace(name + " " + version)
	}
	for i := 0; i < len(users); i++ {
		if u, ok := users[i]; ok {
			g.Users = append(g.Users, u)
		}
	}
	for i := 0; i < len(fs); i++ {
		// Pseudo file systems report no size; only real ones are worth showing.
		if f, ok := fs[i]; ok && f.TotalBytes > 0 {
			g.Filesystems = append(g.Filesystems, *f)
		}
	}
	return g
}

// GuestInfo asks the guest agent of a running machine; Connected is false without one.
func (c *Client) GuestInfo(uuid string) (GuestInfo, error) {
	none := GuestInfo{Users: []string{}, Filesystems: []GuestFilesystem{}}
	l, d, err := c.lookup(uuid)
	if err != nil {
		return none, err
	}
	x, err := c.parseDomain(l, d, false)
	if err != nil {
		return none, err
	}
	if !agentConnected(x) {
		return none, nil
	}
	types := libvirt.DomainGuestInfoUsers | libvirt.DomainGuestInfoOs | libvirt.DomainGuestInfoTimezone |
		libvirt.DomainGuestInfoHostname | libvirt.DomainGuestInfoFilesystem
	params, err := l.DomainGetGuestInfo(d, uint32(types), 0)
	if err != nil {
		c.log.Debug("guest info", "domain", d.Name, "err", err)
		return none, nil
	}
	return parseGuestInfo(params), nil
}

// SetGuestPassword sets a user's password inside a running machine through its agent.
func (c *Client) SetGuestPassword(uuid, user, password string) error {
	if strings.TrimSpace(user) == "" || password == "" {
		return invalidf("Name the user and the new password.")
	}
	l, d, err := c.lookup(uuid)
	if err != nil {
		return err
	}
	x, err := c.parseDomain(l, d, false)
	if err != nil {
		return err
	}
	if !agentConnected(x) {
		return refusedf("%s has no connected guest agent: start the machine and install qemu-guest-agent in it.", d.Name)
	}
	if err := l.DomainSetUserPassword(d, libvirt.OptString{user}, libvirt.OptString{password}, 0); err != nil {
		return fmt.Errorf("set password: %w", classify(err))
	}
	c.log.Info("guest password set", "domain", d.Name, "user", user)
	return nil
}
