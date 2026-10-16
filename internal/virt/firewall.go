package virt

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"
)

// A machine's firewall is a libvirt network filter (nwfilter) of its own, referenced by each
// of its network adapters (ADR-0033). libvirt turns it into iptables/ebtables rules on the
// host side of the adapter, so the guest cannot change it. Rules are stateful: allowing
// a connection in one direction also lets its replies through.
//
// IPv6 (ADR-0041): a rule without a remote address applies to IPv4 and IPv6 alike (written
// as a pair of nwfilter rules with the same priority); one with an address applies to that
// address's family. The default policies cover both families; when one drops, IPv6 neighbor
// discovery and DHCPv6 replies stay allowed, without them IPv6 would not work at all.

// FirewallRule is one rule, in the guest's view: "in" is traffic to the machine.
type FirewallRule struct {
	Direction string `json:"direction"` // in, out
	Action    string `json:"action"`    // accept, drop, reject
	Protocol  string `json:"protocol"`  // all, tcp, udp, icmp
	// Remote is the other end: an IPv4 or IPv6 address or network ("" anyone, both families).
	Remote string `json:"remote,omitempty"`
	// Ports on the machine for "in", on the remote side for "out": "22" or "8000-8100".
	Ports   string `json:"ports,omitempty"`
	Comment string `json:"comment,omitempty"`
}

// Firewall is a machine's whole firewall.
type Firewall struct {
	Enabled bool `json:"enabled"`
	// DefaultIn/DefaultOut apply to IPv4 and IPv6 traffic no rule matched: accept or drop.
	DefaultIn  string `json:"defaultIn"`
	DefaultOut string `json:"defaultOut"`
	// AntiSpoof adds libvirt's clean-traffic filter: the machine may only send with its own
	// MAC and IP address (no spoofing, no rogue DHCP server).
	AntiSpoof bool           `json:"antiSpoof"`
	Rules     []FirewallRule `json:"rules"`

	// Read only: adapters the filter applies to, and those it cannot (macvtap, hostdev).
	Adapters    []string `json:"adapters,omitempty"`
	Unsupported []string `json:"unsupported,omitempty"`
}

// FirewallResult tells whether a change reached the running machine.
type FirewallResult struct {
	LiveError string `json:"liveError,omitempty"`
}

const (
	rulePriority    = 100 // user rules: 100, 101, …
	basicsPriority  = 890 // IPv6 neighbor discovery and DHCPv6, ahead of a default drop
	defaultPriority = 900 // the default policies come last
)

// FilterName is the nwfilter of a machine.
func FilterName(uuid string) string { return "virtomir-" + strings.ToLower(uuid) }

func (r FirewallRule) validate() error {
	if r.Direction != "in" && r.Direction != "out" {
		return invalidf("Direction must be in or out.")
	}
	switch r.Action {
	case "accept", "drop", "reject":
	default:
		return invalidf("Action must be accept, drop or reject.")
	}
	switch r.Protocol {
	case "all", "tcp", "udp", "icmp":
	default:
		return invalidf("Protocol must be all, tcp, udp or icmp.")
	}
	if r.Remote != "" {
		if _, _, _, err := parseRemote(r.Remote); err != nil {
			return err
		}
	}
	if r.Ports != "" {
		if r.Protocol != "tcp" && r.Protocol != "udp" {
			return invalidf("Ports only apply to tcp and udp.")
		}
		if _, _, err := parsePorts(r.Ports); err != nil {
			return err
		}
	}
	if len(r.Comment) > 200 || strings.ContainsAny(r.Comment, "\"\n") {
		return invalidf("The comment must be one line without quotes, at most 200 characters.")
	}
	return nil
}

// parseRemote splits "10.0.0.0/8" or "2001:db8::/32" into address and prefix length (""
// for a single address) and tells whether it is IPv6.
func parseRemote(s string) (ip, mask string, v6 bool, err error) {
	if strings.Contains(s, "/") {
		addr, n, err := net.ParseCIDR(s)
		if err != nil {
			return "", "", false, invalidf("%q is not an IP address or network (e.g. 10.0.0.0/8 or 2001:db8::/32).", s)
		}
		ones, _ := n.Mask.Size()
		return n.IP.String(), strconv.Itoa(ones), addr.To4() == nil, nil
	}
	a := net.ParseIP(s)
	if a == nil {
		return "", "", false, invalidf("%q is not an IP address or network (e.g. 10.0.0.0/8 or 2001:db8::/32).", s)
	}
	return a.String(), "", a.To4() == nil, nil
}

