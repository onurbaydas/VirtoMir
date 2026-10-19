package sdn

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestBuildMatchExpression(t *testing.T) {
	pg := portGroup("aaaaaaaaaaaaaaaa")
	for _, tc := range []struct {
		r    SecurityRule
		want string
	}{
		{SecurityRule{Direction: "ingress", Protocol: "tcp", PortMin: 22, PortMax: 22}, "outport == @pg_aaaaaaaaaaaaaaaa && ip4 && tcp && tcp.dst == 22"},
		{SecurityRule{Direction: "ingress", Protocol: "tcp", PortMin: 8000, PortMax: 8080}, "outport == @pg_aaaaaaaaaaaaaaaa && ip4 && tcp && tcp.dst >= 8000 && tcp.dst <= 8080"},
		{SecurityRule{Direction: "egress", Protocol: "udp", PortMin: 53, PortMax: 53, RemoteCIDR: "10.0.0.0/8"}, "inport == @pg_aaaaaaaaaaaaaaaa && ip4 && udp && udp.dst == 53 && ip4.dst == 10.0.0.0/8"},
		{SecurityRule{Direction: "ingress", Protocol: "icmp", RemoteCIDR: "0.0.0.0/0"}, "outport == @pg_aaaaaaaaaaaaaaaa && icmp4 && ip4.src == 0.0.0.0/0"},
		{SecurityRule{Direction: "ingress", Protocol: "tcp", PortMin: 5432, PortMax: 5432, RemoteGroupID: "bbbbbbbbbbbbbbbb"}, "outport == @pg_aaaaaaaaaaaaaaaa && ip4 && tcp && tcp.dst == 5432 && ip4.src == $pg_bbbbbbbbbbbbbbbb_ip4"},
		{SecurityRule{Direction: "egress", Protocol: "any"}, "inport == @pg_aaaaaaaaaaaaaaaa && ip4"},
	} {
		if got := BuildMatchExpression(tc.r, pg); got != tc.want {
			t.Errorf("got  %s\nwant %s", got, tc.want)
		}
	}
}

func TestReconcileSecurityGroup(t *testing.T) {
	f := &FakeRunner{}
	sg := SecurityGroup{ID: "aaaaaaaaaaaaaaaa", ProjectID: "web", DefaultDeny: true, MemberPorts: []string{"vm-port-1", "vm-port-2"}, Rules: []SecurityRule{
		{Direction: "ingress", Action: "allow-related", Priority: 1000, Protocol: "tcp", PortMin: 443, PortMax: 443},
	}}
	if err := ReconcileSecurityGroup(t.Context(), NB{R: f}, sg); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"ovn-nbctl --may-exist pg-add pg_aaaaaaaaaaaaaaaa",
		"ovn-nbctl pg-set-ports pg_aaaaaaaaaaaaaaaa vm-port-1 vm-port-2",
		"ovn-nbctl set Port_Group pg_aaaaaaaaaaaaaaaa external_ids:virtomir-group=aaaaaaaaaaaaaaaa external_ids:virtomir-project=web",
		"ovn-nbctl --type=port-group acl-del pg_aaaaaaaaaaaaaaaa",
		"ovn-nbctl --type=port-group acl-add pg_aaaaaaaaaaaaaaaa to-lport 1 outport == @pg_aaaaaaaaaaaaaaaa && ip4 drop",
		"ovn-nbctl --type=port-group acl-add pg_aaaaaaaaaaaaaaaa from-lport 1 inport == @pg_aaaaaaaaaaaaaaaa && ip4 drop",
		"ovn-nbctl --type=port-group acl-add pg_aaaaaaaaaaaaaaaa to-lport 1000 outport == @pg_aaaaaaaaaaaaaaaa && ip4 && tcp && tcp.dst == 443 allow-related",
	}
	if !slices.Equal(f.Calls, want) {
		t.Errorf("calls\n%s", strings.Join(f.Calls, "\n"))
	}
	f = &FakeRunner{}
	sg.MemberPorts, sg.DefaultDeny = nil, false
	_ = ReconcileSecurityGroup(t.Context(), NB{R: f}, sg)
	if !f.called("ovn-nbctl clear Port_Group pg_aaaaaaaaaaaaaaaa ports") || f.called("ovn-nbctl pg-set-ports") {
		t.Errorf("empty group %v", f.Calls)
	}
	f = &FakeRunner{}
	_ = DeleteSecurityGroupResources(t.Context(), NB{R: f}, "aaaaaaaaaaaaaaaa")
	if !slices.Equal(f.Calls, []string{"ovn-nbctl --if-exists destroy Port_Group pg_aaaaaaaaaaaaaaaa"}) {
		t.Errorf("delete %v", f.Calls)
	}
}

