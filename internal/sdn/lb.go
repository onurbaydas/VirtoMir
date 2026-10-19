package sdn

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

func lbName(id string) string { return "lb_" + id }

// ---- Store -----------------------------------------------------------------------------------

func validateLB(st *state, lb LoadBalancer) (LoadBalancer, error) {
	vpc, ok := st.VPCs[lb.VPCID]
	if !ok {
		return lb, fmt.Errorf("%w: no VPC %s", ErrNotFound, lb.VPCID)
	}
	lb.ProjectID = vpc.ProjectID
	if strings.TrimSpace(lb.Name) == "" {
		return lb, invalidf("a load balancer needs a name")
	}
	if a, err := netip.ParseAddr(lb.VIP); err != nil || !a.Is4() {
		return lb, invalidf("the virtual IP %q is not an IPv4 address", lb.VIP)
	}
	if lb.Port < 1 || lb.Port > 65535 {
		return lb, invalidf("the port is 1 to 65535")
	}
	if lb.Protocol == "" {
		lb.Protocol = "tcp"
	}
	if lb.Protocol != "tcp" && lb.Protocol != "udp" {
		return lb, invalidf("the protocol is tcp or udp")
	}
	if len(lb.Members) == 0 {
		return lb, invalidf("a load balancer needs members (ip:port)")
	}
	for i, m := range lb.Members {
		ap, err := netip.ParseAddrPort(m)
		if err != nil || !ap.Addr().Is4() || ap.Port() == 0 {
			return lb, invalidf("member %q is not ip:port", m)
		}
		lb.Members[i] = ap.String()
	}
	for _, sw := range lb.AttachedSwitches {
		if !slices.ContainsFunc(sortedValues(st.Subnets, func(s Subnet) string { return s.ID }), func(s Subnet) bool { return s.VPCID == lb.VPCID && s.LogicalSwitch == sw }) {
			return lb, invalidf("%s is not a subnet switch of the VPC", sw)
		}
	}
	for _, r := range lb.AttachedRouters {
		if r != routerName(lb.VPCID) {
			return lb, invalidf("%s is not the VPC's router", r)
		}
	}
	if hc := lb.HealthCheck; hc != nil {
		if hc.Protocol == "http" {
			return lb, invalidf("OVN checks members with TCP or UDP only, not HTTP")
		}
		if hc.Protocol == "" {
			hc.Protocol = lb.Protocol
		}
		if hc.IntervalSeconds <= 0 {
			hc.IntervalSeconds = 5
		}
		if hc.TimeoutSeconds <= 0 {
			hc.TimeoutSeconds = 2
		}
		if hc.Fall <= 0 {
			hc.Fall = 3
		}
		if hc.Rise <= 0 {
			hc.Rise = 2
		}
		if hc.SourceIP != "" {
			if _, err := netip.ParseAddr(hc.SourceIP); err != nil {
				return lb, invalidf("the health check source %q is not an address", hc.SourceIP)
			}
		}
	}
	// Without attachments OVN never sees the VIP: by default the load balancer serves the
	// whole VPC, on every subnet's switch and its router.
	if len(lb.AttachedSwitches) == 0 && len(lb.AttachedRouters) == 0 {
		lb.AttachedSwitches = []string{}
		for _, s := range sortedValues(st.Subnets, func(s Subnet) string { return s.ID }) {
			if s.VPCID == lb.VPCID {
				lb.AttachedSwitches = append(lb.AttachedSwitches, s.LogicalSwitch)
			}
		}
		lb.AttachedRouters = []string{routerName(lb.VPCID)}
	}
	if lb.AttachedSwitches == nil {
		lb.AttachedSwitches = []string{}
	}
	if lb.AttachedRouters == nil {
		lb.AttachedRouters = []string{}
	}
	if lb.Status == "" {
		lb.Status = "active"
	}
	return lb, nil
}

// ListLoadBalancers lists load balancers, of a project and/or VPC when given.
func (s *Store) ListLoadBalancers(projectID, vpcID string) []LoadBalancer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []LoadBalancer
	for _, lb := range sortedValues(s.st.LBs, func(l LoadBalancer) string { return l.Name }) {
		if (projectID == "" || lb.ProjectID == projectID) && (vpcID == "" || lb.VPCID == vpcID) {
			out = append(out, lb)
		}
	}
	return out
}

// GetLoadBalancer returns one load balancer.
func (s *Store) GetLoadBalancer(id string) (LoadBalancer, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	lb, ok := s.st.LBs[id]
	return lb, ok
}