func parsePorts(s string) (from, to int, err error) {
	a, b, isRange := strings.Cut(s, "-")
	from, err1 := strconv.Atoi(strings.TrimSpace(a))
	to = from
	var err2 error
	if isRange {
		to, err2 = strconv.Atoi(strings.TrimSpace(b))
	}
	if err1 != nil || err2 != nil || from < 1 || to > 65535 || to < from {
		return 0, 0, invalidf("%q is not a port or port range (e.g. 22 or 8000-8100).", s)
	}
	return from, to, nil
}

func field(s string) libvirtxml.NWFilterField { return libvirtxml.NWFilterField{Str: s} }

// buildFilter turns a firewall into nwfilter XML.
func buildFilter(name string, fw Firewall) (string, error) {
	f := libvirtxml.NWFilter{Name: name, Chain: "root"}
	if fw.AntiSpoof {
		f.Entries = append(f.Entries, libvirtxml.NWFilterEntry{Ref: &libvirtxml.NWFilterRef{Filter: "clean-traffic"}})
	}
	for i, r := range fw.Rules {
		if err := r.validate(); err != nil {
			return "", fmt.Errorf("rule %d: %w", i+1, err)
		}
		var ip libvirtxml.NWFilterRuleCommonIP
		v4, v6 := true, true
		if r.Remote != "" {
			addr, mask, is6, _ := parseRemote(r.Remote)
			v4, v6 = !is6, is6
			if r.Direction == "in" {
				ip.SrcIPAddr = field(addr)
				if mask != "" {
					ip.SrcIPMask = field(mask)
				}
			} else {
				ip.DstIPAddr = field(addr)
				if mask != "" {
					ip.DstIPMask = field(mask)
				}
			}
		}
		var port libvirtxml.NWFilterRuleCommonPort
		if r.Ports != "" {
			from, to, _ := parsePorts(r.Ports)
			port.DstPortStart = field(strconv.Itoa(from))
			if to != from {
				port.DstPortEnd = field(strconv.Itoa(to))
			}
		}
		if v4 {
			rule := &libvirtxml.NWFilterRule{Action: r.Action, Direction: r.Direction, Priority: rulePriority + i}
			switch r.Protocol {
			case "tcp":
				rule.TCP = &libvirtxml.NWFilterRuleTCP{NWFilterRuleCommonIP: ip, NWFilterRuleCommonPort: port, Comment: r.Comment}
			case "udp":
				rule.UDP = &libvirtxml.NWFilterRuleUDP{NWFilterRuleCommonIP: ip, NWFilterRuleCommonPort: port, Comment: r.Comment}
			case "icmp":
				rule.ICMP = &libvirtxml.NWFilterRuleICMP{NWFilterRuleCommonIP: ip, Comment: r.Comment}
			default:
				rule.All = &libvirtxml.NWFilterRuleAll{NWFilterRuleCommonIP: ip, Comment: r.Comment}
			}
			f.Entries = append(f.Entries, libvirtxml.NWFilterEntry{Rule: rule})
		}
		if v6 {
			rule := &libvirtxml.NWFilterRule{Action: r.Action, Direction: r.Direction, Priority: rulePriority + i}
			switch r.Protocol {
			case "tcp":
				rule.TCPIPv6 = &libvirtxml.NWFilterRuleTCPIPv6{NWFilterRuleCommonIP: ip, NWFilterRuleCommonPort: port, Comment: r.Comment}
			case "udp":
				rule.UDPIPv6 = &libvirtxml.NWFilterRuleUDPIPv6{NWFilterRuleCommonIP: ip, NWFilterRuleCommonPort: port, Comment: r.Comment}
			case "icmp":
				rule.ICMPv6 = &libvirtxml.NWFilterRuleICMPIPv6{NWFilterRuleCommonIP: ip, Comment: r.Comment}
			default:
				rule.AllIPv6 = &libvirtxml.NWFilterRuleAllIPv6{NWFilterRuleCommonIP: ip, Comment: r.Comment}
			}
			f.Entries = append(f.Entries, libvirtxml.NWFilterEntry{Rule: rule})
		}
	}
	for _, p := range []string{fw.DefaultIn, fw.DefaultOut} {
		if p != "" && p != "accept" && p != "drop" {
			return "", invalidf("The default policy must be accept or drop.")
		}
	}
	if fw.DefaultIn == "drop" || fw.DefaultOut == "drop" {
		f.Entries = append(f.Entries, ipv6Basics()...)
	}
	for _, d := range []struct{ dir, policy string }{{"in", fw.DefaultIn}, {"out", fw.DefaultOut}} {
		if d.policy != "drop" {
			continue
		}
		f.Entries = append(f.Entries,
			libvirtxml.NWFilterEntry{Rule: &libvirtxml.NWFilterRule{Action: "drop", Direction: d.dir, Priority: defaultPriority,
				All: &libvirtxml.NWFilterRuleAll{Comment: "VirtoMir default " + d.dir}}},
			libvirtxml.NWFilterEntry{Rule: &libvirtxml.NWFilterRule{Action: "drop", Direction: d.dir, Priority: defaultPriority,
				AllIPv6: &libvirtxml.NWFilterRuleAllIPv6{Comment: "VirtoMir default " + d.dir}}})
	}
	return f.Marshal()
}

