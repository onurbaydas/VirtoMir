package sdn

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestReconcileExternalGateway(t *testing.T) {
	f := &FakeRunner{}
	gw := ExternalGateway{VPCID: "prod", ExternalNetwork: "physnet1", GatewayIP: "203.0.113.1", ExternalIP: "203.0.113.10/24", Enabled: true, GatewayChassis: "hv01"}
	subs := []Subnet{{ID: "prod-web", CIDR: "10.0.1.0/24"}, {ID: "prod-db", CIDR: "10.0.2.0/24"}}
	if err := ReconcileExternalGateway(t.Context(), NB{R: f}, gw, subs); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ovn-nbctl --may-exist ls-add ext-prod",
		"ovn-nbctl lsp-set-type ln-prod localnet",
		"ovn-nbctl lsp-set-options ln-prod network_name=physnet1",
		"ovn-nbctl --may-exist lrp-add lr-prod lrp-prod-gw " + routerMAC("prod-gw") + " 203.0.113.10/24",
		"ovn-nbctl lsp-set-options lsp-prod-gw router-port=lrp-prod-gw",
		"ovn-nbctl lrp-set-gateway-chassis lrp-prod-gw hv01 20",
		"ovn-nbctl --may-exist lr-route-add lr-prod 0.0.0.0/0 203.0.113.1",
		"ovn-nbctl --may-exist lr-nat-add lr-prod snat 203.0.113.10 10.0.1.0/24",
		"ovn-nbctl --may-exist lr-nat-add lr-prod snat 203.0.113.10 10.0.2.0/24",
	} {
		if !f.called(want) {
			t.Errorf("missing %q in\n%s", want, strings.Join(f.Calls, "\n"))
		}
	}
}

func TestRemoveExternalGateway(t *testing.T) {
	f := &FakeRunner{}
	_ = RemoveExternalGateway(t.Context(), NB{R: f}, "prod")
	want := []string{
		"ovn-nbctl --if-exists lr-route-del lr-prod 0.0.0.0/0",
		"ovn-nbctl --if-exists lr-nat-del lr-prod snat",
		"ovn-nbctl --if-exists lrp-del lrp-prod-gw",
		"ovn-nbctl --if-exists ls-del ext-prod",
	}
	if !slices.Equal(f.Calls, want) {
		t.Errorf("%v", f.Calls)
	}
}

func TestReconcileFloatingIP(t *testing.T) {
	f := &FakeRunner{}
	fip := FloatingIP{VPCID: "prod", ExternalIP: "203.0.113.20", InternalIP: "10.0.1.11", PortID: "vm-port-1", Created: testTime}
	_ = ReconcileFloatingIP(t.Context(), NB{R: f}, fip, "52:54:00:AA:BB:CC")
	if !slices.Equal(f.Calls, []string{
		"ovn-nbctl --if-exists lr-nat-del lr-prod dnat_and_snat 203.0.113.20",
		"ovn-nbctl lr-nat-add lr-prod dnat_and_snat 203.0.113.20 10.0.1.11 vm-port-1 52:54:00:aa:bb:cc",
	}) {
		t.Errorf("assign %v", f.Calls)
	}
	f = &FakeRunner{}
	fip.InternalIP, fip.PortID = "", ""
	_ = ReconcileFloatingIP(t.Context(), NB{R: f}, fip, "")
	if !slices.Equal(f.Calls, []string{"ovn-nbctl --if-exists lr-nat-del lr-prod dnat_and_snat 203.0.113.20"}) {
		t.Errorf("unassign %v", f.Calls)
	}
}

func TestFloatingIPStoreLifecycle(t *testing.T) {
	s, _ := testStore(t)
	_, _ = s.CreateVPC(VPC{ID: "prod", Name: "P", ProjectID: "web", IPv4CIDR: "10.0.0.0/16"})
	_, _ = s.CreateSubnet(Subnet{ID: "prod-web", VPCID: "prod", Name: "web", CIDR: "10.0.1.0/24"})
	fip, err := s.CreateFloatingIP(FloatingIP{VPCID: "prod", ExternalIP: "203.0.113.20"})
	if err != nil || fip.ProjectID != "web" || fip.ID == "" {
		t.Fatalf("%+v %v", fip, err)
	}
	if _, err := s.CreateFloatingIP(FloatingIP{VPCID: "prod", ExternalIP: "203.0.113.20"}); !errors.Is(err, ErrExists) {
		t.Errorf("duplicate: %v", err)
	}
	if _, err := s.AssignFloatingIP(fip.ID, "10.9.9.9", ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("outside the VPC: %v", err)
	}
	if _, err := s.AssignFloatingIP(fip.ID, "10.0.1.11", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteFloatingIP(fip.ID); !errors.Is(err, ErrBusy) {
		t.Errorf("deleted while assigned: %v", err)
	}
	if err := s.DeleteVPC("prod"); !errors.Is(err, ErrBusy) {
		t.Errorf("deleted a VPC with a floating IP: %v", err)
	}
	_, _ = s.UnassignFloatingIP(fip.ID)
	if err := s.DeleteFloatingIP(fip.ID); err != nil {
		t.Errorf("delete: %v", err)
	}

	gw := ExternalGateway{VPCID: "prod", ExternalNetwork: "physnet1", GatewayIP: "198.51.100.1", ExternalIP: "203.0.113.10/24", Enabled: true, GatewayChassis: "hv01"}
	if _, err := s.SetExternalGateway(gw); !errors.Is(err, ErrInvalid) {
		t.Errorf("gateway off the external network: %v", err)
	}
	gw.GatewayIP = "203.0.113.1"
	if _, err := s.SetExternalGateway(gw); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.GetExternalGateway("prod"); !ok {
		t.Error("gateway not kept")
	}
	gw.Enabled = false
	_, _ = s.SetExternalGateway(gw)
	if _, ok := s.GetExternalGateway("prod"); ok {
		t.Error("gateway not removed")
	}
}
