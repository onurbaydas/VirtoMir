package sdn

import (
	"errors"
	"path/filepath"
	"testing"
)

func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sdn.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return s, path
}

func TestStoreVPCs(t *testing.T) {
	s, path := testStore(t)
	v, err := s.CreateVPC(VPC{ID: "prod", Name: "Production", ProjectID: "web", IPv4CIDR: "10.0.0.0/16"})
	if err != nil || v.Created.IsZero() {
		t.Fatalf("%+v %v", v, err)
	}
	if _, err := s.CreateVPC(VPC{ID: "prod2", Name: "production", ProjectID: "web", IPv4CIDR: "10.1.0.0/16"}); !errors.Is(err, ErrExists) {
		t.Errorf("same name in the project: %v", err)
	}
	for _, bad := range []VPC{
		{ID: "X", Name: "x", ProjectID: "p", IPv4CIDR: "10.0.0.0/8"},
		{ID: "ok-1", Name: "x", ProjectID: "", IPv4CIDR: "10.0.0.0/8"},
		{ID: "ok-1", Name: "x", ProjectID: "p", IPv4CIDR: "10.0.0.1/8"},
		{ID: "ok-1", Name: "x", ProjectID: "p", IPv4CIDR: "fd00::/64"},
	} {
		if _, err := s.CreateVPC(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("accepted %+v: %v", bad, err)
		}
	}
	if r, ok := s.GetRouter("prod"); !ok || r.LogicalRouter != "lr-prod" {
		t.Errorf("router %+v", r)
	}
	if _, err := s.CreateSubnet(Subnet{ID: "prod-web", VPCID: "prod", Name: "web", CIDR: "10.0.1.0/24", DHCPEnabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteVPC("prod"); !errors.Is(err, ErrBusy) {
		t.Errorf("deleted a VPC with a subnet: %v", err)
	}
	v.IPv4CIDR = "10.0.2.0/24"
	if _, err := s.UpdateVPC(v); !errors.Is(err, ErrInvalid) {
		t.Errorf("shrank the VPC below its subnet: %v", err)
	}
	v.IPv4CIDR, v.Description = "10.0.0.0/15", "grown"
	if _, err := s.UpdateVPC(v); err != nil {
		t.Errorf("grow: %v", err)
	}

	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := r.GetVPC("prod"); got.Description != "grown" || len(r.ListSubnets("prod")) != 1 || len(r.ListVPCs("other")) != 0 {
		t.Errorf("reloaded %+v", got)
	}
	_ = s.DeleteSubnet("prod-web")
	if err := s.DeleteVPC("prod"); err != nil {
		t.Errorf("delete empty VPC: %v", err)
	}
	if _, ok := s.GetRouter("prod"); ok {
		t.Error("router left behind")
	}
}

func TestSubnetValidation(t *testing.T) {
	s, _ := testStore(t)
	_, _ = s.CreateVPC(VPC{ID: "prod", Name: "P", ProjectID: "web", IPv4CIDR: "10.0.0.0/16"})
	sub, err := s.CreateSubnet(Subnet{ID: "prod-web", VPCID: "prod", Name: "web", CIDR: "10.0.1.0/24"})
	if err != nil || sub.GatewayIP != "10.0.1.1" || sub.LogicalSwitch != "ls-prod-web" {
		t.Fatalf("%+v %v (gateway defaults to the first address)", sub, err)
	}
	for name, bad := range map[string]Subnet{
		"outside the VPC": {ID: "s-1", VPCID: "prod", Name: "x", CIDR: "10.1.0.0/24"},
		"overlap":         {ID: "s-1", VPCID: "prod", Name: "x", CIDR: "10.0.1.128/25"},
		"not a network":   {ID: "s-1", VPCID: "prod", Name: "x", CIDR: "10.0.2.5/24"},
		"gateway outside": {ID: "s-1", VPCID: "prod", Name: "x", CIDR: "10.0.2.0/24", GatewayIP: "10.0.3.1"},
		"pool outside":    {ID: "s-1", VPCID: "prod", Name: "x", CIDR: "10.0.2.0/24", AllocationPoolStart: "10.0.9.10"},
		"bad DNS":         {ID: "s-1", VPCID: "prod", Name: "x", CIDR: "10.0.2.0/24", DNSServers: []string{"dns.example"}},
		"too small":       {ID: "s-1", VPCID: "prod", Name: "x", CIDR: "10.0.2.0/30"},
	} {
		if _, err := s.CreateSubnet(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
	if _, err := s.CreateSubnet(Subnet{ID: "s-1", VPCID: "nope", Name: "x", CIDR: "10.0.2.0/24"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown VPC: %v", err)
	}
	_ = s.change(func(st *state) error {
		st.Ports["p1"] = Port{ID: "p1", SubnetID: "prod-web", VMUUID: "vm-1"}
		return nil
	})
	if err := s.DeleteSubnet("prod-web"); !errors.Is(err, ErrBusy) {
		t.Errorf("deleted a subnet with a machine on it: %v", err)
	}
}

func TestSharedAdopt(t *testing.T) {
	a, _ := testStore(t)
	b, _ := testStore(t)
	_, _ = a.CreateVPC(VPC{ID: "prod", Name: "P", ProjectID: "web", IPv4CIDR: "10.0.0.0/16"})
	if ok, err := b.Adopt(a.Shared()); !ok || err != nil {
		t.Fatalf("%v %v", ok, err)
	}
	if _, ok := b.GetVPC("prod"); !ok {
		t.Error("not adopted")
	}
	if ok, _ := b.Adopt(a.Shared()); ok {
		t.Error("same version adopted twice")
	}
}
