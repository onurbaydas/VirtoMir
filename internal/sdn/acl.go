package sdn

import (
	"context"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strings"
)

// Security groups are OVN port groups with ACLs. OVN identifiers in match expressions
// cannot contain dashes, so a group's port group is pg_<id> (IDs are hex), and the
// addresses of a remote group are the address set OVN keeps for it, $pg_<id>_ip4.

var groupID = regexp.MustCompile(`^[0-9a-f]{16}$`)

func portGroup(sgID string) string { return "pg_" + sgID }

// ---- Store -----------------------------------------------------------------------------------

func validateRule(st *state, r SecurityRule) (SecurityRule, error) {
	if r.Direction != "ingress" && r.Direction != "egress" {
		return r, invalidf("the direction is ingress or egress")
	}
	switch r.Action {
	case "":
		r.Action = "allow-related"
	case "allow", "allow-related", "drop", "reject":
	default:
		return r, invalidf("the action is allow, allow-related, drop or reject")
	}
	if r.Priority == 0 {
		r.Priority = 1000
	}
	if r.Priority < 2 || r.Priority > 32767 {
		return r, invalidf("the priority is 2 to 32767 (1 is the group's default deny)")
	}
	switch r.Protocol {
	case "":
		r.Protocol = "any"
	case "tcp", "udp":
		if r.PortMin != 0 || r.PortMax != 0 {
			if r.PortMax == 0 {
				r.PortMax = r.PortMin
			}
			if r.PortMin < 1 || r.PortMin > r.PortMax || r.PortMax > 65535 {
				return r, invalidf("ports are 1 to 65535, the first not above the last")
			}
		}
	case "icmp", "any":
		if r.PortMin != 0 || r.PortMax != 0 {
			return r, invalidf("%s has no ports", r.Protocol)
		}
	default:
		return r, invalidf("the protocol is tcp, udp, icmp or any")
	}
	if r.RemoteCIDR != "" && r.RemoteGroupID != "" {
		return r, invalidf("a rule names a remote network or a remote group, not both")
	}
	if r.RemoteCIDR != "" {
		p, err := netip.ParsePrefix(r.RemoteCIDR)
		if err != nil || !p.Addr().Is4() {
			return r, invalidf("%q is not an IPv4 network", r.RemoteCIDR)
		}
		r.RemoteCIDR = p.Masked().String()
	}
	if r.RemoteGroupID != "" {
		if _, ok := st.Groups[r.RemoteGroupID]; !ok {
			return r, fmt.Errorf("%w: no security group %s", ErrNotFound, r.RemoteGroupID)
		}
	}
	if r.ID == "" {
		r.ID = newID()
	}
	return r, nil
}

// ListSecurityGroups lists groups, of a project and/or VPC when given.
func (s *Store) ListSecurityGroups(projectID, vpcID string) []SecurityGroup {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []SecurityGroup
	for _, g := range sortedValues(s.st.Groups, func(g SecurityGroup) string { return g.Name }) {
		if (projectID == "" || g.ProjectID == projectID) && (vpcID == "" || g.VPCID == vpcID) {
			out = append(out, g)
		}
	}
	return out
}

// GetSecurityGroup returns one group.
func (s *Store) GetSecurityGroup(id string) (SecurityGroup, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g, ok := s.st.Groups[id]
	return g, ok
}

// CreateSecurityGroup adds a group; it starts with one rule letting its machines out.
func (s *Store) CreateSecurityGroup(g SecurityGroup) (SecurityGroup, error) {
	err := s.change(func(st *state) error {
		vpc, ok := st.VPCs[g.VPCID]
		if !ok {
			return fmt.Errorf("%w: no VPC %s", ErrNotFound, g.VPCID)
		}
		if strings.TrimSpace(g.Name) == "" {
			return invalidf("a security group needs a name")
		}
		if g.ID == "" {
			g.ID = newID()
		}
		if !groupID.MatchString(g.ID) {
			return invalidf("a security group ID is 16 hex digits")
		}
		if _, ok := st.Groups[g.ID]; ok {
			return fmt.Errorf("%w: a security group %s exists already", ErrExists, g.ID)
		}
		g.ProjectID = vpc.ProjectID
		rules := g.Rules
		if len(rules) == 0 {
			rules = []SecurityRule{{Direction: "egress", Action: "allow-related", Priority: 100, Protocol: "any", Description: "machines may connect out"}}
		}
		g.Rules = nil
		st.Groups[g.ID] = g // so rules may name the group itself as remote
		for _, r := range rules {
			r, err := validateRule(st, r)
			if err != nil {
				return err
			}
			g.Rules = append(g.Rules, r)
		}
		if g.MemberPorts == nil {
			g.MemberPorts = []string{}
		}
		now := s.now().UTC()
		g.Created, g.Updated = now, now
		st.Groups[g.ID] = g
		return nil
	})
	return g, err
}

