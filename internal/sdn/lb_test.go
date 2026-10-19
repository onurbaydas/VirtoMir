package sdn

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestReconcileLoadBalancer(t *testing.T) {
	f := &FakeRunner{}
	lb := LoadBalancer{ID: "cccccccccccccccc", ProjectID: "web", VPCID: "prod", VIP: "10.0.1.100", Port: 80, Protocol: "tcp",
		Members: []string{"10.0.1.11:8080", "10.0.1.12:8080"}, AttachedSwitches: []string{"ls-prod-web"}, AttachedRouters: []string{"lr-prod"},
		HealthCheck: &HealthCheckSpec{Protocol: "tcp", Port: 8080, IntervalSeconds: 5, TimeoutSeconds: 2, Fall: 3, Rise: 2}}
	ports := map[string]string{"10.0.1.11": "vm-port-a", "10.0.1.12": "vm-port-b"}
	member := func(ip string) (string, Subnet, bool) {
		p, ok := ports[ip]
		return p, Subnet{CIDR: "10.0.1.0/24"}, ok
	}
	if err := ReconcileLoadBalancer(t.Context(), NB{R: f}, lb, member); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"ovn-nbctl --if-exists lb-del lb_cccccccccccccccc",
		"ovn-nbctl lb-add lb_cccccccccccccccc 10.0.1.100:80 10.0.1.11:8080,10.0.1.12:8080 tcp",
		"ovn-nbctl set Load_Balancer lb_cccccccccccccccc external_ids:virtomir-lb=cccccccccccccccc external_ids:virtomir-project=web",
		"ovn-nbctl --may-exist ls-lb-add ls-prod-web lb_cccccccccccccccc",
		"ovn-nbctl --may-exist lr-lb-add lr-prod lb_cccccccccccccccc",
		`ovn-nbctl -- --id=@hc create Load_Balancer_Health_Check vip="10.0.1.100:80" options:interval=5 options:timeout=2 options:success_count=2 options:failure_count=3 -- add Load_Balancer lb_cccccccccccccccc health_check @hc`,
		`ovn-nbctl set Load_Balancer lb_cccccccccccccccc ip_port_mappings:10.0.1.11="vm-port-a:10.0.1.254"`,
		`ovn-nbctl set Load_Balancer lb_cccccccccccccccc ip_port_mappings:10.0.1.12="vm-port-b:10.0.1.254"`,
	}
	if !slices.Equal(f.Calls, want) {
		t.Errorf("calls\n%s", strings.Join(f.Calls, "\n"))
	}
	ports = map[string]string{}
	if err := ReconcileLoadBalancer(t.Context(), NB{R: &FakeRunner{}}, lb, member); err == nil {
		t.Error("health check without the members' ports")
	}
	f = &FakeRunner{}
	_ = DeleteLoadBalancerResources(t.Context(), NB{R: f}, lb)
	if !slices.Equal(f.Calls, []string{"ovn-nbctl --if-exists lb-del lb_cccccccccccccccc"}) {
		t.Errorf("delete %v", f.Calls)
	}
}

func TestLoadBalancerStore(t *testing.T) {
	s, _ := testStore(t)
	_, _ = s.CreateVPC(VPC{ID: "prod", Name: "P", ProjectID: "web", IPv4CIDR: "10.0.0.0/16"})
	_, _ = s.CreateSubnet(Subnet{ID: "prod-web", VPCID: "prod", Name: "web", CIDR: "10.0.1.0/24"})
	lb, err := s.CreateLoadBalancer(LoadBalancer{VPCID: "prod", Name: "web", VIP: "10.0.1.100", Port: 80, Members: []string{"10.0.1.11:8080"},
		AttachedSwitches: []string{"ls-prod-web"}, HealthCheck: &HealthCheckSpec{}})
	if err != nil || lb.Protocol != "tcp" || lb.HealthCheck.IntervalSeconds != 5 || lb.ProjectID != "web" {
		t.Fatalf("%+v %v", lb, err)
	}
	for name, bad := range map[string]LoadBalancer{
		"member":       {VPCID: "prod", Name: "x", VIP: "10.0.1.100", Port: 80, Members: []string{"10.0.1.11"}},
		"protocol":     {VPCID: "prod", Name: "x", VIP: "10.0.1.100", Port: 80, Protocol: "sctp", Members: []string{"10.0.1.11:80"}},
		"http check":   {VPCID: "prod", Name: "x", VIP: "10.0.1.100", Port: 80, Members: []string{"10.0.1.11:80"}, HealthCheck: &HealthCheckSpec{Protocol: "http"}},
		"other switch": {VPCID: "prod", Name: "x", VIP: "10.0.1.100", Port: 80, Members: []string{"10.0.1.11:80"}, AttachedSwitches: []string{"ls-elsewhere"}},
		"other router": {VPCID: "prod", Name: "x", VIP: "10.0.1.100", Port: 80, Members: []string{"10.0.1.11:80"}, AttachedRouters: []string{"lr-other"}},
	} {
		if _, err := s.CreateLoadBalancer(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
	lb.Members = append(lb.Members, "10.0.1.12:8080")
	if got, err := s.UpdateLoadBalancer(lb); err != nil || len(got.Members) != 2 {
		t.Errorf("update %+v %v", got, err)
	}
	if err := s.DeleteVPC("prod"); !errors.Is(err, ErrBusy) {
		t.Errorf("VPC with a load balancer deleted: %v", err)
	}
	if err := s.DeleteLoadBalancer(lb.ID); err != nil {
		t.Error(err)
	}
	if lastAddress("10.0.1.0/24") != "10.0.1.254" || lastAddress("10.0.0.0/16") != "10.0.255.254" {
		t.Error("lastAddress")
	}
}
