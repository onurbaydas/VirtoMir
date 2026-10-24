package physnet

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"regexp"
	"sort"
	"strings"
)

// BGP through FRR (experimental, not tested on hardware): VirtoMir writes one `router bgp`
// section — this host's AS, router ID, neighbors and the subnets it announces, and with
// EVPN the VNIs of the host's VXLAN overlays — and leaves the rest of FRR's configuration
// alone. Each host has its own (router IDs differ).

// BGPNeighbor is a router this host peers with.
type BGPNeighbor struct {
	Address     string `json:"address"`
	RemoteAS    uint32 `json:"remoteAs"`
	Description string `json:"description,omitempty"`
	Password    string `json:"password,omitempty"`
	BFD         bool   `json:"bfd,omitempty"`
}

// BGPConfig is this host's BGP speaker.
type BGPConfig struct {
	Enabled   bool          `json:"enabled"`
	LocalAS   uint32        `json:"localAs"`
	RouterID  string        `json:"routerId"`
	Neighbors []BGPNeighbor `json:"neighbors"`
	// Announce are the networks this host announces (its NAT or routed subnets).
	Announce []string `json:"announce"`
	// EVPN announces the host's VXLAN overlays (advertise-all-vni), so leaf switches and
	// other hosts learn where machines are instead of flooding.
	EVPN bool `json:"evpn"`
	// AppliedAS is the AS VirtoMir last configured, to remove that section on a change.
	AppliedAS uint32 `json:"appliedAs,omitempty"`
}

var (
	descRE = regexp.MustCompile(`^[A-Za-z0-9 ._:/-]{0,80}$`)
	passRE = regexp.MustCompile(`^[!-~]{1,80}$`) // printable, no spaces
)

// Validate checks every value that ends up in FRR's configuration.
func (c BGPConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.LocalAS == 0 {
		return invalidf("the local AS number is 1 to 4294967295")
	}
	if a, err := netip.ParseAddr(c.RouterID); err != nil || !a.Is4() {
		return invalidf("the router ID is an IPv4 address (usually this host's)")
	}
	if len(c.Neighbors) == 0 {
		return invalidf("add at least one neighbor")
	}
	seen := map[string]bool{}
	for _, n := range c.Neighbors {
		if _, err := netip.ParseAddr(n.Address); err != nil {
			return invalidf("neighbor %q is not an IP address", n.Address)
		}
		if seen[n.Address] {
			return invalidf("neighbor %s is listed twice", n.Address)
		}
		seen[n.Address] = true
		if n.RemoteAS == 0 {
			return invalidf("neighbor %s needs its AS number", n.Address)
		}
		if !descRE.MatchString(n.Description) {
			return invalidf("the description of %s is letters, digits, spaces and . _ : / -", n.Address)
		}
		if n.Password != "" && !passRE.MatchString(n.Password) {
			return invalidf("the password of %s is printable characters without spaces", n.Address)
		}
	}
	for _, p := range c.Announce {
		if pf, err := netip.ParsePrefix(p); err != nil || pf.Masked() != pf {
			return invalidf("%q is not a network like 10.10.0.0/24", p)
		}
	}
	return nil
}

// FRRConfig is the section VirtoMir gives FRR.
func FRRConfig(c BGPConfig) (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "router bgp %d\n bgp router-id %s\n no bgp ebgp-requires-policy\n no bgp default ipv4-unicast\n", c.LocalAS, c.RouterID)
	for _, n := range c.Neighbors {
		fmt.Fprintf(&b, " neighbor %s remote-as %d\n", n.Address, n.RemoteAS)
		if n.Description != "" {
			fmt.Fprintf(&b, " neighbor %s description %s\n", n.Address, n.Description)
		}
		if n.Password != "" {
			fmt.Fprintf(&b, " neighbor %s password %s\n", n.Address, n.Password)
		}
		if n.BFD {
			fmt.Fprintf(&b, " neighbor %s bfd\n", n.Address)
		}
	}
	var v4, v6 []string
	for _, p := range c.Announce {
		if netip.MustParsePrefix(p).Addr().Is4() {
			v4 = append(v4, p)
		} else {
			v6 = append(v6, p)
		}
	}
	family := func(name string, nets []string, is4 bool) {
		fmt.Fprintf(&b, " !\n address-family %s\n", name)
		for _, p := range nets {
			fmt.Fprintf(&b, "  network %s\n", p)
		}
		for _, n := range c.Neighbors {
			if a := netip.MustParseAddr(n.Address); a.Is4() == is4 {
				fmt.Fprintf(&b, "  neighbor %s activate\n", n.Address)
			}
		}
		b.WriteString(" exit-address-family\n")
	}
	family("ipv4 unicast", v4, true)
	if len(v6) > 0 || hasV6(c.Neighbors) {
		family("ipv6 unicast", v6, false)
	}
	if c.EVPN {
		b.WriteString(" !\n address-family l2vpn evpn\n")
		for _, n := range c.Neighbors {
			fmt.Fprintf(&b, "  neighbor %s activate\n", n.Address)
		}
		b.WriteString("  advertise-all-vni\n exit-address-family\n")
	}
	b.WriteString("exit\n")
	return b.String(), nil
}

