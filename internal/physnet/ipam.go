package physnet

import (
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"time"
)

// IP address pools: a range of a network that VirtoMir hands out as fixed addresses (set in
// the machine through cloud-init, registered in DNS), so addresses never collide across the
// cluster. Pools and their allocations are the same on every member.

// Allocation is an address a machine holds.
type Allocation struct {
	IP     string `json:"ip"`
	MAC    string `json:"mac,omitempty"`
	VMUUID string `json:"vmUuid,omitempty"`
	VMName string `json:"vmName,omitempty"`
	// Hostname is the machine's DNS name in the zone ("" = not registered).
	Hostname string    `json:"hostname,omitempty"`
	Host     string    `json:"host,omitempty"` // the member the machine was created on
	Created  time.Time `json:"created"`
}

// IPPool is a range of addresses of one network.
type IPPool struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Network     string   `json:"network,omitempty"` // the libvirt network machines join for it
	CIDR        string   `json:"cidr"`
	Gateway     string   `json:"gateway,omitempty"`
	RangeStart  string   `json:"rangeStart"`
	RangeEnd    string   `json:"rangeEnd"`
	DNSServers  []string `json:"dnsServers,omitempty"`
	// Domain is the DNS search domain the machines get.
	Domain      string       `json:"domain,omitempty"`
	Reserved    []string     `json:"reserved,omitempty"`
	Allocations []Allocation `json:"allocations"`
	Created     time.Time    `json:"created"`
	Updated     time.Time    `json:"updated"`
}

