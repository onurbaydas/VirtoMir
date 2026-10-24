package physnet

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type fakeRunner struct {
	calls   []string
	answers map[string]string
	missing bool
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, line)
	if f.missing {
		return nil, exec.ErrNotFound
	}
	best, out := "", ""
	for k, v := range f.answers {
		if strings.HasPrefix(line, k) && len(k) > len(best) {
			best, out = k, v
		}
	}
	return []byte(out), nil
}

// sysfs builds a host with one SR-IOV NIC (ens1f0, 2 of 8 VFs) and a plain one (eno1).
func sysfs(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("sysfs has colons and symlinks")
	}
	root := t.TempDir()
	pf := filepath.Join(root, "sys/devices/pci0000:00/0000:04:00.0")
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, s string) { must(os.MkdirAll(filepath.Dir(p), 0o755)); must(os.WriteFile(p, []byte(s), 0o644)) }
	write(filepath.Join(pf, "sriov_totalvfs"), "8\n")
	write(filepath.Join(pf, "sriov_numvfs"), "2\n")
	must(os.MkdirAll(filepath.Join(root, "sys/bus/pci/drivers/ixgbe"), 0o755))
	must(os.Symlink(filepath.Join(root, "sys/bus/pci/drivers/ixgbe"), filepath.Join(pf, "driver")))
	for i, addr := range []string{"0000:04:10.0", "0000:04:10.2"} {
		vf := filepath.Join(root, "sys/devices/pci0000:00", addr)
		must(os.MkdirAll(vf, 0o755))
		must(os.Symlink(vf, filepath.Join(pf, "virtfn"+string(rune('0'+i)))))
		if i == 0 {
			write(filepath.Join(vf, "net/ens1f0v0/address"), "52:54:00:aa:00:01\n")
		}
	}
	must(os.MkdirAll(filepath.Join(root, "sys/class/net/ens1f0"), 0o755))
	must(os.Symlink(pf, filepath.Join(root, "sys/class/net/ens1f0/device")))
	plain := filepath.Join(root, "sys/devices/pci0000:00/0000:00:1f.6")
	must(os.MkdirAll(plain, 0o755))
	must(os.MkdirAll(filepath.Join(root, "sys/class/net/eno1"), 0o755))
	must(os.Symlink(plain, filepath.Join(root, "sys/class/net/eno1/device")))
	must(os.MkdirAll(filepath.Join(root, "sys/class/net/lo"), 0o755))
	return root
}

func TestSRIOV(t *testing.T) {
	root := sysfs(t)
	s := SRIOV{Root: root, RulesDir: filepath.Join(root, "etc/udev/rules.d")}
	devs, err := s.Devices()
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 1 {
		t.Fatalf("devices %+v", devs)
	}
	d := devs[0]
	if d.Interface != "ens1f0" || d.PCI != "0000:04:00.0" || d.Driver != "ixgbe" || d.TotalVFs != 8 || d.CurrentVFs != 2 || len(d.VFs) != 2 ||
		d.VFs[0].PCI != "0000:04:10.0" || d.VFs[0].Netdev != "ens1f0v0" || d.VFs[0].MAC != "52:54:00:aa:00:01" || d.VFs[1].Netdev != "" || d.Persistent {
		t.Errorf("device %+v", d)
	}
	if err := s.SetVFs("ens1f0", 4, true); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(filepath.Join(root, "sys/devices/pci0000:00/0000:04:00.0/sriov_numvfs")); string(raw) != "4" {
		t.Errorf("numvfs %q", raw)
	}
	rule, _ := os.ReadFile(s.ruleFile("ens1f0"))
	if !strings.Contains(string(rule), `KERNEL=="ens1f0", ATTR{device/sriov_numvfs}="4"`) {
		t.Errorf("rule %q", rule)
	}
	if devs, _ := s.Devices(); !devs[0].Persistent {
		t.Error("the rule is not seen")
	}
	if err := s.SetVFs("ens1f0", 0, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.ruleFile("ens1f0")); !errors.Is(err, os.ErrNotExist) {
		t.Error("no VFs, but the rule stays")
	}
	if err := s.SetVFs("ens1f0", 9, false); !errors.Is(err, ErrInvalid) {
		t.Errorf("too many: %v", err)
	}
	if err := s.SetVFs("eno1", 2, false); !errors.Is(err, ErrNotFound) {
		t.Errorf("no SR-IOV: %v", err)
	}
	if err := s.SetVFs("../../etc", 1, false); !errors.Is(err, ErrInvalid) {
		t.Errorf("path in a name: %v", err)
	}
}