func hasV6(ns []BGPNeighbor) bool {
	for _, n := range ns {
		if netip.MustParseAddr(n.Address).Is6() {
			return true
		}
	}
	return false
}

// FRRStatus says whether FRR runs here with bgpd.
type FRRStatus struct {
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
	BGPD      bool   `json:"bgpd"`
}

// DetectFRR asks vtysh for FRR's version and running daemons.
func DetectFRR(ctx context.Context, r Runner) FRRStatus {
	out, err := r.Run(ctx, "vtysh", "-c", "show version")
	if err != nil {
		return FRRStatus{}
	}
	st := FRRStatus{Installed: true}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "FRRouting ") {
			st.Version = strings.Fields(strings.TrimSpace(line))[1]
			break
		}
	}
	if out, err := r.Run(ctx, "vtysh", "-c", "show daemons"); err == nil {
		st.BGPD = strings.Contains(" "+string(out)+" ", " bgpd ")
	}
	return st
}

// FRRFile is where the section is kept for people to read; FRR's own saved
// configuration holds it too after write memory.
var FRRFile = "/etc/frr/virtomir-bgp.conf"

// ApplyBGP replaces VirtoMir's BGP section in the running FRR and saves FRR's
// configuration; it returns the config with AppliedAS set. Neighbors reconnect.
func ApplyBGP(ctx context.Context, r Runner, c BGPConfig) (BGPConfig, error) {
	text := ""
	if c.Enabled {
		var err error
		if text, err = FRRConfig(c); err != nil {
			return c, err
		}
		st := DetectFRR(ctx, r)
		switch {
		case !st.Installed:
			return c, fmt.Errorf("FRR is not installed on this host (apt install frr, or dnf install frr)")
		case !st.BGPD:
			return c, fmt.Errorf("FRR runs without bgpd: set bgpd=yes in /etc/frr/daemons and restart frr")
		}
	}
	if c.AppliedAS != 0 {
		if _, err := r.Run(ctx, "vtysh", "-c", "configure terminal", "-c", fmt.Sprintf("no router bgp %d", c.AppliedAS)); err != nil {
			return c, missing(err, "FRR (vtysh)")
		}
		c.AppliedAS = 0
	}
	if c.Enabled {
		if err := os.WriteFile(FRRFile, []byte("! Written by VirtoMir; changes here are overwritten.\n"+text), 0o640); err != nil {
			return c, err
		}
		if _, err := r.Run(ctx, "vtysh", "-f", FRRFile); err != nil {
			return c, err
		}
		c.AppliedAS = c.LocalAS
	}
	if _, err := r.Run(ctx, "vtysh", "-c", "write memory"); err != nil {
		return c, err
	}
	return c, nil
}

// BGPSession is a neighbor as FRR sees it.
type BGPSession struct {
	Address     string   `json:"address"`
	RemoteAS    uint32   `json:"remoteAs"`
	Description string   `json:"description,omitempty"`
	State       string   `json:"state"` // Established, Active, Connect, Idle, …
	Uptime      string   `json:"uptime,omitempty"`
	Received    int      `json:"prefixesReceived"`
	Sent        int      `json:"prefixesSent"`
	Families    []string `json:"families"` // ipv4Unicast, ipv6Unicast, l2VpnEvpn
}

// Sessions reads `show bgp summary json` (all address families).
func Sessions(ctx context.Context, r Runner) ([]BGPSession, error) {
	out, err := r.Run(ctx, "vtysh", "-c", "show bgp summary json")
	if err != nil {
		return nil, missing(err, "FRR (vtysh)")
	}
	type family struct {
		Peers map[string]struct {
			RemoteAS uint32 `json:"remoteAs"`
			State    string `json:"state"`
			Uptime   string `json:"peerUptime"`
			PfxRcd   int    `json:"pfxRcd"`
			PfxSnt   int    `json:"pfxSnt"`
			Desc     string `json:"desc"`
		} `json:"peers"`
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("read show bgp summary: %w", err)
	}
	doc := map[string]family{}
	families := []string{}
	for f, v := range raw {
		var fam family
		if json.Unmarshal(v, &fam) == nil && fam.Peers != nil {
			doc[f] = fam
			families = append(families, f)
		}
	}
	sort.Strings(families)
	by := map[string]*BGPSession{}
	for _, f := range families {
		for addr, p := range doc[f].Peers {
			s := by[addr]
			if s == nil {
				s = &BGPSession{Address: addr, RemoteAS: p.RemoteAS, Description: p.Desc, State: p.State, Uptime: p.Uptime, Families: []string{}}
				by[addr] = s
			}
			s.Families = append(s.Families, f)
			s.Received += p.PfxRcd
			s.Sent += p.PfxSnt
		}
	}
	list := make([]BGPSession, 0, len(by))
	for _, s := range by {
		list = append(list, *s)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Address < list[j].Address })
	return list, nil
}
