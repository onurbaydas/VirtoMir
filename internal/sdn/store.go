package sdn

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// Errors for the API.
var (
	ErrInvalid  = errors.New("invalid")
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("exists")
	ErrBusy     = errors.New("in use")
)

func invalidf(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, a...)...)
}

var (
	slug       = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$`)
	regexpName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
)

// state is everything the store keeps; it is also what members exchange.
type state struct {
	Version  int                        `json:"version"`
	VPCs     map[string]VPC             `json:"vpcs"`
	Subnets  map[string]Subnet          `json:"subnets"`
	Routers  map[string]Router          `json:"routers"` // by VPC ID
	Ports    map[string]Port            `json:"ports"`
	Gateways map[string]ExternalGateway `json:"gateways"` // by VPC ID
	FIPs     map[string]FloatingIP      `json:"floatingIps"`
	Groups   map[string]SecurityGroup   `json:"securityGroups"`
	LBs      map[string]LoadBalancer    `json:"loadBalancers"`
	IPsec    IPsecConfig                `json:"ipsec"`
}

func newState() state {
	return state{VPCs: map[string]VPC{}, Subnets: map[string]Subnet{}, Routers: map[string]Router{}, Ports: map[string]Port{},
		Gateways: map[string]ExternalGateway{}, FIPs: map[string]FloatingIP{}, Groups: map[string]SecurityGroup{}, LBs: map[string]LoadBalancer{},
		IPsec: IPsecConfig{Status: "disabled"}}
}

func (st *state) fill() {
	n := newState()
	if st.VPCs == nil {
		st.VPCs = n.VPCs
	}
	if st.Subnets == nil {
		st.Subnets = n.Subnets
	}
	if st.Routers == nil {
		st.Routers = n.Routers
	}
	if st.Ports == nil {
		st.Ports = n.Ports
	}
	if st.Gateways == nil {
		st.Gateways = n.Gateways
	}
	if st.FIPs == nil {
		st.FIPs = n.FIPs
	}
	if st.Groups == nil {
		st.Groups = n.Groups
	}
	if st.LBs == nil {
		st.LBs = n.LBs
	}
	if st.IPsec.Status == "" {
		st.IPsec.Status = "disabled"
	}
}

// Store keeps the SDN's intended state in -state-dir/sdn.json; OVN is reconciled to it.
type Store struct {
	path string
	mu   sync.RWMutex
	st   state
	now  func() time.Time
}

// Open loads path; a missing file is an empty store.
func Open(path string) (*Store, error) {
	s := &Store{path: path, st: newState(), now: time.Now}
	if path == "" {
		return s, nil
	}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("read SDN store: %w", err)
	}
	if err := json.Unmarshal(raw, &s.st); err != nil {
		return nil, fmt.Errorf("read SDN store: %w", err)
	}
	s.st.fill()
	return s, nil
}

func (s *Store) saveLocked() error {
	if s.path == "" {
		return nil
	}
	raw, err := json.MarshalIndent(s.st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// change runs fn on a copy of the state and keeps it as a new version if fn and the save
// succeed.
func (s *Store) change(fn func(st *state) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := json.Marshal(s.st)
	if err != nil {
		return err
	}
	var next state
	if err := json.Unmarshal(raw, &next); err != nil {
		return err
	}
	next.fill()
	if err := fn(&next); err != nil {
		return err
	}
	prev := s.st
	next.Version++
	s.st = next
	if err := s.saveLocked(); err != nil {
		s.st = prev
		return err
	}
	return nil
}

func sortedValues[T any](m map[string]T, key func(T) string) []T {
	out := make([]T, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b T) int { return strings.Compare(key(a), key(b)) })
	return out
}

// ---- VPCs ----------------------------------------------------------------------------------

func (v VPC) validate() error {
	switch {
	case !slug.MatchString(v.ID):
		return invalidf("a VPC ID is 3 to 40 lower-case letters, digits and dashes")
	case strings.TrimSpace(v.Name) == "":
		return invalidf("a VPC needs a name")
	case v.ProjectID == "":
		return invalidf("a VPC belongs to a project")
	}
	p, err := netip.ParsePrefix(v.IPv4CIDR)
	if err != nil || !p.Addr().Is4() || p.Masked() != p {
		return invalidf("%q is not an IPv4 network like 10.0.0.0/16", v.IPv4CIDR)
	}
	return nil
}

// ListVPCs lists the VPCs, of one project when projectID is set, by name.
func (s *Store) ListVPCs(projectID string) []VPC {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := sortedValues(s.st.VPCs, func(v VPC) string { return v.Name })
	if projectID != "" {
		out = slices.DeleteFunc(out, func(v VPC) bool { return v.ProjectID != projectID })
	}
	return out
}

// GetVPC returns one VPC.
func (s *Store) GetVPC(id string) (VPC, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.st.VPCs[id]
	return v, ok
}

// CreateVPC adds a VPC and its router.
func (s *Store) CreateVPC(v VPC) (VPC, error) {
	if err := v.validate(); err != nil {
		return VPC{}, err
	}
	err := s.change(func(st *state) error {
		if _, ok := st.VPCs[v.ID]; ok {
			return fmt.Errorf("%w: a VPC %s exists already", ErrExists, v.ID)
		}
		for _, o := range st.VPCs {
			if o.ProjectID == v.ProjectID && strings.EqualFold(o.Name, v.Name) {
				return fmt.Errorf("%w: project %s has a VPC named %s already", ErrExists, v.ProjectID, v.Name)
			}
		}
		now := s.now().UTC()
		v.Created, v.Updated = now, now
		st.VPCs[v.ID] = v
		st.Routers[v.ID] = Router{ID: "rtr-" + v.ID, VPCID: v.ID, Name: v.Name + " router", LogicalRouter: routerName(v.ID), Routes: []Route{}}
		return nil
	})
	return v, err
}

// UpdateVPC changes a VPC's name, description and default-deny; its CIDR may only grow
// over its subnets, and its project stays.
func (s *Store) UpdateVPC(v VPC) (VPC, error) {
	err := s.change(func(st *state) error {
		old, ok := st.VPCs[v.ID]
		if !ok {
			return fmt.Errorf("%w: no VPC %s", ErrNotFound, v.ID)
		}
		v.ProjectID, v.Created, v.Updated = old.ProjectID, old.Created, s.now().UTC()
		if err := v.validate(); err != nil {
			return err
		}
		outer := netip.MustParsePrefix(v.IPv4CIDR)
		for _, sub := range st.Subnets {
			if sub.VPCID == v.ID && !containsPrefix(outer, netip.MustParsePrefix(sub.CIDR)) {
				return invalidf("subnet %s (%s) would fall outside %s", sub.Name, sub.CIDR, v.IPv4CIDR)
			}
		}
		st.VPCs[v.ID] = v
		return nil
	})
	if err != nil {
		return VPC{}, err
	}
	return v, nil
}

// DeleteVPC removes an empty VPC (no subnets, floating IPs, groups or load balancers).
func (s *Store) DeleteVPC(id string) error {
	return s.change(func(st *state) error {
		if _, ok := st.VPCs[id]; !ok {
			return fmt.Errorf("%w: no VPC %s", ErrNotFound, id)
		}
		for _, sub := range st.Subnets {
			if sub.VPCID == id {
				return fmt.Errorf("%w: the VPC still has subnet %s", ErrBusy, sub.Name)
			}
		}
		for _, f := range st.FIPs {
			if f.VPCID == id {
				return fmt.Errorf("%w: the VPC still has floating IP %s", ErrBusy, f.ExternalIP)
			}
		}
		for _, g := range st.Groups {
			if g.VPCID == id {
				return fmt.Errorf("%w: the VPC still has security group %s", ErrBusy, g.Name)
			}
		}
		for _, lb := range st.LBs {
			if lb.VPCID == id {
				return fmt.Errorf("%w: the VPC still has load balancer %s", ErrBusy, lb.Name)
			}
		}
		delete(st.VPCs, id)
		delete(st.Routers, id)
		delete(st.Gateways, id)
		return nil
	})
}

// ---- Subnets -------------------------------------------------------------------------------

func containsPrefix(outer, inner netip.Prefix) bool {
	return outer.Bits() <= inner.Bits() && outer.Contains(inner.Addr())
}

func validateSubnet(st *state, sub Subnet) (Subnet, error) {
	if !slug.MatchString(sub.ID) {
		return sub, invalidf("a subnet ID is 3 to 40 lower-case letters, digits and dashes")
	}
	if strings.TrimSpace(sub.Name) == "" {
		return sub, invalidf("a subnet needs a name")
	}
	vpc, ok := st.VPCs[sub.VPCID]
	if !ok {
		return sub, fmt.Errorf("%w: no VPC %s", ErrNotFound, sub.VPCID)
	}
	p, err := netip.ParsePrefix(sub.CIDR)
	if err != nil || !p.Addr().Is4() || p.Masked() != p || p.Bits() > 29 {
		return sub, invalidf("%q is not an IPv4 network like 10.0.1.0/24 (at most /29)", sub.CIDR)
	}
	if !containsPrefix(netip.MustParsePrefix(vpc.IPv4CIDR), p) {
		return sub, invalidf("%s is not inside the VPC's %s", sub.CIDR, vpc.IPv4CIDR)
	}
	for _, o := range st.Subnets {
		if o.VPCID == sub.VPCID && o.ID != sub.ID && netip.MustParsePrefix(o.CIDR).Overlaps(p) {
			return sub, invalidf("%s overlaps subnet %s (%s)", sub.CIDR, o.Name, o.CIDR)
		}
	}
	if sub.GatewayIP == "" {
		sub.GatewayIP = p.Addr().Next().String()
	}
	gw, err := netip.ParseAddr(sub.GatewayIP)
	if err != nil || !p.Contains(gw) || gw == p.Addr() {
		return sub, invalidf("the gateway %q is not an address of %s", sub.GatewayIP, sub.CIDR)
	}
	for _, ip := range []string{sub.AllocationPoolStart, sub.AllocationPoolEnd} {
		if a, err := netip.ParseAddr(ip); ip != "" && (err != nil || !p.Contains(a)) {
			return sub, invalidf("the allocation pool address %q is not in %s", ip, sub.CIDR)
		}
	}
	for _, d := range sub.DNSServers {
		if _, err := netip.ParseAddr(d); err != nil {
			return sub, invalidf("%q is not a DNS server address", d)
		}
	}
	sub.LogicalSwitch = switchName(sub.ID)
	return sub, nil
}

// ListSubnets lists a VPC's subnets (all subnets when vpcID is "").
func (s *Store) ListSubnets(vpcID string) []Subnet {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := sortedValues(s.st.Subnets, func(v Subnet) string { return v.CIDR })
	if vpcID != "" {
		out = slices.DeleteFunc(out, func(v Subnet) bool { return v.VPCID != vpcID })
	}
	return out
}

// GetSubnet returns one subnet.
func (s *Store) GetSubnet(id string) (Subnet, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.st.Subnets[id]
	return v, ok
}

// CreateSubnet adds a subnet; IDs are unique in the whole SDN (they name OVN switches).
func (s *Store) CreateSubnet(sub Subnet) (Subnet, error) {
	err := s.change(func(st *state) error {
		if _, ok := st.Subnets[sub.ID]; ok {
			return fmt.Errorf("%w: a subnet %s exists already", ErrExists, sub.ID)
		}
		var err error
		if sub, err = validateSubnet(st, sub); err != nil {
			return err
		}
		st.Subnets[sub.ID] = sub
		return nil
	})
	return sub, err
}

// UpdateSubnet replaces a subnet (it stays in its VPC).
func (s *Store) UpdateSubnet(sub Subnet) (Subnet, error) {
	err := s.change(func(st *state) error {
		old, ok := st.Subnets[sub.ID]
		if !ok {
			return fmt.Errorf("%w: no subnet %s", ErrNotFound, sub.ID)
		}
		sub.VPCID = old.VPCID
		if sub.OVNDHCPID == "" {
			sub.OVNDHCPID = old.OVNDHCPID
		}
		var err error
		if sub, err = validateSubnet(st, sub); err != nil {
			return err
		}
		st.Subnets[sub.ID] = sub
		return nil
	})
	return sub, err
}

// DeleteSubnet removes a subnet no machine is plugged into.
func (s *Store) DeleteSubnet(id string) error {
	return s.change(func(st *state) error {
		if _, ok := st.Subnets[id]; !ok {
			return fmt.Errorf("%w: no subnet %s", ErrNotFound, id)
		}
		for _, p := range st.Ports {
			if p.SubnetID == id {
				return fmt.Errorf("%w: machine %s is plugged into the subnet", ErrBusy, p.VMUUID)
			}
		}
		delete(st.Subnets, id)
		return nil
	})
}

// GetRouter returns a VPC's router.
func (s *Store) GetRouter(vpcID string) (Router, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.st.Routers[vpcID]
	return r, ok
}

// UpdateRouter replaces a router's static routes.
func (s *Store) UpdateRouter(r Router) (Router, error) {
	err := s.change(func(st *state) error {
		old, ok := st.Routers[r.VPCID]
		if !ok {
			return fmt.Errorf("%w: no VPC %s", ErrNotFound, r.VPCID)
		}
		for _, rt := range r.Routes {
			if _, err := netip.ParsePrefix(rt.DestinationCIDR); err != nil {
				return invalidf("%q is not a destination network", rt.DestinationCIDR)
			}
			if _, err := netip.ParseAddr(rt.NextHopIP); err != nil {
				return invalidf("%q is not a next-hop address", rt.NextHopIP)
			}
		}
		old.Routes = slices.Clone(r.Routes)
		if old.Routes == nil {
			old.Routes = []Route{}
		}
		if r.Name != "" {
			old.Name = r.Name
		}
		st.Routers[r.VPCID] = old
		r = old
		return nil
	})
	return r, err
}

// ---- Exchange between members ----------------------------------------------------------------

// Shared is the whole state with its version.
type Shared struct {
	Raw     json.RawMessage `json:"state"`
	Version int             `json:"version"`
}

// Shared returns the state for other members.
func (s *Store) Shared() Shared {
	s.mu.RLock()
	defer s.mu.RUnlock()
	raw, _ := json.Marshal(s.st)
	return Shared{Raw: raw, Version: s.st.Version}
}

// Adopt takes another member's state if it is newer.
func (s *Store) Adopt(sh Shared) (bool, error) {
	var next state
	if err := json.Unmarshal(sh.Raw, &next); err != nil {
		return false, err
	}
	next.fill()
	s.mu.Lock()
	defer s.mu.Unlock()
	if next.Version <= s.st.Version {
		return false, nil
	}
	prev := s.st
	s.st = next
	if err := s.saveLocked(); err != nil {
		s.st = prev
		return false, err
	}
	return true, nil
}
