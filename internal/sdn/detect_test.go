package sdn

import (
	"errors"
	"testing"
)

func TestDetectHostInstalled(t *testing.T) {
	f := &FakeRunner{Answers: map[string]string{
		"ovs-vsctl --version":                                                  "ovs-vsctl (Open vSwitch) 3.3.0\nDB Schema 8.5.0\n",
		"ovn-nbctl --version":                                                  "ovn-nbctl 24.03.2\nOpen vSwitch Library 3.3.0\n",
		"systemctl is-active openvswitch-switch":                               "active\n",
		"systemctl is-active ovn-central":                                      "active\n",
		"systemctl is-active ovn-host":                                         "active\n",
		"ovs-vsctl --if-exists get Open_vSwitch . external_ids:system-id":      "\"6f0e3c1a-hv01\"\n",
		"ovs-vsctl --if-exists get Open_vSwitch . external_ids:ovn-encap-type": "geneve\n",
		"ovs-vsctl --if-exists get Open_vSwitch . external_ids:ovn-encap-ip":   "\"10.20.0.11\"\n",
		"ovs-vsctl --if-exists get Open_vSwitch . external_ids:ovn-remote":     "\"tcp:10.20.0.11:6642\"\n",
	}}
	st, err := DetectHost(t.Context(), f)
	if err != nil {
		t.Fatal(err)
	}
	want := HostStatus{Installed: true, OVSVersion: "3.3.0", OVNVersion: "24.03.2", ServiceOVS: true, ServiceOVNCentral: true, ServiceOVNHost: true,
		IntegrationBridge: "br-int", SystemID: "6f0e3c1a-hv01", EncapsulationType: "geneve", EncapsulationIP: "10.20.0.11",
		Role: "central", CentralEndpoint: "tcp:10.20.0.11:6642"}
	if st != want {
		t.Errorf("status\n got %+v\nwant %+v", st, want)
	}
}

func TestDetectHostMissing(t *testing.T) {
	st, err := DetectHost(t.Context(), &FakeRunner{Missing: map[string]bool{"ovs-vsctl": true}})
	if err != nil || st.Installed || st.OVSVersion != "" {
		t.Errorf("%+v %v", st, err)
	}
	// OVS without OVN, no br-int yet: installed is about OVN.
	f := &FakeRunner{Answers: map[string]string{"ovs-vsctl --version": "ovs-vsctl (Open vSwitch) 3.3.0"},
		Missing: map[string]bool{"ovn-nbctl": true}, Errors: map[string]error{"ovs-vsctl br-exists": errors.New("exit status 2")}}
	st, _ = DetectHost(t.Context(), f)
	if st.Installed || st.OVSVersion != "3.3.0" || st.IntegrationBridge != "" || st.Role != "controller" {
		t.Errorf("OVS only: %+v", st)
	}
}

const sbChassis = `{"data":[["6f0e3c1a-hv02","hv-02"],["6f0e3c1a-hv01","hv-01"]],"headings":["name","hostname"]}`
const sbEncap = `{"data":[["6f0e3c1a-hv01","10.20.0.11","geneve"],["6f0e3c1a-hv02","10.20.0.12","vxlan"],["6f0e3c1a-hv02","10.20.0.12","geneve"]],"headings":["chassis_name","ip","type"]}`

func TestListChassis(t *testing.T) {
	f := &FakeRunner{Answers: map[string]string{
		"ovn-sbctl --format=json --columns=name,hostname list Chassis":      sbChassis,
		"ovn-sbctl --format=json --columns=chassis_name,ip,type list Encap": sbEncap,
	}}
	got, err := ListChassis(t.Context(), f)
	if err != nil {
		t.Fatal(err)
	}
	want := []ChassisInfo{{"6f0e3c1a-hv01", "hv-01", "10.20.0.11", "geneve"}, {"6f0e3c1a-hv02", "hv-02", "10.20.0.12", "geneve"}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("chassis %+v", got)
	}
	if _, err := ListChassis(t.Context(), &FakeRunner{Answers: map[string]string{"ovn-sbctl": "not json"}}); err == nil {
		t.Error("garbage accepted")
	}
}

func TestAtomForms(t *testing.T) {
	for raw, want := range map[string]string{`"x"`: "x", `["uuid","u-1"]`: "u-1", `["set",["a","b"]]`: "a", `["set",[]]`: "", `7`: ""} {
		if got := atom([]byte(raw)); got != want {
			t.Errorf("atom(%s) = %q", raw, got)
		}
	}
}

func TestEnsureIntegrationBridge(t *testing.T) {
	f := &FakeRunner{}
	if err := EnsureIntegrationBridge(t.Context(), f, "br-int"); err != nil || !f.called("ovs-vsctl --may-exist add-br br-int") {
		t.Errorf("%v %v", err, f.Calls)
	}
}
