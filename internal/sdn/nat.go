package sdn

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/netip"
	"strings"
)

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---- Store: gateways and floating IPs --------------------------------------------------------

func validateGateway(st *state, gw ExternalGateway) error {
	if _, ok := st.VPCs[gw.VPCID]; !ok {
		return fmt.Errorf("%w: no VPC %s", ErrNotFound, gw.VPCID)
	}
	if !gw.Enabled {
		return nil
	}
	p, err := netip.ParsePrefix(gw.ExternalIP)
	if err != nil || !p.Addr().Is4() {
		return invalidf("the external address is an IPv4 address with its prefix, like 203.0.113.10/24")
	}
	gip, err := netip.ParseAddr(gw.GatewayIP)
	if err != nil || !p.Masked().Contains(gip) {
		return invalidf("the upstream gateway %q is not on %s", gw.GatewayIP, p.Masked())
	}
	if !slug.MatchString(gw.ExternalNetwork) && !regexpName.MatchString(gw.ExternalNetwork) {
		return invalidf("the external network is the provider network's name (e.g. physnet1)")
	}
	if gw.GatewayChassis == "" {
		return invalidf("name the chassis (host) that carries the external traffic")
	}
	return nil
}

// SetExternalGateway connects a VPC to an external network (Enabled false disconnects it).
func (s *Store) SetExternalGateway(gw ExternalGateway) (ExternalGateway, error) {
	err := s.change(func(st *state) error {
		if err := validateGateway(st, gw); err != nil {
			return err
		}
		if gw.Enabled {
			st.Gateways[gw.VPCID] = gw
		} else {
			delete(st.Gateways, gw.VPCID)
		}
		return nil
	})
	return gw, err
}

// GetExternalGateway returns a VPC's gateway.
func (s *Store) GetExternalGateway(vpcID string) (ExternalGateway, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g, ok := s.st.Gateways[vpcID]
	return g, ok
}

// ListFloatingIPs lists floating IPs, of a project and/or VPC when given.
func (s *Store) ListFloatingIPs(projectID, vpcID string) []FloatingIP {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []FloatingIP
	for _, f := range sortedValues(s.st.FIPs, func(f FloatingIP) string { return f.ExternalIP }) {
		if (projectID == "" || f.ProjectID == projectID) && (vpcID == "" || f.VPCID == vpcID) {
			out = append(out, f)
		}
	}
	return out
}

// GetFloatingIP returns one floating IP.
func (s *Store) GetFloatingIP(id string) (FloatingIP, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	f, ok := s.st.FIPs[id]
	return f, ok
}

// CreateFloatingIP allocates an external address to a VPC (unassigned).
func (s *Store) CreateFloatingIP(f FloatingIP) (FloatingIP, error) {
	err := s.change(func(st *state) error {
		vpc, ok := st.VPCs[f.VPCID]
		if !ok {
			return fmt.Errorf("%w: no VPC %s", ErrNotFound, f.VPCID)
		}
		a, err := netip.ParseAddr(f.ExternalIP)
		if err != nil || !a.Is4() {
			return invalidf("%q is not an IPv4 address", f.ExternalIP)
		}
		for _, o := range st.FIPs {
			if o.ExternalIP == a.String() {
				return fmt.Errorf("%w: %s is a floating IP already", ErrExists, a)
			}
		}
		if f.ID == "" {
			f.ID = newID()
		}
		f.ExternalIP, f.ProjectID, f.InternalIP, f.PortID, f.Created = a.String(), vpc.ProjectID, "", "", s.now().UTC()
		st.FIPs[f.ID] = f
		return nil
	})
	return f, err
}

// AssignFloatingIP maps a floating IP to a private address of its VPC (and optionally the
// port that has it, which makes the NAT distributed).
func (s *Store) AssignFloatingIP(id, internalIP, portID string) (FloatingIP, error) {
	var f FloatingIP
	err := s.change(func(st *state) error {
		var ok bool
		if f, ok = st.FIPs[id]; !ok {
			return fmt.Errorf("%w: no floating IP %s", ErrNotFound, id)
		}
		a, err := netip.ParseAddr(internalIP)
		if err != nil {
			return invalidf("%q is not an address", internalIP)
		}
		inside := false
		for _, sub := range st.Subnets {
			inside = inside || (sub.VPCID == f.VPCID && netip.MustParsePrefix(sub.CIDR).Contains(a))
		}
		if !inside {
			return invalidf("%s is in no subnet of the VPC", a)
		}
		if portID != "" {
			if p, ok := st.Ports[portID]; !ok || p.IP != a.String() {
				return invalidf("port %s does not have %s", portID, a)
			}
		}
		for _, o := range st.FIPs {
			if o.ID != id && o.InternalIP == a.String() && o.VPCID == f.VPCID {
				return fmt.Errorf("%w: %s has floating IP %s already", ErrBusy, a, o.ExternalIP)
			}
		}
		f.InternalIP, f.PortID = a.String(), portID
		st.FIPs[id] = f
		return nil
	})
	return f, err
}

