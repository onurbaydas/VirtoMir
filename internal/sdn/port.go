package sdn

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// PortID names a machine's port: it is also the iface-id OVS hands to ovn-controller.
func PortID(vmUUID, mac string) string {
	u := strings.ReplaceAll(strings.ToLower(vmUUID), "-", "")
	if len(u) > 8 {
		u = u[:8]
	}
	return "vm-port-" + u + "-" + strings.ReplaceAll(strings.ToLower(mac), ":", "")
}

// PortsOf lists a machine's ports.
func (s *Store) PortsOf(vmUUID string) []Port {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Port
	for _, p := range sortedValues(s.st.Ports, func(p Port) string { return p.ID }) {
		if strings.EqualFold(p.VMUUID, vmUUID) {
			out = append(out, p)
		}
	}
	return out
}

// GetPort returns one port.
func (s *Store) GetPort(id string) (Port, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.st.Ports[id]
	return p, ok
}

// PortWithIP finds the port that has ip (for load balancer health checks).
func (s *Store) PortWithIP(ip string) (Port, Subnet, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.st.Ports {
		if p.IP == ip {
			return p, s.st.Subnets[p.SubnetID], true
		}
	}
	return Port{}, Subnet{}, false
}

// AllocatePort makes a machine's adapter a port of a subnet: the logical switch port with
// its MAC and address (a requested one, or one OVN hands out), port security so the
// machine cannot claim another address, DHCP, and its security groups. It returns the
// port and the groups whose port groups need reconciling.
func AllocatePort(ctx context.Context, nb NB, s *Store, subnetID, vmUUID, mac, requestedIP string, groups []string) (Port, error) {
	sub, ok := s.GetSubnet(subnetID)
	if !ok {
		return Port{}, fmt.Errorf("%w: no subnet %s", ErrNotFound, subnetID)
	}
	if requestedIP != "" {
		a, err := netip.ParseAddr(requestedIP)
		if err != nil || !netip.MustParsePrefix(sub.CIDR).Contains(a) || a.String() == sub.GatewayIP {
			return Port{}, invalidf("%q is not a free address of %s", requestedIP, sub.CIDR)
		}
		if p, _, taken := s.PortWithIP(a.String()); taken {
			return Port{}, fmt.Errorf("%w: %s belongs to machine %s", ErrBusy, a, p.VMUUID)
		}
	}
	for _, g := range groups {
		sg, ok := s.GetSecurityGroup(g)
		if !ok || sg.VPCID != sub.VPCID {
			return Port{}, invalidf("security group %s is not in the subnet's VPC", g)
		}
	}
	mac = strings.ToLower(mac)
	id := PortID(vmUUID, mac)
	ls := switchName(subnetID)
	addr := mac + " dynamic"
	if requestedIP != "" {
		addr = mac + " " + requestedIP
	}
	steps := [][]string{
		{"--may-exist", "lsp-add", ls, id},
		{"lsp-set-addresses", id, addr},
		{"set", "Logical_Switch_Port", id, "external_ids:virtomir-vm=" + vmUUID, "external_ids:virtomir-subnet=" + subnetID},
	}
	for _, st := range steps {
		if _, err := nb.Run(ctx, st...); err != nil {
			return Port{}, err
		}
	}
	ip := requestedIP
	if ip == "" { // OVN's IPAM picked one: "MAC IP"
		out, err := nb.Run(ctx, "get", "Logical_Switch_Port", id, "dynamic_addresses")
		if err != nil {
			return Port{}, err
		}
		f := strings.Fields(strings.Trim(strings.TrimSpace(string(out)), `"`))
		if len(f) < 2 {
			return Port{}, fmt.Errorf("OVN gave port %s no address", id)
		}
		ip = f[1]
	}
	if _, err := nb.Run(ctx, "lsp-set-port-security", id, mac+" "+ip); err != nil {
		return Port{}, err
	}
	if sub.DHCPEnabled && sub.OVNDHCPID != "" {
		if _, err := nb.Run(ctx, "lsp-set-dhcpv4-options", id, sub.OVNDHCPID); err != nil {
			return Port{}, err
		}
	}
	p := Port{ID: id, SubnetID: subnetID, VMUUID: vmUUID, MAC: mac, IP: ip, SecurityGroups: slices.Clone(groups)}
	err := s.change(func(st *state) error {
		st.Ports[id] = p
		for _, g := range groups {
			sg := st.Groups[g]
			if !slices.Contains(sg.MemberPorts, id) {
				sg.MemberPorts = append(sg.MemberPorts, id)
				slices.Sort(sg.MemberPorts)
			}
			st.Groups[g] = sg
		}
		return nil
	})
	if err != nil {
		return Port{}, err
	}
	for _, g := range groups {
		if sg, ok := s.GetSecurityGroup(g); ok {
			if err := ReconcileSecurityGroup(ctx, nb, sg); err != nil {
				return p, err
			}
		}
	}
	return p, nil
}

// DeallocatePort removes a machine's port, takes it out of its groups and releases a
// floating IP pointing at it.
func DeallocatePort(ctx context.Context, nb NB, s *Store, portID string) error {
	p, ok := s.GetPort(portID)
	if !ok {
		return nil
	}
	var fips []FloatingIP
	err := s.change(func(st *state) error {
		delete(st.Ports, portID)
		for id, sg := range st.Groups {
			if slices.Contains(sg.MemberPorts, portID) {
				sg.MemberPorts = slices.DeleteFunc(sg.MemberPorts, func(x string) bool { return x == portID })
				st.Groups[id] = sg
			}
		}
		for id, f := range st.FIPs {
			if f.PortID == portID || (f.InternalIP == p.IP && st.Subnets[p.SubnetID].VPCID == f.VPCID) {
				f.InternalIP, f.PortID = "", ""
				st.FIPs[id] = f
				fips = append(fips, f)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, f := range fips {
		if err := ReconcileFloatingIP(ctx, nb, f, ""); err != nil {
			return err
		}
	}
	for _, g := range p.SecurityGroups {
		if sg, ok := s.GetSecurityGroup(g); ok {
			if err := ReconcileSecurityGroup(ctx, nb, sg); err != nil {
				return err
			}
		}
	}
	_, err = nb.Run(ctx, "--if-exists", "lsp-del", portID)
	return err
}
