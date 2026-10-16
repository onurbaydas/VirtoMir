package hostnet

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/onurbaydas/virtomir/internal/hostinfo"
)

// systemd-networkd: VirtoMir only adds files, all named 05-virtomir-*, so they are read
// before the usual 10-/50-/… files and win the [Match] for the ports. Nothing of the host's
// own configuration is edited: removing VirtoMir's files gives the ports back to it. A VLAN
// is attached to its parent with a drop-in next to the parent's .network file.
type networkdBackend struct{ m *Manager }

func (b *networkdBackend) name() string { return "networkd" }

const networkdPrefix = "05-virtomir-"

func (b *networkdBackend) dir() string { return b.m.path("etc/systemd/network") }

// networkFile asks networkd which .network file configures an interface.
func (b *networkdBackend) networkFile(ctx context.Context, name string) string {
	out, err := b.m.Run(ctx, "networkctl", "status", "--no-pager", name)
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), ":"); ok && k == "Network File" {
			v = strings.TrimSpace(v)
			if v != "n/a" {
				return v
			}
		}
	}
	return ""
}

// ini is a minimal reader for systemd unit files: sections in order, with their lines.
type iniSection struct {
	name  string
	lines []string
}

func parseINI(s string) []iniSection {
	var out []iniSection
	for _, l := range strings.Split(s, "\n") {
		t := strings.TrimSpace(l)
		switch {
		case t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, ";"):
		case strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]"):
			out = append(out, iniSection{name: t[1 : len(t)-1]})
		case len(out) > 0:
			out[len(out)-1].lines = append(out[len(out)-1].lines, t)
		}
	}
	return out
}

// networkdAddrKeys in [Network] are the IP configuration that moves to a bridge or bond;
// these sections move whole.
var (
	networkdAddrKeys     = []string{"DHCP", "Address", "Gateway", "DNS", "Domains", "IPv6AcceptRA", "IPv6PrivacyExtensions", "LinkLocalAddressing", "NTP", "IPForward", "IPMasquerade", "LLMNR", "MulticastDNS"}
	networkdAddrSections = []string{"Address", "Route", "DHCP", "DHCPv4", "DHCPv6", "IPv6AcceptRA", "RoutingPolicyRule", "DHCPPrefixDelegation"}
)

var unitNameRE = regexp.MustCompile(`^[A-Za-z0-9._@-]+$`)