func TestSecurityGroupStore(t *testing.T) {
	s, _ := testStore(t)
	_, _ = s.CreateVPC(VPC{ID: "prod", Name: "P", ProjectID: "web", IPv4CIDR: "10.0.0.0/16"})
	web, err := s.CreateSecurityGroup(SecurityGroup{VPCID: "prod", Name: "web"})
	if err != nil || len(web.Rules) != 1 || web.Rules[0].Direction != "egress" || web.ProjectID != "web" {
		t.Fatalf("%+v %v (a new group lets its machines out)", web, err)
	}
	db, _ := s.CreateSecurityGroup(SecurityGroup{VPCID: "prod", Name: "db"})
	r, err := s.AddSecurityRule(db.ID, SecurityRule{Direction: "ingress", Protocol: "tcp", PortMin: 5432, RemoteGroupID: web.ID})
	if err != nil || r.PortMax != 5432 || r.Action != "allow-related" || r.Priority != 1000 {
		t.Fatalf("%+v %v", r, err)
	}
	for name, bad := range map[string]SecurityRule{
		"direction":      {Direction: "sideways"},
		"ports on icmp":  {Direction: "ingress", Protocol: "icmp", PortMin: 1},
		"port range":     {Direction: "ingress", Protocol: "tcp", PortMin: 90, PortMax: 80},
		"both remotes":   {Direction: "ingress", RemoteCIDR: "10.0.0.0/8", RemoteGroupID: web.ID},
		"priority 1":     {Direction: "ingress", Priority: 1},
		"remote network": {Direction: "ingress", RemoteCIDR: "nope"},
		"unknown remote": {Direction: "ingress", RemoteGroupID: "ffffffffffffffff"},
	} {
		if _, err := s.AddSecurityRule(db.ID, bad); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := s.DeleteSecurityGroup(web.ID); !errors.Is(err, ErrBusy) {
		t.Errorf("deleted a group another group's rule names: %v", err)
	}
	_ = s.change(func(st *state) error {
		st.Ports["vm-port-1"] = Port{ID: "vm-port-1", SubnetID: "x"}
		return nil
	})
	if err := s.BindPortToSecurityGroup(db.ID, "vm-port-1"); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.GetSecurityGroup(db.ID); !slices.Equal(g.MemberPorts, []string{"vm-port-1"}) {
		t.Errorf("members %v", g.MemberPorts)
	}
	if err := s.DeleteSecurityGroup(db.ID); !errors.Is(err, ErrBusy) {
		t.Errorf("deleted a group with ports: %v", err)
	}
	_ = s.UnbindPortFromSecurityGroup(db.ID, "vm-port-1")
	_ = s.DeleteSecurityRule(db.ID, r.ID)
	if err := s.DeleteSecurityGroup(db.ID); err != nil {
		t.Errorf("delete: %v", err)
	}
	if err := s.DeleteVPC("prod"); !errors.Is(err, ErrBusy) {
		t.Errorf("deleted a VPC with a group: %v", err)
	}
}