// CreateLoadBalancer adds a load balancer.
func (s *Store) CreateLoadBalancer(lb LoadBalancer) (LoadBalancer, error) {
	err := s.change(func(st *state) error {
		if lb.ID == "" {
			lb.ID = newID()
		}
		if !groupID.MatchString(lb.ID) {
			return invalidf("a load balancer ID is 16 hex digits")
		}
		if _, ok := st.LBs[lb.ID]; ok {
			return fmt.Errorf("%w: a load balancer %s exists already", ErrExists, lb.ID)
		}
		var err error
		if lb, err = validateLB(st, lb); err != nil {
			return err
		}
		now := s.now().UTC()
		lb.Created, lb.Updated = now, now
		st.LBs[lb.ID] = lb
		return nil
	})
	return lb, err
}

// UpdateLoadBalancer replaces a load balancer (it stays in its VPC).
func (s *Store) UpdateLoadBalancer(lb LoadBalancer) (LoadBalancer, error) {
	err := s.change(func(st *state) error {
		old, ok := st.LBs[lb.ID]
		if !ok {
			return fmt.Errorf("%w: no load balancer %s", ErrNotFound, lb.ID)
		}
		lb.VPCID, lb.Created = old.VPCID, old.Created
		var err error
		if lb, err = validateLB(st, lb); err != nil {
			return err
		}
		lb.Updated = s.now().UTC()
		st.LBs[lb.ID] = lb
		return nil
	})
	return lb, err
}

// DeleteLoadBalancer removes a load balancer.
func (s *Store) DeleteLoadBalancer(id string) error {
	return s.change(func(st *state) error {
		if _, ok := st.LBs[id]; !ok {
			return fmt.Errorf("%w: no load balancer %s", ErrNotFound, id)
		}
		delete(st.LBs, id)
		return nil
	})
}

// ---- OVN -------------------------------------------------------------------------------------

// MemberPort finds the logical port and subnet of a member address, for health checks.
type MemberPort func(ip string) (port string, subnet Subnet, ok bool)

// lastAddress is the subnet's last usable address (the default health check source).
func lastAddress(cidr string) string {
	p := netip.MustParsePrefix(cidr)
	a := p.Addr().As4()
	host := uint32(1)<<(32-p.Bits()) - 2
	v := uint32(a[0])<<24 | uint32(a[1])<<16 | uint32(a[2])<<8 | uint32(a[3])
	v += host
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}).String()
}

// ReconcileLoadBalancer makes the OVN load balancer match lb: it is made afresh (lb-add
// with --may-exist would add members, not replace them), attached to its switches and
// router, and given a health check with the logical port of every member.
func ReconcileLoadBalancer(ctx context.Context, nb NB, lb LoadBalancer, member MemberPort) error {
	name := lbName(lb.ID)
	vip := lb.VIP + ":" + strconv.Itoa(lb.Port)
	steps := [][]string{
		{"--if-exists", "lb-del", name},
		{"lb-add", name, vip, strings.Join(lb.Members, ","), lb.Protocol},
		{"set", "Load_Balancer", name, "external_ids:virtomir-lb=" + lb.ID, "external_ids:virtomir-project=" + lb.ProjectID},
	}
	for _, sw := range lb.AttachedSwitches {
		steps = append(steps, []string{"--may-exist", "ls-lb-add", sw, name})
	}
	for _, r := range lb.AttachedRouters {
		steps = append(steps, []string{"--may-exist", "lr-lb-add", r, name})
	}
	if hc := lb.HealthCheck; hc != nil {
		steps = append(steps, []string{"--", "--id=@hc", "create", "Load_Balancer_Health_Check", "vip=\"" + vip + "\"",
			fmt.Sprintf("options:interval=%d", hc.IntervalSeconds), fmt.Sprintf("options:timeout=%d", hc.TimeoutSeconds),
			fmt.Sprintf("options:success_count=%d", hc.Rise), fmt.Sprintf("options:failure_count=%d", hc.Fall),
			"--", "add", "Load_Balancer", name, "health_check", "@hc"})
		for _, m := range lb.Members {
			ip := netip.MustParseAddrPort(m).Addr().String()
			port, sub, ok := member(ip)
			if !ok {
				return fmt.Errorf("health check: no machine port has %s", ip)
			}
			src := hc.SourceIP
			if src == "" {
				src = lastAddress(sub.CIDR)
			}
			steps = append(steps, []string{"set", "Load_Balancer", name, "ip_port_mappings:" + ip + "=\"" + port + ":" + src + "\""})
		}
	}
	for _, s := range steps {
		if _, err := nb.Run(ctx, s...); err != nil {
			return err
		}
	}
	return nil
}

// DeleteLoadBalancerResources removes the OVN load balancer; its attachments and health
// check go with it.
func DeleteLoadBalancerResources(ctx context.Context, nb NB, lb LoadBalancer) error {
	_, err := nb.Run(ctx, "--if-exists", "lb-del", lbName(lb.ID))
	return err
}
