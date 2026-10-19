package sdn

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestReconcileIPsec(t *testing.T) {
	on := IPsecConfig{Enabled: true, Mode: "cert", CertFile: "/etc/ipsec.d/certs/hv01.pem", KeyFile: "/etc/ipsec.d/private/hv01.key", CACertFile: "/etc/ipsec.d/cacerts/ca.pem"}
	f := &FakeRunner{}
	if err := ReconcileIPsec(t.Context(), f, NB{R: f}, true, on); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"ovs-vsctl set Open_vSwitch . other_config:certificate=/etc/ipsec.d/certs/hv01.pem other_config:private_key=/etc/ipsec.d/private/hv01.key other_config:ca_cert=/etc/ipsec.d/cacerts/ca.pem",
		"systemctl enable --now openvswitch-ipsec",
		"ovn-nbctl set NB_Global . ipsec=true",
	}
	if !slices.Equal(f.Calls, want) {
		t.Errorf("enable\n%s", strings.Join(f.Calls, "\n"))
	}
	f = &FakeRunner{}
	_ = ReconcileIPsec(t.Context(), f, NB{R: f}, false, IPsecConfig{})
	if !slices.Equal(f.Calls, []string{"systemctl disable --now openvswitch-ipsec", "ovs-vsctl remove Open_vSwitch . other_config certificate private_key ca_cert"}) {
		t.Errorf("disable on a controller %v", f.Calls)
	}
	f = &FakeRunner{Errors: map[string]error{"systemctl enable": errors.New("unit not found")}}
	if err := ReconcileIPsec(t.Context(), f, NB{R: f}, true, on); err == nil || !strings.Contains(err.Error(), "ipsec") {
		t.Errorf("err %v", err)
	}
}

func TestIPsecStore(t *testing.T) {
	s, _ := testStore(t)
	if _, err := s.SetIPsecConfig(IPsecConfig{Enabled: true, Mode: "psk", PSK: "x"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("PSK accepted: %v", err)
	}
	if _, err := s.SetIPsecConfig(IPsecConfig{Enabled: true, CertFile: "relative.pem", KeyFile: "/k", CACertFile: "/ca"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("relative path accepted: %v", err)
	}
	c, err := s.SetIPsecConfig(IPsecConfig{Enabled: true, CertFile: "/c", KeyFile: "/k", CACertFile: "/ca", PSK: "old"})
	if err != nil || c.Mode != "cert" || c.PSK != "" {
		t.Fatalf("%+v %v", c, err)
	}
	_ = s.SetIPsecStatus("active", "")
	if got := s.GetIPsecConfig(); got.Status != "active" || !got.Enabled {
		t.Errorf("%+v", got)
	}
	if (IPsecConfig{PSK: "secret"}).Redacted().PSK != "********" {
		t.Error("redacted")
	}
}
