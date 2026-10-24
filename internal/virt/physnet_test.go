package virt

import (
	"bytes"
	"strings"
	"testing"
)

func TestStaticNetworkConfig(t *testing.T) {
	ci := CloudInit{User: "ops", Password: "pw", Network: &StaticNetwork{MAC: "52:54:00:AA:BB:01", Addresses: []string{"10.20.0.50/24", "fd00:20::50/64"},
		Gateway4: "10.20.0.1", Gateway6: "fd00:20::1", Nameservers: []string{"10.20.0.53"}, Search: []string{"prod.example"}}}
	if err := ci.normalize("web-01"); err != nil {
		t.Fatal(err)
	}
	want := `version: 2
ethernets:
  nic0:
    match:
      macaddress: "52:54:00:aa:bb:01"
    dhcp4: false
    dhcp6: false
    addresses: ["10.20.0.50/24", "fd00:20::50/64"]
    routes:
      - to: default
        via: "10.20.0.1"
      - to: "::/0"
        via: "fd00:20::1"
    nameservers:
      addresses: ["10.20.0.53"]
      search: ["prod.example"]
`
	if got := ci.networkConfig(); got != want {
		t.Errorf("got\n%s", got)
	}
	iso, err := ci.seedISO()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(iso, []byte("macaddress")) || !bytes.Contains(bytes.ToUpper(iso), []byte("NETWORK_CONFIG")) && !bytes.Contains(iso, []byte("network-config")) {
		t.Error("the seed has no network-config")
	}
	plain := CloudInit{User: "ops", Password: "pw"}
	_ = plain.normalize("db")
	if plain.networkConfig() != "" {
		t.Error("network-config without a fixed address")
	}
	for i, n := range []StaticNetwork{
		{Addresses: []string{"10.20.0.50/24"}},
		{MAC: "52:54:00:aa:bb:01"},
		{MAC: "52:54:00:aa:bb:01", Addresses: []string{"10.20.0.50"}},
		{MAC: "52:54:00:aa:bb:01", Addresses: []string{"10.20.0.50/24"}, Gateway4: "gw"},
		{MAC: "52:54:00:aa:bb:01", Addresses: []string{"10.20.0.50/24"}, Search: []string{"bad domain"}},
	} {
		c := CloudInit{User: "ops", Password: "pw", Network: &n}
		if err := c.normalize("x"); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
}

func TestStaticAddressNeedsItsAdapter(t *testing.T) {
	spec := CreateSpec{Name: "web", VCPUs: 1, MemoryMiB: 512, BaseImage: "/img/x.qcow2", DiskGiB: 10, Network: &NICSpec{Network: "default"},
		CloudInit: &CloudInit{User: "ops", Password: "pw", Network: &StaticNetwork{MAC: "52:54:00:aa:bb:01", Addresses: []string{"10.20.0.50/24"}}}}
	if err := spec.normalize(); err != nil {
		t.Fatal(err)
	}
	if spec.Network.MAC != "52:54:00:aa:bb:01" {
		t.Errorf("the adapter did not get the address's MAC: %q", spec.Network.MAC)
	}
	spec.Network.MAC = "52:54:00:aa:bb:02"
	if err := spec.normalize(); err == nil || !strings.Contains(err.Error(), "MAC") {
		t.Errorf("different MAC: %v", err)
	}
	spec.Network = nil
	if err := spec.normalize(); err == nil {
		t.Error("a fixed address without an adapter")
	}
}

func TestHostdevNetwork(t *testing.T) {
	x, err := buildNetwork(NetworkSpec{Name: "sriov-v20", Mode: "hostdev", PF: "ens1f0", VLAN: 20})
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := x.Marshal()
	for _, want := range []string{`<forward mode="hostdev" managed="yes">`, `<pf dev="ens1f0">`, `<tag id="20">`} {
		if !strings.Contains(doc, want) {
			t.Errorf("missing %s in\n%s", want, doc)
		}
	}
	v, err := parseNetwork(doc)
	if err != nil || v.Mode != "hostdev" || v.PF != "ens1f0" {
		t.Errorf("parsed %+v %v", v, err)
	}
	for _, bad := range []NetworkSpec{{Name: "a", Mode: "hostdev"}, {Name: "a", Mode: "hostdev", PF: "ens1f0; reboot"}, {Name: "a", Mode: "hostdev", PF: "ens1f0", VLAN: 5000}} {
		if _, err := buildNetwork(bad); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}