// ipv6Basics keeps IPv6 working under a default drop: neighbor discovery (router and
// neighbor solicitations/advertisements, ICMPv6 133-136) both ways, and DHCPv6. Without
// state matching: conntrack does not track neighbor discovery, so a NEW/ESTABLISHED
// match never sees it (found in the live test).
func ipv6Basics() []libvirtxml.NWFilterEntry {
	var out []libvirtxml.NWFilterEntry
	for _, dir := range []string{"in", "out"} {
		for t := 133; t <= 136; t++ {
			out = append(out, libvirtxml.NWFilterEntry{Rule: &libvirtxml.NWFilterRule{Action: "accept", Direction: dir, Priority: basicsPriority, StateMatch: "false",
				ICMPv6: &libvirtxml.NWFilterRuleICMPIPv6{Type: field(strconv.Itoa(t)), Comment: "VirtoMir IPv6 neighbor discovery"}}})
		}
	}
	return append(out,
		libvirtxml.NWFilterEntry{Rule: &libvirtxml.NWFilterRule{Action: "accept", Direction: "out", Priority: basicsPriority, StateMatch: "false",
			UDPIPv6: &libvirtxml.NWFilterRuleUDPIPv6{NWFilterRuleCommonPort: libvirtxml.NWFilterRuleCommonPort{DstPortStart: field("547")}, Comment: "VirtoMir DHCPv6"}}},
		libvirtxml.NWFilterEntry{Rule: &libvirtxml.NWFilterRule{Action: "accept", Direction: "in", Priority: basicsPriority, StateMatch: "false",
			UDPIPv6: &libvirtxml.NWFilterRuleUDPIPv6{NWFilterRuleCommonPort: libvirtxml.NWFilterRuleCommonPort{SrcPortStart: field("547"), DstPortStart: field("546")}, Comment: "VirtoMir DHCPv6"}}})
}