// UpdateSecurityGroup changes a group's name, description and default deny.
func (s *Store) UpdateSecurityGroup(g SecurityGroup) (SecurityGroup, error) {
	err := s.change(func(st *state) error {
		old, ok := st.Groups[g.ID]
		if !ok {
			return fmt.Errorf("%w: no security group %s", ErrNotFound, g.ID)
		}
		if strings.TrimSpace(g.Name) == "" {
			return invalidf("a security group needs a name")
		}
		old.Name, old.Description, old.DefaultDeny, old.Updated = g.Name, g.Description, g.DefaultDeny, s.now().UTC()
		st.Groups[g.ID] = old
		g = old
		return nil
	})
	return g, err
}

// DeleteSecurityGroup removes a group no port is in and no rule names.
func (s *Store) DeleteSecurityGroup(id string) error {
	return s.change(func(st *state) error {
		g, ok := st.Groups[id]
		if !ok {
			return fmt.Errorf("%w: no security group %s", ErrNotFound, id)
		}
		if len(g.MemberPorts) > 0 {
			return fmt.Errorf("%w: %d ports are in the group", ErrBusy, len(g.MemberPorts))
		}
		for _, o := range st.Groups {
			for _, r := range o.Rules {
				if o.ID != id && r.RemoteGroupID == id {
					return fmt.Errorf("%w: a rule of group %s names this group", ErrBusy, o.Name)
				}
			}
		}
		delete(st.Groups, id)
		return nil
	})
}

// AddSecurityRule adds a rule to a group.
func (s *Store) AddSecurityRule(sgID string, r SecurityRule) (SecurityRule, error) {
	err := s.change(func(st *state) error {
		g, ok := st.Groups[sgID]
		if !ok {
			return fmt.Errorf("%w: no security group %s", ErrNotFound, sgID)
		}
		var err error
		if r, err = validateRule(st, r); err != nil {
			return err
		}
		g.Rules, g.Updated = append(g.Rules, r), s.now().UTC()
		st.Groups[sgID] = g
		return nil
	})
	return r, err
}

// DeleteSecurityRule removes a rule from a group.
func (s *Store) DeleteSecurityRule(sgID, ruleID string) error {
	return s.change(func(st *state) error {
		g, ok := st.Groups[sgID]
		if !ok {
			return fmt.Errorf("%w: no security group %s", ErrNotFound, sgID)
		}
		i := slices.IndexFunc(g.Rules, func(r SecurityRule) bool { return r.ID == ruleID })
		if i < 0 {
			return fmt.Errorf("%w: no rule %s", ErrNotFound, ruleID)
		}
		g.Rules, g.Updated = slices.Delete(g.Rules, i, i+1), s.now().UTC()
		st.Groups[sgID] = g
		return nil
	})
}

// BindPortToSecurityGroup puts a port into a group.
func (s *Store) BindPortToSecurityGroup(sgID, portID string) error {
	return s.change(func(st *state) error {
		g, ok := st.Groups[sgID]
		if !ok {
			return fmt.Errorf("%w: no security group %s", ErrNotFound, sgID)
		}
		if !slices.Contains(g.MemberPorts, portID) {
			g.MemberPorts = append(g.MemberPorts, portID)
			slices.Sort(g.MemberPorts)
		}
		if p, ok := st.Ports[portID]; ok && !slices.Contains(p.SecurityGroups, sgID) {
			p.SecurityGroups = append(p.SecurityGroups, sgID)
			st.Ports[portID] = p
		}
		st.Groups[sgID] = g
		return nil
	})
}

