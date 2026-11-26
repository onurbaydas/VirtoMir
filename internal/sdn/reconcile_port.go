package sdn

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// ReconcilePort makes a stored machine port exist in OVN again, with its stored MAC and
// address (never a new one from OVN's IPAM), port security and DHCP. Its security groups
// list it already; reconciling them afterwards puts it back into their port groups.
func ReconcilePort(ctx context.Context, nb NB, p Port, sub Subnet) error {
	if p.IP == "" || p.MAC == "" {
		return fmt.Errorf("port %s has no stored address; plug the machine in again", p.ID)
	}
	steps := [][]string{
		{"--may-exist", "lsp-add", switchName(p.SubnetID), p.ID},
		{"lsp-set-addresses", p.ID, p.MAC + " " + p.IP},
		{"set", "Logical_Switch_Port", p.ID, "external_ids:virtomir-vm=" + p.VMUUID, "external_ids:virtomir-subnet=" + p.SubnetID},
		{"lsp-set-port-security", p.ID, p.MAC + " " + p.IP},
	}
	if sub.DHCPEnabled && sub.OVNDHCPID != "" {
		steps = append(steps, []string{"lsp-set-dhcpv4-options", p.ID, sub.OVNDHCPID})
	}
	for _, st := range steps {
		if _, err := nb.Run(ctx, st...); err != nil {
			return err
		}
	}
	return nil
}

// Orphan is an object in OVN that is named like one of VirtoMir's but that VirtoMir no
// longer has: left by a failed delete, or by hand.
type Orphan struct {
	Table string `json:"table"`
	Name  string `json:"name"`
}

// orphanKinds are the OVN tables VirtoMir names objects in, the prefix it uses, and how to
// tell whether the rest of a name is still in the store.
func (s *Store) orphanKinds() []struct {
	table, prefix string
	known         func(id string) bool
} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	vpcs, subnets, ports, groups, lbs := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for id := range s.st.VPCs {
		vpcs[id] = true
	}
	for id := range s.st.Subnets {
		subnets[id] = true
	}
	for id := range s.st.Ports {
		ports[id] = true
	}
	for id := range s.st.Groups {
		groups[portGroup(id)] = true
	}
	for id := range s.st.LBs {
		lbs[lbName(id)] = true
	}
	return []struct {
		table, prefix string
		known         func(id string) bool
	}{
		{"Logical_Router", "lr-", func(n string) bool { return vpcs[strings.TrimPrefix(n, "lr-")] }},
		{"Logical_Switch", "ls-", func(n string) bool { return subnets[strings.TrimPrefix(n, "ls-")] }},
		{"Logical_Switch_Port", "vm-port-", func(n string) bool { return ports[n] }},
		{"Port_Group", "pg_", func(n string) bool { return groups[n] }},
		{"Load_Balancer", "lb_", func(n string) bool { return lbs[n] }},
	}
}

// FindOrphans lists OVN objects named like VirtoMir's that the store does not have. It only
// reports them; nothing is deleted.
func FindOrphans(ctx context.Context, nb NB, s *Store) ([]Orphan, error) {
	out := []Orphan{}
	for _, k := range s.orphanKinds() {
		raw, err := nb.Run(ctx, "--bare", "--columns=name", "list", k.table)
		if err != nil {
			return out, err
		}
		var names []string
		for _, line := range strings.Split(string(raw), "\n") {
			n := strings.Trim(strings.TrimSpace(line), `"`)
			if strings.HasPrefix(n, k.prefix) && !k.known(n) {
				names = append(names, n)
			}
		}
		slices.Sort(names)
		for _, n := range slices.Compact(names) {
			out = append(out, Orphan{Table: k.table, Name: n})
		}
	}
	return out, nil
}