// parseFilter reads back a filter written by buildFilter.
func parseFilter(doc string) (Firewall, error) {
	var f libvirtxml.NWFilter
	if err := f.Unmarshal(doc); err != nil {
		return Firewall{}, err
	}
	fw := Firewall{Enabled: true, DefaultIn: "accept", DefaultOut: "accept", Rules: []FirewallRule{}}
	// A rule for both families is an IPv4 + IPv6 pair with the same priority.
	pairOf := map[int]int{} // priority → index in fw.Rules of its address-less IPv4 rule
	for _, e := range f.Entries {
		if e.Ref != nil {
			fw.AntiSpoof = fw.AntiSpoof || e.Ref.Filter == "clean-traffic"
			continue
		}
		r := e.Rule
		if r == nil {
			continue
		}
		if r.Priority >= basicsPriority && r.Priority < defaultPriority {
			continue // ipv6Basics
		}
		if r.Priority >= defaultPriority && (r.All != nil || r.AllIPv6 != nil) && r.Action == "drop" {
			if r.Direction == "in" {
				fw.DefaultIn = "drop"
			} else {
				fw.DefaultOut = "drop"
			}
			continue
		}
		fr := FirewallRule{Direction: r.Direction, Action: r.Action}
		var ip libvirtxml.NWFilterRuleCommonIP
		var port *libvirtxml.NWFilterRuleCommonPort
		v6 := false
		switch {
		case r.TCPIPv6 != nil:
			fr.Protocol, ip, port, fr.Comment, v6 = "tcp", r.TCPIPv6.NWFilterRuleCommonIP, &r.TCPIPv6.NWFilterRuleCommonPort, r.TCPIPv6.Comment, true
		case r.UDPIPv6 != nil:
			fr.Protocol, ip, port, fr.Comment, v6 = "udp", r.UDPIPv6.NWFilterRuleCommonIP, &r.UDPIPv6.NWFilterRuleCommonPort, r.UDPIPv6.Comment, true
		case r.ICMPv6 != nil:
			fr.Protocol, ip, fr.Comment, v6 = "icmp", r.ICMPv6.NWFilterRuleCommonIP, r.ICMPv6.Comment, true
		case r.AllIPv6 != nil:
			fr.Protocol, ip, fr.Comment, v6 = "all", r.AllIPv6.NWFilterRuleCommonIP, r.AllIPv6.Comment, true
		case r.TCP != nil:
			fr.Protocol, ip, port, fr.Comment = "tcp", r.TCP.NWFilterRuleCommonIP, &r.TCP.NWFilterRuleCommonPort, r.TCP.Comment
		case r.UDP != nil:
			fr.Protocol, ip, port, fr.Comment = "udp", r.UDP.NWFilterRuleCommonIP, &r.UDP.NWFilterRuleCommonPort, r.UDP.Comment
		case r.ICMP != nil:
			fr.Protocol, ip, fr.Comment = "icmp", r.ICMP.NWFilterRuleCommonIP, r.ICMP.Comment
		case r.All != nil:
			fr.Protocol, ip, fr.Comment = "all", r.All.NWFilterRuleCommonIP, r.All.Comment
		default:
			continue // something added by hand that this page does not show
		}
		addr, mask := ip.SrcIPAddr.Str, ip.SrcIPMask.Str
		if fr.Direction == "out" {
			addr, mask = ip.DstIPAddr.Str, ip.DstIPMask.Str
		}
		if addr != "" {
			fr.Remote = addr
			if mask != "" {
				fr.Remote += "/" + mask
			}
		}
		if port != nil && port.DstPortStart.Str != "" {
			fr.Ports = port.DstPortStart.Str
			if port.DstPortEnd.Str != "" && port.DstPortEnd.Str != port.DstPortStart.Str {
				fr.Ports += "-" + port.DstPortEnd.Str
			}
		}
		if fr.Remote == "" {
			if j, ok := pairOf[r.Priority]; ok && v6 && fw.Rules[j] == fr {
				continue // the IPv6 half of a rule for both families
			}
			if !v6 {
				pairOf[r.Priority] = len(fw.Rules)
			}
		}
		fw.Rules = append(fw.Rules, fr)
	}
	return fw, nil
}

// filterable: libvirt applies nwfilters to tap-based adapters (virtual networks, bridges).
func filterable(n libvirtxml.DomainInterface) bool {
	return n.Source != nil && (n.Source.Network != nil || n.Source.Bridge != nil)
}

func ifaceName(n libvirtxml.DomainInterface) string {
	if n.MAC != nil {
		return n.MAC.Address
	}
	return "adapter"
}

// setFilterRefs points every filterable adapter at filter (or removes the reference with
// ""). It returns whether anything changed.
func setFilterRefs(x *libvirtxml.Domain, filter string) bool {
	changed := false
	if x.Devices == nil {
		return false
	}
	for i := range x.Devices.Interfaces {
		n := &x.Devices.Interfaces[i]
		if !filterable(*n) {
			continue
		}
		cur := ""
		if n.FilterRef != nil {
			cur = n.FilterRef.Filter
		}
		switch {
		case filter != "" && cur != filter:
			n.FilterRef = &libvirtxml.DomainInterfaceFilterRef{Filter: filter}
			changed = true
		case filter == "" && cur != "" && strings.HasPrefix(cur, "virtomir-"):
			n.FilterRef = nil
			changed = true
		}
	}
	return changed
}