func (b *networkdBackend) create(ctx context.Context, s Spec, ifs map[string]hostinfo.Interface) (plan, error) {
	p := newPlan()
	base := filepath.Join(b.dir(), networkdPrefix+s.Name)
	var nd strings.Builder
	fmt.Fprintf(&nd, "# Created by VirtoMir.\n[NetDev]\nName=%s\n", s.Name)
	if mac := ifs[s.TakeAddressFrom].MAC; s.Kind == "bridge" && s.TakeAddressFrom != "" && mac != "" {
		fmt.Fprintf(&nd, "MACAddress=%s\n", mac) // the port's MAC: DHCP hands out the same address
	}
	switch s.Kind {
	case "bridge":
		fmt.Fprintf(&nd, "Kind=bridge\n\n[Bridge]\nSTP=%s\nForwardDelaySec=0\n", yesNo(s.STP))
	case "bond":
		fmt.Fprintf(&nd, "Kind=bond\n\n[Bond]\nMode=%s\nMIIMonitorSec=100ms\n", s.BondMode)
	case "vlan":
		fmt.Fprintf(&nd, "Kind=vlan\n\n[VLAN]\nId=%d\n", s.VLAN)
	}
	p.writes[base+".netdev"] = []byte(nd.String())

	var net strings.Builder
	fmt.Fprintf(&net, "# Created by VirtoMir.\n[Match]\nName=%s\n\n", s.Name)
	if s.TakeAddressFrom != "" {
		src := b.networkFile(ctx, s.TakeAddressFrom)
		if src == "" {
			return plan{}, refusedf("systemd-networkd does not configure %s, so VirtoMir cannot move its address.", s.TakeAddressFrom)
		}
		raw, err := os.ReadFile(b.m.path(src))
		if err != nil {
			return plan{}, err
		}
		sections := parseINI(string(raw))
		var keep []string
		for _, sec := range sections {
			if sec.name != "Network" {
				continue
			}
			for _, l := range sec.lines {
				k, _, _ := strings.Cut(l, "=")
				k = strings.TrimSpace(k)
				switch {
				case k == "VLAN" || k == "MACVLAN" || k == "VXLAN" || k == "Tunnel":
					return plan{}, refusedf("%s also carries %s; moving it into a %s is not something VirtoMir does for you.", s.TakeAddressFrom, l, s.Kind)
				case slices.Contains(networkdAddrKeys, k):
					keep = append(keep, l)
				}
			}
		}
		if len(keep) == 0 {
			return plan{}, refusedf("%s has no address settings in %s.", s.TakeAddressFrom, src)
		}
		fmt.Fprintf(&net, "[Network]\n%s\nConfigureWithoutCarrier=yes\n", strings.Join(keep, "\n"))
		for _, sec := range sections {
			if slices.Contains(networkdAddrSections, sec.name) {
				fmt.Fprintf(&net, "\n[%s]\n%s\n", sec.name, strings.Join(sec.lines, "\n"))
			}
		}
	} else {
		net.WriteString("[Network]\nLinkLocalAddressing=no\nConfigureWithoutCarrier=yes\n")
	}
	p.writes[base+".network"] = []byte(net.String())

	role := map[string]string{"bridge": "Bridge", "bond": "Bond"}[s.Kind]
	reconf := []cmd{}
	for _, port := range s.Ports {
		if src := b.networkFile(ctx, port); src != "" && filepath.Base(src) < networkdPrefix {
			return plan{}, refusedf("%s is configured by %s, which networkd reads before VirtoMir's files. Rename it to sort after %s.", port, src, networkdPrefix)
		}
		p.writes[base+"-port-"+port+".network"] = []byte(fmt.Sprintf("# Created by VirtoMir: %s is a port of %s.\n[Match]\nName=%s\n\n[Network]\n%s=%s\nLinkLocalAddressing=no\n", port, s.Name, port, role, s.Name))
		reconf = append(reconf, try("networkctl", "reconfigure", port))
	}
	if s.Kind == "vlan" {
		src := b.networkFile(ctx, s.Parent)
		if src == "" {
			// The parent is not configured at all: give it a minimal file of VirtoMir's own.
			p.writes[filepath.Join(b.dir(), networkdPrefix+s.Name+"-parent-"+s.Parent+".network")] = []byte(fmt.Sprintf("# Created by VirtoMir: carries VLAN %s.\n[Match]\nName=%s\n\n[Network]\nVLAN=%s\nLinkLocalAddressing=no\n", s.Name, s.Parent, s.Name))
		} else {
			if !unitNameRE.MatchString(filepath.Base(src)) {
				return plan{}, refusedf("Unexpected network file name %s.", src)
			}
			p.writes[filepath.Join(b.dir(), filepath.Base(src)+".d", "90-virtomir-"+s.Name+".conf")] = []byte(fmt.Sprintf("# Created by VirtoMir.\n[Network]\nVLAN=%s\n", s.Name))
		}
		reconf = append(reconf, try("networkctl", "reconfigure", s.Parent))
	}
	p.apply = append([]cmd{must("networkctl", "reload")}, reconf...)
	p.apply = append(p.apply, try("networkctl", "reconfigure", s.Name))
	p.undo = []cmd{try("ip", "link", "delete", s.Name)}
	p.reapply = append([]cmd{try("networkctl", "reload")}, reconf...)
	p.record = &Record{}
	return p, nil
}

func (b *networkdBackend) remove(ctx context.Context, r Record) (plan, error) {
	p := newPlan()
	mine, _ := filepath.Glob(filepath.Join(b.dir(), networkdPrefix+r.Name+".*"))
	more, _ := filepath.Glob(filepath.Join(b.dir(), networkdPrefix+r.Name+"-*.network"))
	drop, _ := filepath.Glob(filepath.Join(b.dir(), "*.d", "90-virtomir-"+r.Name+".conf"))
	for _, f := range append(append(mine, more...), drop...) {
		p.writes[f] = nil
	}
	reconf := []cmd{}
	for _, port := range append(append([]string{}, r.Ports...), r.Parent) {
		if port != "" {
			reconf = append(reconf, try("networkctl", "reconfigure", port))
		}
	}
	p.apply = append([]cmd{must("networkctl", "reload"), try("ip", "link", "delete", r.Name)}, reconf...)
	p.reapply = append([]cmd{try("networkctl", "reload"), try("networkctl", "reconfigure", r.Name)}, reconf...)
	return p, nil
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
