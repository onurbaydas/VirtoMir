package sdn

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func portFixture(t *testing.T) (*Store, SecurityGroup) {
	t.Helper()
	s, _ := testStore(t)
	_, _ = s.CreateVPC(VPC{ID: "prod", Name: "P", ProjectID: "web", IPv4CIDR: "10.0.0.0/16"})
	_, _ = s.CreateSubnet(Subnet{ID: "prod-web", VPCID: "prod", Name: "web", CIDR: "10.0.1.0/24", DHCPEnabled: true})
	sub, _ := s.GetSubnet("prod-web")
	sub.OVNDHCPID = "dhcp-1"
	_, _ = s.UpdateSubnet(sub)
	sg, _ := s.CreateSecurityGroup(SecurityGroup{VPCID: "prod", Name: "web"})
	return s, sg
}

func TestAllocatePort(t *testing.T) {
	s, sg := portFixture(t)
	f := &FakeRunner{Answers: map[string]string{"ovn-nbctl get Logical_Switch_Port": "\"52:54:00:aa:bb:cc 10.0.1.5\"\n"}}
	p, err := AllocatePort(t.Context(), NB{R: f}, s, "prod-web", "4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e01", "52:54:00:AA:BB:CC", "", []string{sg.ID})
	if err != nil {
		t.Fatal(err)
	}
	id := "vm-port-4f1c2a7e-525400aabbcc"
	if p.ID != id || p.IP != "10.0.1.5" || p.MAC != "52:54:00:aa:bb:cc" {
		t.Errorf("port %+v", p)
	}
	for _, want := range []string{
		"ovn-nbctl --may-exist lsp-add ls-prod-web " + id,
		"ovn-nbctl lsp-set-addresses " + id + " 52:54:00:aa:bb:cc dynamic",
		"ovn-nbctl lsp-set-port-security " + id + " 52:54:00:aa:bb:cc 10.0.1.5",
		"ovn-nbctl lsp-set-dhcpv4-options " + id + " dhcp-1",
		"ovn-nbctl pg-set-ports pg_" + sg.ID + " " + id,
	} {
		if !f.called(want) {
			t.Errorf("missing %q in\n%s", want, strings.Join(f.Calls, "\n"))
		}
	}
	if g, _ := s.GetSecurityGroup(sg.ID); !slices.Equal(g.MemberPorts, []string{id}) {
		t.Errorf("group members %v", g.MemberPorts)
	}

	// A fixed address: no dynamic lookup; the same address twice is refused.
	f = &FakeRunner{}
	if _, err := AllocatePort(t.Context(), NB{R: f}, s, "prod-web", "vm-2", "52:54:00:00:00:02", "10.0.1.20", nil); err != nil || f.called("ovn-nbctl get") {
		t.Errorf("%v %v", err, f.Calls)
	}
	if _, err := AllocatePort(t.Context(), NB{R: f}, s, "prod-web", "vm-3", "52:54:00:00:00:03", "10.0.1.20", nil); !errors.Is(err, ErrBusy) {
		t.Errorf("taken address: %v", err)
	}
	if _, err := AllocatePort(t.Context(), NB{R: f}, s, "prod-web", "vm-3", "52:54:00:00:00:03", "10.0.1.1", nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("the gateway address: %v", err)
	}
	if err := s.DeleteSubnet("prod-web"); !errors.Is(err, ErrBusy) {
		t.Errorf("subnet with ports deleted: %v", err)
	}
}

func TestDeallocatePort(t *testing.T) {
	s, sg := portFixture(t)
	f := &FakeRunner{}
	p, _ := AllocatePort(t.Context(), NB{R: f}, s, "prod-web", "vm-1", "52:54:00:00:00:01", "10.0.1.10", []string{sg.ID})
	fip, _ := s.CreateFloatingIP(FloatingIP{VPCID: "prod", ExternalIP: "203.0.113.20"})
	_, _ = s.AssignFloatingIP(fip.ID, "10.0.1.10", p.ID)
	f = &FakeRunner{}
	if err := DeallocatePort(t.Context(), NB{R: f}, s, p.ID); err != nil {
		t.Fatal(err)
	}
	if !f.called("ovn-nbctl --if-exists lsp-del "+p.ID) || !f.called("ovn-nbctl clear Port_Group pg_"+sg.ID+" ports") ||
		!f.called("ovn-nbctl --if-exists lr-nat-del lr-prod dnat_and_snat 203.0.113.20") {
		t.Errorf("calls\n%s", strings.Join(f.Calls, "\n"))
	}
	if _, ok := s.GetPort(p.ID); ok {
		t.Error("port kept")
	}
	if got, _ := s.GetFloatingIP(fip.ID); got.InternalIP != "" {
		t.Error("floating IP still assigned")
	}
	if err := DeallocatePort(t.Context(), NB{R: f}, s, "unknown"); err != nil {
		t.Errorf("unknown port: %v", err)
	}
}
