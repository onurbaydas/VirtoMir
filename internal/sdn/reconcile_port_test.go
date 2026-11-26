package sdn

import (
	"reflect"
	"strings"
	"testing"
)

func TestReconcilePortKeepsItsAddress(t *testing.T) {
	s, _ := portFixture(t)
	sub, _ := s.GetSubnet("prod-web")
	p := Port{ID: "vm-port-4f1c2a7e-525400aabbcc", SubnetID: "prod-web", VMUUID: "4f1c2a7e", MAC: "52:54:00:aa:bb:cc", IP: "10.0.1.5"}
	f := &FakeRunner{}
	if err := ReconcilePort(t.Context(), NB{R: f}, p, sub); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ovn-nbctl --may-exist lsp-add ls-prod-web " + p.ID,
		"ovn-nbctl lsp-set-addresses " + p.ID + " 52:54:00:aa:bb:cc 10.0.1.5",
		"ovn-nbctl set Logical_Switch_Port " + p.ID + " external_ids:virtomir-vm=4f1c2a7e external_ids:virtomir-subnet=prod-web",
		"ovn-nbctl lsp-set-port-security " + p.ID + " 52:54:00:aa:bb:cc 10.0.1.5",
		"ovn-nbctl lsp-set-dhcpv4-options " + p.ID + " dhcp-1",
	} {
		if !f.called(want) {
			t.Errorf("missing %q in\n%s", want, strings.Join(f.Calls, "\n"))
		}
	}
	if f.called("ovn-nbctl get") || f.called("ovn-nbctl lsp-set-addresses "+p.ID+" 52:54:00:aa:bb:cc dynamic") {
		t.Errorf("asked OVN's IPAM for a new address:\n%s", strings.Join(f.Calls, "\n"))
	}
	if err := ReconcilePort(t.Context(), NB{R: &FakeRunner{}}, Port{ID: "x", SubnetID: "prod-web"}, sub); err == nil {
		t.Error("a port without a stored address was reconciled")
	}
}

func TestFindOrphans(t *testing.T) {
	s, sg := portFixture(t)
	f := &FakeRunner{}
	p, err := AllocatePort(t.Context(), NB{R: f}, s, "prod-web", "4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e01", "52:54:00:aa:bb:cc", "10.0.1.5", []string{sg.ID})
	if err != nil {
		t.Fatal(err)
	}
	f = &FakeRunner{Answers: map[string]string{
		"ovn-nbctl --bare --columns=name list Logical_Router":      "lr-prod\n\nlr-gone\n\nsomeone-elses\n",
		"ovn-nbctl --bare --columns=name list Logical_Switch":      "ls-prod-web\n\next-prod\n\nls-old\n",
		"ovn-nbctl --bare --columns=name list Logical_Switch_Port": p.ID + "\n\nlrp-prod-web\n\nvm-port-deadbeef-525400000001\n",
		"ovn-nbctl --bare --columns=name list Port_Group":          "pg_" + sg.ID + "\n\npg_lost\n",
		"ovn-nbctl --bare --columns=name list Load_Balancer":       "",
	}}
	got, err := FindOrphans(t.Context(), NB{R: f}, s)
	if err != nil {
		t.Fatal(err)
	}
	want := []Orphan{
		{"Logical_Router", "lr-gone"},
		{"Logical_Switch", "ls-old"},
		{"Logical_Switch_Port", "vm-port-deadbeef-525400000001"},
		{"Port_Group", "pg_lost"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("orphans %v, want %v", got, want)
	}
	if f.called("ovn-nbctl --if-exists") || f.called("ovn-nbctl lsp-del") || f.called("ovn-nbctl destroy") {
		t.Errorf("deleted something:\n%s", strings.Join(f.Calls, "\n"))
	}
}