func bgpConfig() BGPConfig {
	return BGPConfig{Enabled: true, LocalAS: 65011, RouterID: "10.0.0.11", EVPN: true,
		Neighbors: []BGPNeighbor{{Address: "10.0.0.1", RemoteAS: 65000, Description: "leaf1 eth49", BFD: true}, {Address: "10.0.0.2", RemoteAS: 65000, Password: "s3cret!"}},
		Announce:  []string{"10.20.0.0/24", "fd00:20::/64"}}
}

func TestFRRConfig(t *testing.T) {
	got, err := FRRConfig(bgpConfig())
	if err != nil {
		t.Fatal(err)
	}
	want := `router bgp 65011
 bgp router-id 10.0.0.11
 no bgp ebgp-requires-policy
 no bgp default ipv4-unicast
 neighbor 10.0.0.1 remote-as 65000
 neighbor 10.0.0.1 description leaf1 eth49
 neighbor 10.0.0.1 bfd
 neighbor 10.0.0.2 remote-as 65000
 neighbor 10.0.0.2 password s3cret!
 !
 address-family ipv4 unicast
  network 10.20.0.0/24
  neighbor 10.0.0.1 activate
  neighbor 10.0.0.2 activate
 exit-address-family
 !
 address-family ipv6 unicast
  network fd00:20::/64
 exit-address-family
 !
 address-family l2vpn evpn
  neighbor 10.0.0.1 activate
  neighbor 10.0.0.2 activate
  advertise-all-vni
 exit-address-family
exit
`
	if got != want {
		t.Errorf("got\n%s", got)
	}
	bad := []func(*BGPConfig){
		func(c *BGPConfig) { c.LocalAS = 0 },
		func(c *BGPConfig) { c.RouterID = "fd00::1" },
		func(c *BGPConfig) { c.Neighbors[0].Address = "leaf1" },
		func(c *BGPConfig) { c.Neighbors[1].Address = "10.0.0.1" },
		func(c *BGPConfig) { c.Neighbors[0].Description = "x\n router bgp 1" },
		func(c *BGPConfig) { c.Neighbors[1].Password = "with space" },
		func(c *BGPConfig) { c.Announce = []string{"10.20.0.1/24"} },
		func(c *BGPConfig) { c.Neighbors = nil },
	}
	for i, f := range bad {
		c := bgpConfig()
		f(&c)
		if _, err := FRRConfig(c); !errors.Is(err, ErrInvalid) {
			t.Errorf("case %d accepted", i)
		}
	}
}