// UnassignFloatingIP takes a floating IP off its machine.
func (s *Store) UnassignFloatingIP(id string) (FloatingIP, error) {
	var f FloatingIP
	err := s.change(func(st *state) error {
		var ok bool
		if f, ok = st.FIPs[id]; !ok {
			return fmt.Errorf("%w: no floating IP %s", ErrNotFound, id)
		}
		f.InternalIP, f.PortID = "", ""
		st.FIPs[id] = f
		return nil
	})
	return f, err
}

// DeleteFloatingIP releases an unassigned floating IP.
func (s *Store) DeleteFloatingIP(id string) error {
	return s.change(func(st *state) error {
		f, ok := st.FIPs[id]
		if !ok {
			return fmt.Errorf("%w: no floating IP %s", ErrNotFound, id)
		}
		if f.InternalIP != "" {
			return fmt.Errorf("%w: %s is assigned to %s; unassign it first", ErrBusy, f.ExternalIP, f.InternalIP)
		}
		delete(st.FIPs, id)
		return nil
	})
}

// ---- OVN -------------------------------------------------------------------------------------

func extSwitch(vpcID string) string    { return "ext-" + vpcID }
func gwPort(vpcID string) string       { return "lrp-" + vpcID + "-gw" }
func gwSwitchPort(vpcID string) string { return "lsp-" + vpcID + "-gw" }
func localnetPort(vpcID string) string { return "ln-" + vpcID }

// ReconcileExternalGateway connects the VPC router to the provider network: an external
// switch with a localnet port, the router's gateway port bound to a chassis (OVN needs
// that for NAT), the default route and SNAT for every subnet.
func ReconcileExternalGateway(ctx context.Context, nb NB, gw ExternalGateway, subnets []Subnet) error {
	p := netip.MustParsePrefix(gw.ExternalIP)
	lr, ls := routerName(gw.VPCID), extSwitch(gw.VPCID)
	steps := [][]string{
		{"--may-exist", "ls-add", ls},
		{"--may-exist", "lsp-add", ls, localnetPort(gw.VPCID)},
		{"lsp-set-type", localnetPort(gw.VPCID), "localnet"},
		{"lsp-set-addresses", localnetPort(gw.VPCID), "unknown"},
		{"lsp-set-options", localnetPort(gw.VPCID), "network_name=" + gw.ExternalNetwork},
		{"--may-exist", "lrp-add", lr, gwPort(gw.VPCID), routerMAC(gw.VPCID + "-gw"), gw.ExternalIP},
		{"--may-exist", "lsp-add", ls, gwSwitchPort(gw.VPCID)},
		{"lsp-set-type", gwSwitchPort(gw.VPCID), "router"},
		{"lsp-set-addresses", gwSwitchPort(gw.VPCID), "router"},
		{"lsp-set-options", gwSwitchPort(gw.VPCID), "router-port=" + gwPort(gw.VPCID)},
		{"lrp-set-gateway-chassis", gwPort(gw.VPCID), gw.GatewayChassis, "20"},
		{"--may-exist", "lr-route-add", lr, "0.0.0.0/0", gw.GatewayIP},
	}
	for _, sub := range subnets {
		steps = append(steps, []string{"--may-exist", "lr-nat-add", lr, "snat", p.Addr().String(), sub.CIDR})
	}
	for _, s := range steps {
		if _, err := nb.Run(ctx, s...); err != nil {
			return err
		}
	}
	return nil
}

// RemoveExternalGateway disconnects the VPC from the provider network.
func RemoveExternalGateway(ctx context.Context, nb NB, vpcID string) error {
	lr := routerName(vpcID)
	steps := [][]string{
		{"--if-exists", "lr-route-del", lr, "0.0.0.0/0"},
		{"--if-exists", "lr-nat-del", lr, "snat"},
		{"--if-exists", "lrp-del", gwPort(vpcID)},
		{"--if-exists", "ls-del", extSwitch(vpcID)},
	}
	for _, s := range steps {
		if _, err := nb.Run(ctx, s...); err != nil {
			return err
		}
	}
	return nil
}

// ReconcileFloatingIP maps (or, unassigned, unmaps) a floating IP. With a port and its MAC
// the NAT happens on the machine's own host instead of the gateway chassis.
func ReconcileFloatingIP(ctx context.Context, nb NB, f FloatingIP, portMAC string) error {
	lr := routerName(f.VPCID)
	if _, err := nb.Run(ctx, "--if-exists", "lr-nat-del", lr, "dnat_and_snat", f.ExternalIP); err != nil {
		return err
	}
	if f.InternalIP == "" {
		return nil
	}
	args := []string{"lr-nat-add", lr, "dnat_and_snat", f.ExternalIP, f.InternalIP}
	if f.PortID != "" && portMAC != "" {
		args = append(args, f.PortID, strings.ToLower(portMAC))
	}
	_, err := nb.Run(ctx, args...)
	return err
}