// Firewall reads a machine's firewall.
func (c *Client) Firewall(uuid string) (Firewall, error) {
	l, d, err := c.lookup(uuid)
	if err != nil {
		return Firewall{}, err
	}
	fw := Firewall{DefaultIn: "accept", DefaultOut: "accept", Rules: []FirewallRule{}}
	if f, err := l.NwfilterLookupByName(FilterName(uuid)); err == nil {
		doc, err := l.NwfilterGetXMLDesc(f, 0)
		if err != nil {
			return fw, classify(err)
		}
		if fw, err = parseFilter(doc); err != nil {
			return fw, err
		}
	}
	x, err := c.parseDomain(l, d, true)
	if err != nil {
		return fw, err
	}
	used := false
	if x.Devices != nil {
		for _, n := range x.Devices.Interfaces {
			if !filterable(n) {
				fw.Unsupported = append(fw.Unsupported, ifaceName(n))
				continue
			}
			fw.Adapters = append(fw.Adapters, ifaceName(n))
			used = used || (n.FilterRef != nil && n.FilterRef.Filter == FilterName(uuid))
		}
	}
	fw.Enabled = fw.Enabled && used
	return fw, nil
}

// SetFirewall writes the filter and points the machine's adapters at it (or, disabled,
// removes the references and the filter). A running machine gets the change right away.
func (c *Client) SetFirewall(uuid string, fw Firewall) (FirewallResult, error) {
	var res FirewallResult
	name := FilterName(uuid)
	var doc string
	if fw.Enabled {
		var err error
		if doc, err = buildFilter(name, fw); err != nil {
			return res, err
		}
	}
	l, d, err := c.lookup(uuid)
	if err != nil {
		return res, err
	}
	sum, err := summary(l, d)
	if err != nil {
		return res, classify(err)
	}
	on := isOn(sum.State) || sum.State == StatePaused
	ref := ""
	if fw.Enabled {
		// Define (or redefine) the filter first: libvirt re-applies it to running adapters.
		// A redefinition must carry the filter's UUID, or libvirt refuses the same name.
		if f, err := l.NwfilterLookupByName(name); err == nil {
			var x libvirtxml.NWFilter
			if err := x.Unmarshal(doc); err == nil {
				x.UUID = formatUUID(f.UUID)
				if out, err := x.Marshal(); err == nil {
					doc = out
				}
			}
		}
		if _, err := l.NwfilterDefineXML(doc); err != nil {
			return res, fmt.Errorf("network filters: %w (is libvirt's nwfilter driver installed?)", classify(err))
		}
		ref = name
	}

	// Persistent definition.
	x, err := c.parseDomain(l, d, true)
	if err != nil {
		return res, err
	}
	if setFilterRefs(x, ref) {
		out, err := x.Marshal()
		if err != nil {
			return res, err
		}
		if _, err := l.DomainDefineXMLFlags(out, libvirt.DomainDefineValidate); err != nil {
			return res, classify(err)
		}
	}

	// Running machine: update each adapter in place.
	if on {
		live, err := c.parseDomain(l, d, false)
		if err == nil && live.Devices != nil {
			var errs []string
			for _, n := range live.Devices.Interfaces {
				if !filterable(n) {
					continue
				}
				one := libvirtxml.Domain{Devices: &libvirtxml.DomainDeviceList{Interfaces: []libvirtxml.DomainInterface{n}}}
				if !setFilterRefs(&one, ref) {
					continue
				}
				nd, err := one.Devices.Interfaces[0].Marshal()
				if err == nil {
					err = l.DomainUpdateDeviceFlags(d, nd, libvirt.DomainDeviceModifyLive)
				}
				if err != nil {
					errs = append(errs, ifaceName(n)+": "+err.Error())
				}
			}
			if len(errs) > 0 {
				res.LiveError = "applies at the next start: " + strings.Join(errs, "; ")
			}
		}
	}

	if !fw.Enabled {
		if f, err := l.NwfilterLookupByName(name); err == nil {
			if err := l.NwfilterUndefine(f); err != nil && res.LiveError == "" {
				// Still referenced by the running machine until it restarts; harmless.
				c.log.Debug("undefine nwfilter", "filter", name, "err", err)
			}
		}
	}
	c.log.Info("firewall", "domain", d.Name, "enabled", fw.Enabled, "rules", len(fw.Rules))
	return res, nil
}

// RemoveFirewall deletes a machine's filter after the machine itself was deleted.
func (c *Client) RemoveFirewall(uuid string) error {
	l, err := c.conn()
	if err != nil {
		return err
	}
	f, err := l.NwfilterLookupByName(FilterName(uuid))
	if err != nil {
		return nil
	}
	if err := l.NwfilterUndefine(f); err != nil {
		return errors.Join(errors.New("remove the machine's network filter"), classify(err))
	}
	return nil
}