func TestApplyBGP(t *testing.T) {
	old := FRRFile
	FRRFile = filepath.Join(t.TempDir(), "virtomir-bgp.conf")
	defer func() { FRRFile = old }()
	f := &fakeRunner{answers: map[string]string{
		"vtysh -c show version": "FRRouting 10.2.1 (virtomir-dev) on Linux(6.12.43).\nCopyright 1996-2005 Kunihiro Ishiguro, et al.\n",
		"vtysh -c show daemons": " mgmtd zebra bgpd staticd watchfrr\n",
	}}
	ctx := context.Background()
	c, err := ApplyBGP(ctx, f, bgpConfig())
	if err != nil {
		t.Fatal(err)
	}
	if c.AppliedAS != 65011 || !strings.Contains(strings.Join(f.calls, "\n"), "vtysh -f "+FRRFile+"\nvtysh -c write memory") {
		t.Errorf("apply %v %v", c.AppliedAS, f.calls)
	}
	if raw, _ := os.ReadFile(FRRFile); !strings.Contains(string(raw), "router bgp 65011") {
		t.Errorf("file %q", raw)
	}
	f.calls = nil
	c.LocalAS = 65012
	c, _ = ApplyBGP(ctx, f, c)
	if f.calls[2] != "vtysh -c configure terminal -c no router bgp 65011" || c.AppliedAS != 65012 {
		t.Errorf("the old section must go first: %v", f.calls)
	}
	f.calls = nil
	c.Enabled = false
	c, _ = ApplyBGP(ctx, f, c)
	if strings.Join(f.calls, "\n") != "vtysh -c configure terminal -c no router bgp 65012\nvtysh -c write memory" || c.AppliedAS != 0 {
		t.Errorf("disable %v", f.calls)
	}
	f.answers["vtysh -c show daemons"] = " zebra staticd\n"
	if _, err := ApplyBGP(ctx, f, bgpConfig()); err == nil || !strings.Contains(err.Error(), "bgpd=yes") {
		t.Errorf("without bgpd: %v", err)
	}
	if st := DetectFRR(ctx, &fakeRunner{missing: true}); st.Installed {
		t.Error("FRR found without vtysh")
	}
}

func TestSessions(t *testing.T) {
	raw, _ := os.ReadFile(filepath.Join("testdata", "bgp_summary.json"))
	f := &fakeRunner{answers: map[string]string{"vtysh -c show bgp summary json": string(raw)}}
	list, err := Sessions(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("%+v", list)
	}
	a, b := list[0], list[1]
	if a.Address != "10.0.0.1" || a.State != "Established" || a.Received != 16 || a.Sent != 5 || strings.Join(a.Families, ",") != "ipv4Unicast,l2VpnEvpn" || a.Description != "leaf1 eth49" {
		t.Errorf("leaf1 %+v", a)
	}
	if b.State != "Active" || b.Uptime != "never" || len(b.Families) != 1 {
		t.Errorf("leaf2 %+v", b)
	}
}

func pool() IPPool {
	return IPPool{ID: "prod-v20", Name: "Prod VLAN 20", Network: "vlan20", CIDR: "10.20.0.0/24", Gateway: "10.20.0.1",
		RangeStart: "10.20.0.1", RangeEnd: "10.20.0.6", Reserved: []string{"10.20.0.3"}, DNSServers: []string{"10.20.0.53"}, Domain: "prod.example"}
}