// UnbindPortFromSecurityGroup takes a port out of a group.
func (s *Store) UnbindPortFromSecurityGroup(sgID, portID string) error {
	return s.change(func(st *state) error {
		g, ok := st.Groups[sgID]
		if !ok {
			return fmt.Errorf("%w: no security group %s", ErrNotFound, sgID)
		}
		g.MemberPorts = slices.DeleteFunc(g.MemberPorts, func(p string) bool { return p == portID })
		if p, ok := st.Ports[portID]; ok {
			p.SecurityGroups = slices.DeleteFunc(p.SecurityGroups, func(x string) bool { return x == sgID })
			st.Ports[portID] = p
		}
		st.Groups[sgID] = g
		return nil
	})
}

// ---- OVN -------------------------------------------------------------------------------------

// BuildMatchExpression is the OVN match of a rule on port group pg.
func BuildMatchExpression(r SecurityRule, pg string) string {
	var b strings.Builder
	remote := "ip4.src"
	if r.Direction == "ingress" {
		b.WriteString("outport == @" + pg)
	} else {
		b.WriteString("inport == @" + pg)
		remote = "ip4.dst"
	}
	switch r.Protocol {
	case "tcp", "udp":
		b.WriteString(" && ip4 && " + r.Protocol)
		switch {
		case r.PortMin > 0 && r.PortMin == r.PortMax:
			fmt.Fprintf(&b, " && %s.dst == %d", r.Protocol, r.PortMin)
		case r.PortMin > 0:
			fmt.Fprintf(&b, " && %s.dst >= %d && %s.dst <= %d", r.Protocol, r.PortMin, r.Protocol, r.PortMax)
		}
	case "icmp":
		b.WriteString(" && icmp4")
	default:
		b.WriteString(" && ip4")
	}
	switch {
	case r.RemoteCIDR != "":
		b.WriteString(" && " + remote + " == " + r.RemoteCIDR)
	case r.RemoteGroupID != "":
		b.WriteString(" && " + remote + " == $" + portGroup(r.RemoteGroupID) + "_ip4")
	}
	return b.String()
}

func ovnDirection(dir string) string {
	if dir == "ingress" {
		return "to-lport"
	}
	return "from-lport"
}

// ReconcileSecurityGroup makes the group's port group hold its ports and only its ACLs:
// the rules, and with default deny a lowest-priority drop each way.
func ReconcileSecurityGroup(ctx context.Context, nb NB, sg SecurityGroup) error {
	pg := portGroup(sg.ID)
	if _, err := nb.Run(ctx, "--may-exist", "pg-add", pg); err != nil {
		return err
	}
	ports := []string{"clear", "Port_Group", pg, "ports"}
	if len(sg.MemberPorts) > 0 {
		ports = append([]string{"pg-set-ports", pg}, sg.MemberPorts...)
	}
	steps := [][]string{
		ports,
		{"set", "Port_Group", pg, "external_ids:virtomir-group=" + sg.ID, "external_ids:virtomir-project=" + sg.ProjectID},
		{"--type=port-group", "acl-del", pg},
	}
	if sg.DefaultDeny {
		steps = append(steps,
			[]string{"--type=port-group", "acl-add", pg, "to-lport", "1", "outport == @" + pg + " && ip4", "drop"},
			[]string{"--type=port-group", "acl-add", pg, "from-lport", "1", "inport == @" + pg + " && ip4", "drop"})
	}
	for _, r := range sg.Rules {
		steps = append(steps, []string{"--type=port-group", "acl-add", pg, ovnDirection(r.Direction), fmt.Sprint(r.Priority), BuildMatchExpression(r, pg), r.Action})
	}
	for _, s := range steps {
		if _, err := nb.Run(ctx, s...); err != nil {
			return err
		}
	}
	return nil
}

// DeleteSecurityGroupResources removes the group's port group; its ACLs belong to it and
// go with it.
func DeleteSecurityGroupResources(ctx context.Context, nb NB, sgID string) error {
	_, err := nb.Run(ctx, "--if-exists", "destroy", "Port_Group", portGroup(sgID))
	return err
}
