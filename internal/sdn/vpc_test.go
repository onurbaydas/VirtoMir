package sdn

import (
	"slices"
	"strings"
	"testing"
)

func TestReconcileVPC(t *testing.T) {
	f := &FakeRunner{}
	if err := ReconcileVPC(t.Context(), NB{R: f, DB: "tcp:10.20.0.11:6641"}, VPC{ID: "prod", ProjectID: "web"}); err != nil {
		t.Fatal(err)
	}
	if !f.called("ovn-nbctl --db=tcp:10.20.0.11:6641 --may-exist lr-add lr-prod") ||
		!f.called("ovn-nbctl --db=tcp:10.20.0.11:6641 set Logical_Router lr-prod external_ids:virtomir-project=web") {
		t.Errorf("calls %v", f.Calls)
	}
	f = &FakeRunner{}
	_ = DeleteVPCResources(t.Context(), NB{R: f}, "prod")
	if !slices.Equal(f.Calls, []string{"ovn-nbctl --if-exists lr-del lr-prod"}) {
		t.Errorf("delete %v", f.Calls)
	}
}

func TestReconcileSubnet(t *testing.T) {
	sub := Subnet{ID: "prod-web", VPCID: "prod", CIDR: "10.0.1.0/24", GatewayIP: "10.0.1.1", DHCPEnabled: true, DNSServers: []string{"1.1.1.1", "9.9.9.9"}}
	mac := routerMAC(sub.ID)
	f := &FakeRunner{Answers: map[string]string{"ovn-nbctl create DHCP_Options": "8a7b6c5d-0000-4000-8000-000000000001\n"}}
	id, err := ReconcileSubnet(t.Context(), NB{R: f}, VPC{ID: "prod"}, sub)
	if err != nil || id != "8a7b6c5d-0000-4000-8000-000000000001" {
		t.Fatalf("%q %v", id, err)
	}
	for _, want := range []string{
		"ovn-nbctl --may-exist ls-add ls-prod-web",
		"ovn-nbctl --may-exist lrp-add lr-prod lrp-prod-web " + mac + " 10.0.1.1/24",
		"ovn-nbctl --may-exist lsp-add ls-prod-web lsp-prod-web-router",
		"ovn-nbctl lsp-set-type lsp-prod-web-router router",
		"ovn-nbctl lsp-set-addresses lsp-prod-web-router router",
		"ovn-nbctl lsp-set-options lsp-prod-web-router router-port=lrp-prod-web",
		"ovn-nbctl create DHCP_Options cidr=10.0.1.0/24 external_ids:virtomir-subnet=prod-web options:server_id=10.0.1.1 options:server_mac=" + mac + " options:router=10.0.1.1 options:lease_time=3600 options:dns_server={1.1.1.1,9.9.9.9}",
	} {
		if !f.called(want) {
			t.Errorf("missing %q in\n%s", want, strings.Join(f.Calls, "\n"))
		}
	}
	// Again: the DHCP row is found and updated, not created twice.
	f = &FakeRunner{Answers: map[string]string{"ovn-nbctl --bare --columns=_uuid find DHCP_Options": "8a7b6c5d-0000-4000-8000-000000000001\n"}}
	if id, _ := ReconcileSubnet(t.Context(), NB{R: f}, VPC{ID: "prod"}, sub); id != "8a7b6c5d-0000-4000-8000-000000000001" || f.called("ovn-nbctl create") ||
		!f.called("ovn-nbctl set DHCP_Options 8a7b6c5d-0000-4000-8000-000000000001 cidr=10.0.1.0/24") {
		t.Errorf("second pass %v", f.Calls)
	}
	if routerMAC("a-b") == routerMAC("a-c") || !strings.HasPrefix(routerMAC("x"), "02:") {
		t.Error("router MACs")
	}
}

func TestDeleteSubnetResources(t *testing.T) {
	f := &FakeRunner{}
	_ = DeleteSubnetResources(t.Context(), NB{R: f}, "prod", Subnet{ID: "prod-web", OVNDHCPID: "8a7b"})
	want := []string{
		"ovn-nbctl --if-exists lsp-del lsp-prod-web-router",
		"ovn-nbctl --if-exists lrp-del lrp-prod-web",
		"ovn-nbctl --if-exists ls-del ls-prod-web",
		"ovn-nbctl --if-exists destroy DHCP_Options 8a7b",
	}
	if !slices.Equal(f.Calls, want) {
		t.Errorf("%v", f.Calls)
	}
}