func TestIPPools(t *testing.T) {
	path := filepath.Join(t.TempDir(), "physnet.json")
	s, _ := Open(path)
	s.now = func() time.Time { return time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC) }
	if _, err := s.CreatePool(pool()); err != nil {
		t.Fatal(err)
	}
	for i, f := range []func(*IPPool){
		func(p *IPPool) { p.ID = "Prod" },
		func(p *IPPool) { p.CIDR = "10.20.0.5/24" },
		func(p *IPPool) { p.RangeEnd = "10.21.0.9" },
		func(p *IPPool) { p.RangeStart, p.RangeEnd = "10.20.0.9", "10.20.0.2" },
		func(p *IPPool) { p.Gateway = "192.168.1.1" },
		func(p *IPPool) { p.DNSServers = []string{"dns1"} },
		func(p *IPPool) { p.Domain = "-bad-" },
	} {
		p := pool()
		p.ID = "other"
		f(&p)
		if _, err := s.CreatePool(p); !errors.Is(err, ErrInvalid) {
			t.Errorf("case %d: %v", i, err)
		}
	}
	over := pool()
	over.ID, over.RangeStart, over.RangeEnd = "other", "10.20.0.5", "10.20.0.9"
	if _, err := s.CreatePool(over); !errors.Is(err, ErrConflict) {
		t.Errorf("overlap: %v", err)
	}

	alloc := func(vm, want string) (Allocation, error) {
		return s.Allocate("prod-v20", Allocation{VMUUID: vm, VMName: "vm-" + vm, MAC: "52:54:00:00:00:" + vm}, want)
	}
	a, _ := alloc("01", "")
	b, _ := alloc("02", "")
	if a.IP != "10.20.0.2" || b.IP != "10.20.0.4" {
		t.Errorf("first free skips the gateway and reserved: %s %s", a.IP, b.IP)
	}
	if again, _ := alloc("01", ""); again.IP != "10.20.0.2" {
		t.Errorf("a machine keeps its address: %s", again.IP)
	}
	if _, err := alloc("03", "10.20.0.4"); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "vm-02") {
		t.Errorf("taken: %v", err)
	}
	if _, err := alloc("03", "10.20.0.3"); !errors.Is(err, ErrConflict) {
		t.Errorf("reserved: %v", err)
	}
	if c, err := alloc("03", "10.20.0.6"); err != nil || c.IP != "10.20.0.6" {
		t.Errorf("wanted: %v %v", c, err)
	}
	_, _ = alloc("04", "")
	if _, err := alloc("05", ""); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "no free address") {
		t.Errorf("exhausted: %v", err)
	}
	if why, _ := s.Check("prod-v20", "10.20.0.5"); !strings.Contains(why, "held by vm-04") {
		t.Errorf("check %q", why)
	}
	if err := s.DeletePool("prod-v20"); !errors.Is(err, ErrBusy) {
		t.Errorf("delete with allocations: %v", err)
	}
	cut := pool()
	cut.RangeEnd = "10.20.0.4"
	if _, err := s.UpdatePool(cut); !errors.Is(err, ErrBusy) {
		t.Errorf("update cutting allocations: %v", err)
	}
	rel, _ := s.ReleaseVM("03")
	if len(rel) != 1 || rel[0].IP != "10.20.0.6" || rel[0].Pool != "prod-v20" {
		t.Errorf("release vm %+v", rel)
	}
	if err := s.Release("prod-v20", "10.20.0.5"); err != nil {
		t.Fatal(err)
	}
	again, _ := Open(path)
	p, _ := again.Pool("prod-v20")
	if len(p.Allocations) != 2 || p.Allocations[0].IP != "10.20.0.2" || p.Allocations[1].IP != "10.20.0.4" {
		t.Errorf("reloaded %+v", p.Allocations)
	}
	other, _ := Open("")
	if ok, err := other.Adopt(again.Shared()); !ok || err != nil || len(other.Pools()) != 1 {
		t.Errorf("adopt %v %v", ok, err)
	}
	if ok, _ := other.Adopt(Shared{Version: 1}); ok {
		t.Error("older adopted")
	}
}

func TestReservationsAreNotFree(t *testing.T) {
	s, _ := Open("")
	_, _ = s.CreatePool(pool())
	if _, err := s.Allocate("prod-v20", Allocation{VMName: "printer"}, "10.20.0.2"); err != nil {
		t.Fatal(err)
	}
	if why, _ := s.Check("prod-v20", "10.20.0.2"); !strings.Contains(why, "printer") {
		t.Errorf("check %q", why)
	}
	if _, err := s.Allocate("prod-v20", Allocation{VMName: "scanner"}, "10.20.0.2"); !errors.Is(err, ErrConflict) {
		t.Errorf("second reservation of one address: %v", err)
	}
	if a, _ := s.Allocate("prod-v20", Allocation{VMUUID: "u1"}, ""); a.IP == "10.20.0.2" {
		t.Error("a machine got a reserved address")
	}
}