var (
	poolIDRE  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,38}[a-z0-9]$`)
	domainRE  = regexp.MustCompile(`^([A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)*[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.?$`)
	netNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
)

// Prefix is the pool's network.
func (p IPPool) Prefix() netip.Prefix { return netip.MustParsePrefix(p.CIDR) }

// validate checks a pool's addresses (allocations are the store's business).
func (p *IPPool) validate() error {
	if !poolIDRE.MatchString(p.ID) {
		return invalidf("the pool ID is 2 to 40 lower-case letters, digits and dashes")
	}
	if strings.TrimSpace(p.Name) == "" || len(p.Name) > 80 {
		return invalidf("the pool needs a name of at most 80 characters")
	}
	if p.Network != "" && !netNameRE.MatchString(p.Network) {
		return invalidf("%q is not a network name", p.Network)
	}
	pf, err := netip.ParsePrefix(p.CIDR)
	if err != nil || pf.Masked() != pf {
		return invalidf("the network is a CIDR like 10.20.0.0/24")
	}
	in := func(what, s string, required bool) (netip.Addr, error) {
		if s == "" && !required {
			return netip.Addr{}, nil
		}
		a, err := netip.ParseAddr(s)
		if err != nil || !pf.Contains(a) {
			return netip.Addr{}, invalidf("the %s %q is not an address in %s", what, s, p.CIDR)
		}
		return a, nil
	}
	start, err := in("first address", p.RangeStart, true)
	if err != nil {
		return err
	}
	end, err := in("last address", p.RangeEnd, true)
	if err != nil {
		return err
	}
	if end.Less(start) {
		return invalidf("the range ends before it starts")
	}
	if _, err := in("gateway", p.Gateway, false); err != nil {
		return err
	}
	for _, r := range p.Reserved {
		if _, err := in("reserved address", r, true); err != nil {
			return err
		}
	}
	for _, d := range p.DNSServers {
		if _, err := netip.ParseAddr(d); err != nil {
			return invalidf("the DNS server %q is not an IP address", d)
		}
	}
	if p.Domain != "" && !domainRE.MatchString(p.Domain) {
		return invalidf("%q is not a domain name", p.Domain)
	}
	return nil
}

// free reports why ip cannot be handed out to machine vm ("" = it can); vm may hold it
// already. With vm "" (a reservation, or just asking) every holder counts.
func (p IPPool) free(ip netip.Addr, vm string) string {
	start, end := netip.MustParseAddr(p.RangeStart), netip.MustParseAddr(p.RangeEnd)
	switch {
	case !p.Prefix().Contains(ip):
		return fmt.Sprintf("%s is not in %s", ip, p.CIDR)
	case ip.Less(start) || end.Less(ip):
		return fmt.Sprintf("%s is outside the pool's range %s – %s", ip, p.RangeStart, p.RangeEnd)
	case p.Gateway != "" && ip == netip.MustParseAddr(p.Gateway):
		return fmt.Sprintf("%s is the gateway", ip)
	case slices.Contains(p.Reserved, ip.String()):
		return fmt.Sprintf("%s is reserved", ip)
	}
	for _, a := range p.Allocations {
		if a.IP == ip.String() && (vm == "" || a.VMUUID != vm) {
			who := a.VMName
			if who == "" {
				who = "another machine"
			}
			return fmt.Sprintf("%s is held by %s", ip, who)
		}
	}
	return ""
}

// Check says whether ip is free in pool id ("" = yes).
func (s *Store) Check(id, ip string) (string, error) {
	p, ok := s.Pool(id)
	if !ok {
		return "", fmt.Errorf("%w: no IP pool %s", ErrNotFound, id)
	}
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return "", invalidf("%q is not an IP address", ip)
	}
	return p.free(a, ""), nil
}

// Allocate gives a machine an address of pool id: want if free, else the first free one.
// A machine that holds an address of the pool keeps it.
func (s *Store) Allocate(id string, a Allocation, want string) (Allocation, error) {
	var got Allocation
	err := s.change(func(st *state) error {
		i := slices.IndexFunc(st.Pools, func(p IPPool) bool { return p.ID == id })
		if i < 0 {
			return fmt.Errorf("%w: no IP pool %s", ErrNotFound, id)
		}
		p := &st.Pools[i]
		if a.VMUUID != "" {
			if j := slices.IndexFunc(p.Allocations, func(x Allocation) bool { return x.VMUUID == a.VMUUID }); j >= 0 && (want == "" || want == p.Allocations[j].IP) {
				got = p.Allocations[j]
				return errUnchanged
			}
		}
		var ip netip.Addr
		if want != "" {
			w, err := netip.ParseAddr(want)
			if err != nil {
				return invalidf("%q is not an IP address", want)
			}
			if why := p.free(w, a.VMUUID); why != "" {
				return fmt.Errorf("%w: %s", ErrConflict, why)
			}
			ip = w
		} else {
			end := netip.MustParseAddr(p.RangeEnd)
			for c := netip.MustParseAddr(p.RangeStart); !end.Less(c); c = c.Next() {
				if p.free(c, a.VMUUID) == "" {
					ip = c
					break
				}
			}
			if !ip.IsValid() {
				return fmt.Errorf("%w: pool %s has no free address left", ErrConflict, p.Name)
			}
		}
		a.IP, a.Created = ip.String(), s.now().UTC()
		p.Allocations = slices.DeleteFunc(p.Allocations, func(x Allocation) bool { return a.VMUUID != "" && x.VMUUID == a.VMUUID })
		p.Allocations = append(p.Allocations, a)
		slices.SortFunc(p.Allocations, func(x, y Allocation) int { return netip.MustParseAddr(x.IP).Compare(netip.MustParseAddr(y.IP)) })
		got = a
		return nil
	})
	return got, err
}

// Release frees an address of pool id.
func (s *Store) Release(id, ip string) error {
	return s.change(func(st *state) error {
		i := slices.IndexFunc(st.Pools, func(p IPPool) bool { return p.ID == id })
		if i < 0 {
			return fmt.Errorf("%w: no IP pool %s", ErrNotFound, id)
		}
		n := len(st.Pools[i].Allocations)
		st.Pools[i].Allocations = slices.DeleteFunc(st.Pools[i].Allocations, func(a Allocation) bool { return a.IP == ip })
		if len(st.Pools[i].Allocations) == n {
			return fmt.Errorf("%w: %s is not allocated in %s", ErrNotFound, ip, id)
		}
		return nil
	})
}

// Claim binds an address that was handed out before the machine existed to its UUID.
func (s *Store) Claim(id, ip, uuid string) error {
	return s.change(func(st *state) error {
		i := slices.IndexFunc(st.Pools, func(p IPPool) bool { return p.ID == id })
		if i < 0 {
			return fmt.Errorf("%w: no IP pool %s", ErrNotFound, id)
		}
		j := slices.IndexFunc(st.Pools[i].Allocations, func(a Allocation) bool { return a.IP == ip })
		if j < 0 {
			return fmt.Errorf("%w: %s is not allocated in %s", ErrNotFound, ip, id)
		}
		st.Pools[i].Allocations[j].VMUUID = uuid
		return nil
	})
}

// Released is an allocation that was given back, with its pool.
type Released struct {
	Pool string `json:"pool"`
	Allocation
}

// ReleaseVM frees every address a machine holds.
func (s *Store) ReleaseVM(uuid string) ([]Released, error) {
	var out []Released
	err := s.change(func(st *state) error {
		for i := range st.Pools {
			st.Pools[i].Allocations = slices.DeleteFunc(st.Pools[i].Allocations, func(a Allocation) bool {
				if a.VMUUID == uuid {
					out = append(out, Released{Pool: st.Pools[i].ID, Allocation: a})
					return true
				}
				return false
			})
		}
		if len(out) == 0 {
			return errUnchanged
		}
		return nil
	})
	return out, err
}
