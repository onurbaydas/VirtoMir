package hostnet

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/onurbaydas/virtomir/internal/hostinfo"
)

const cloudInit = `# written by cloud-init
network:
  version: 2
  ethernets:
    eth0:
      match:
        macaddress: "00:15:5d:01:68:00"
      set-name: eth0
      addresses:
        - 192.168.77.10/24
      routes:
        - to: default
          via: 192.168.77.1
      nameservers:
        addresses: [1.1.1.1]
`

type fakeHost struct {
	mu      sync.Mutex
	ran     []string
	fail    string // a command prefix that fails
	active  string // the active systemd unit
	outputs map[string]string
}

func (f *fakeHost) run(_ context.Context, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	line := strings.Join(args, " ")
	if strings.HasPrefix(line, "systemctl is-active ") {
		if strings.HasSuffix(line, f.active) {
			return []byte("active\n"), nil
		}
		return []byte("inactive\n"), errors.New("inactive")
	}
	f.ran = append(f.ran, line)
	if f.fail != "" && strings.HasPrefix(line, f.fail) {
		return nil, errors.New("boom")
	}
	return []byte(f.outputs[line]), nil
}

func (f *fakeHost) commands() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.ran, "\n")
}

func newTestManager(t *testing.T, f *fakeHost, look ...string) *Manager {
	t.Helper()
	root := t.TempDir()
	write(t, root, "proc/net/route", "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\nEth0\t00000000\t014DA8C0\t0003\t0\t0\t0\t00000000\neth0\t00000000\t014DA8C0\t0003\t0\t0\t0\t00000000\n")
	return &Manager{Root: root, StateDir: filepath.Join(root, "state"), Run: f.run, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Window: time.Minute,
		LookPath: func(s string) (string, error) {
			for _, l := range look {
				if l == s {
					return "/usr/bin/" + s, nil
				}
			}
			return "", errors.New("not found")
		},
		Interfaces: func() []hostinfo.Interface {
			return []hostinfo.Interface{
				{Name: "lo", Kind: "loopback"},
				{Name: "eth0", Kind: "physical", MAC: "00:15:5d:01:68:00", Addresses: []string{"192.168.77.10/24", "fe80::1/64"}},
				{Name: "eth1", Kind: "physical", Addresses: []string{"fe80::2/64"}},
				{Name: "eth2", Kind: "physical", Master: "br9"},
				{Name: "wlan0", Kind: "physical", Wireless: true},
				{Name: "virbr0", Kind: "bridge"},
				{Name: "vnet3", Kind: "tap"},
			}
		}}
}

func write(t *testing.T, root, rel, data string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func TestValidate(t *testing.T) {
	f := &fakeHost{active: "systemd-networkd"}
	m := newTestManager(t, f, "networkctl")
	for _, s := range []Spec{
		{Kind: "bridge", Name: "br0", Ports: []string{"eth0"}},                          // uplink without its address
		{Kind: "bridge", Name: "eth1", Ports: []string{}},                               // exists
		{Kind: "bridge", Name: "br0", Ports: []string{"eth2"}},                          // already in br9
		{Kind: "bridge", Name: "br0", Ports: []string{"vnet3"}},                         // a machine's tap
		{Kind: "bridge", Name: "br0", Ports: []string{"virbr0"}},                        // libvirt's
		{Kind: "bridge", Name: "br0", Ports: []string{"eth1"}, TakeAddressFrom: "eth0"}, // not a port
		{Kind: "bridge", Name: "br0", Ports: []string{"eth1", "eth1"}},
		{Kind: "bridge", Name: "br0", Ports: []string{"wlan0"}}, // Wi-Fi cannot be a bridge port
		{Kind: "bond", Name: "bond0"},
		{Kind: "bond", Name: "bond0", Ports: []string{"eth1"}, BondMode: "fastest"},
		{Kind: "vlan", Name: "eth1.5000", Parent: "eth1", VLAN: 5000},
		{Kind: "vlan", Name: "v1", Parent: "nope", VLAN: 10},
		{Kind: "bridge", Name: "-x"},
		{Kind: "tunnel", Name: "t0"},
	} {
		if _, err := m.validate(&s); err == nil {
			t.Errorf("accepted %+v", s)
		}
	}
	ok := Spec{Kind: "bond", Name: "bond0", Ports: []string{"eth1"}}
	if _, err := m.validate(&ok); err != nil || ok.BondMode != "active-backup" {
		t.Errorf("bond: %v %+v", err, ok)
	}
	ok = Spec{Kind: "bridge", Name: "br0", Ports: []string{"eth0", "eth1"}, TakeAddressFrom: "eth0"}
	if _, err := m.validate(&ok); err != nil {
		t.Errorf("bridge taking the uplink's address: %v", err)
	}
}

func TestNetplanBridgeConfirmAndRemove(t *testing.T) {
	f := &fakeHost{}
	m := newTestManager(t, f, "netplan")
	write(t, m.Root, "etc/netplan/50-cloud-init.yaml", cloudInit)
	write(t, m.Root, "etc/cloud/cloud.cfg.d/README", "")

	p, err := m.Create(context.Background(), Spec{Kind: "bridge", Name: "br0", Ports: []string{"eth0"}, TakeAddressFrom: "eth0"})
	if err != nil {
		t.Fatal(err)
	}
	ci := read(t, m.Root, "etc/netplan/50-cloud-init.yaml")
	if strings.Contains(ci, "192.168.77.10") || !strings.Contains(ci, "dhcp4: false") || !strings.Contains(ci, "macaddress") || !strings.Contains(ci, "# written by cloud-init") {
		t.Errorf("cloud-init file:\n%s", ci)
	}
	own := read(t, m.Root, netplanOwn)
	for _, want := range []string{"bridges:", "br0:", "interfaces: [eth0]", "stp: false", "192.168.77.10/24", "via: 192.168.77.1", "1.1.1.1", "macaddress: 00:15:5d:01:68:00"} {
		if !strings.Contains(own, want) {
			t.Errorf("own file lacks %q:\n%s", want, own)
		}
	}
	if !strings.Contains(read(t, m.Root, "etc/cloud/cloud.cfg.d/99-virtomir-network.cfg"), "config: disabled") {
		t.Error("cloud-init not told to keep off")
	}
	if !strings.Contains(f.commands(), "netplan generate\nnetplan apply") {
		t.Errorf("commands:\n%s", f.commands())
	}
	if st := m.Status(); st.Pending == nil || st.Pending.ID != p.ID || st.Backend != "netplan" {
		t.Errorf("status %+v", st)
	}
	if _, err := m.Create(context.Background(), Spec{Kind: "vlan", Name: "eth1.5", Parent: "eth1", VLAN: 5}); err == nil {
		t.Error("a second change while one is pending")
	}
	if err := m.Confirm(p.ID); err != nil {
		t.Fatal(err)
	}
	if recs := m.Records(); len(recs) != 1 || recs[0].Moved == "" {
		t.Fatalf("records %+v", recs)
	}

	// Removal gives the address back to eth0.
	p, err = m.Delete(context.Background(), "br0")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Confirm(p.ID); err != nil {
		t.Fatal(err)
	}
	ci = read(t, m.Root, "etc/netplan/50-cloud-init.yaml")
	if !strings.Contains(ci, "192.168.77.10/24") || strings.Contains(ci, "dhcp4: false") {
		t.Errorf("address not given back:\n%s", ci)
	}
	if read(t, m.Root, netplanOwn) != "<missing>" || len(m.Records()) != 0 {
		t.Errorf("br0 left behind: %s", read(t, m.Root, netplanOwn))
	}
}

func TestRollback(t *testing.T) {
	f := &fakeHost{}
	m := newTestManager(t, f, "netplan")
	write(t, m.Root, "etc/netplan/50-cloud-init.yaml", cloudInit)
	m.Window = 50 * time.Millisecond

	if _, err := m.Create(context.Background(), Spec{Kind: "bridge", Name: "br0", Ports: []string{"eth0"}, TakeAddressFrom: "eth0"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for m.Status().Pending != nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if m.Status().Pending != nil {
		t.Fatal("not rolled back")
	}
	if got := read(t, m.Root, "etc/netplan/50-cloud-init.yaml"); got != cloudInit {
		t.Errorf("file not restored:\n%s", got)
	}
	if read(t, m.Root, netplanOwn) != "<missing>" || len(m.Records()) != 0 {
		t.Error("own file or record left after rollback")
	}
	if !strings.HasSuffix(f.commands(), "ip link delete br0\nnetplan apply") {
		t.Errorf("rollback commands:\n%s", f.commands())
	}

	// A failing apply rolls back at once and says so.
	f.fail = "netplan apply"
	_, err := m.Create(context.Background(), Spec{Kind: "vlan", Name: "eth1.7", Parent: "eth1", VLAN: 7})
	if err == nil || !strings.Contains(err.Error(), "previous configuration was restored") {
		t.Errorf("err %v", err)
	}
	if read(t, m.Root, netplanOwn) != "<missing>" {
		t.Error("own file left after failed apply")
	}
}

func TestRecoverAfterRestart(t *testing.T) {
	f := &fakeHost{active: "systemd-networkd"}
	m := newTestManager(t, f, "networkctl")
	if _, err := m.Create(context.Background(), Spec{Kind: "bridge", Name: "br5", Ports: []string{"eth1"}}); err != nil {
		t.Fatal(err)
	}
	if read(t, m.Root, "etc/systemd/network/05-virtomir-br5-port-eth1.network") == "<missing>" {
		t.Fatal("port file not written")
	}
	// A new daemon finds the unconfirmed change and undoes it.
	m2 := newTestManager(t, f, "networkctl")
	m2.Root, m2.StateDir = m.Root, m.StateDir
	m2.Recover()
	for _, rel := range []string{"etc/systemd/network/05-virtomir-br5.netdev", "etc/systemd/network/05-virtomir-br5.network", "etc/systemd/network/05-virtomir-br5-port-eth1.network"} {
		if read(t, m.Root, rel) != "<missing>" {
			t.Errorf("%s survived the recovery", rel)
		}
	}
	if m2.Status().Pending != nil {
		t.Error("still pending")
	}
}

func TestNetworkdTakeAddress(t *testing.T) {
	f := &fakeHost{active: "systemd-networkd", outputs: map[string]string{
		"networkctl status --no-pager eth0": "● 2: eth0\n   Network File: /etc/systemd/network/10-eth0.network\n",
	}}
	m := newTestManager(t, f, "networkctl")
	write(t, m.Root, "etc/systemd/network/10-eth0.network", "[Match]\nName=eth0\n\n[Network]\nAddress=192.168.77.10/24\nGateway=192.168.77.1\nDNS=1.1.1.1\n\n[Route]\nDestination=10.0.0.0/8\nGateway=192.168.77.254\n")
	if _, err := m.Create(context.Background(), Spec{Kind: "bridge", Name: "br0", Ports: []string{"eth0"}, TakeAddressFrom: "eth0"}); err != nil {
		t.Fatal(err)
	}
	net := read(t, m.Root, "etc/systemd/network/05-virtomir-br0.network")
	if !strings.Contains(read(t, m.Root, "etc/systemd/network/05-virtomir-br0.netdev"), "MACAddress=00:15:5d:01:68:00") {
		t.Error("bridge must keep the port's MAC")
	}
	for _, want := range []string{"Name=br0", "Address=192.168.77.10/24", "Gateway=192.168.77.1", "[Route]\nDestination=10.0.0.0/8"} {
		if !strings.Contains(net, want) {
			t.Errorf("lacks %q:\n%s", want, net)
		}
	}
	if !strings.Contains(read(t, m.Root, "etc/systemd/network/05-virtomir-br0-port-eth0.network"), "Bridge=br0") {
		t.Error("port file")
	}
	if !strings.Contains(read(t, m.Root, "etc/systemd/network/10-eth0.network"), "Address=192.168.77.10/24") {
		t.Error("the host's own file must stay untouched")
	}
}

func TestNMBridge(t *testing.T) {
	f := &fakeHost{active: "NetworkManager", outputs: map[string]string{
		"nmcli -g GENERAL.CONNECTION device show eth0":                                      "Wired connection 1\n",
		"nmcli -g GENERAL.TYPE device show eth0":                                            "ethernet\n",
		"nmcli -g " + strings.Join(nmIPFields, ",") + " connection show Wired connection 1": "manual\n192.168.77.10/24\n192.168.77.1\n1.1.1.1\n\n\nauto\n\n\n\n\n\n",
	}}
	m := newTestManager(t, f, "nmcli")
	p, err := m.Create(context.Background(), Spec{Kind: "bridge", Name: "br0", Ports: []string{"eth0"}, TakeAddressFrom: "eth0"})
	if err != nil {
		t.Fatal(err)
	}
	cmds := f.commands()
	for _, want := range []string{
		"nmcli connection add type bridge con-name virtomir-br0 ifname br0 bridge.stp no",
		"ipv4.method manual ipv4.addresses 192.168.77.10/24 ipv4.gateway 192.168.77.1 ipv4.dns 1.1.1.1 ipv6.method auto bridge.mac-address 00:15:5d:01:68:00",
		"nmcli connection add type ethernet con-name virtomir-br0-eth0 ifname eth0 master br0 slave-type bridge",
		"nmcli connection up virtomir-br0\nnmcli connection up virtomir-br0-eth0",
	} {
		if !strings.Contains(cmds, want) {
			t.Errorf("lacks %q:\n%s", want, cmds)
		}
	}
	if err := m.Rollback(p.ID); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(f.commands(), "nmcli connection delete virtomir-br0-eth0\nnmcli connection delete virtomir-br0\nnmcli connection up Wired connection 1") {
		t.Errorf("rollback:\n%s", f.commands())
	}
}

func TestUnsupported(t *testing.T) {
	m := newTestManager(t, &fakeHost{active: "networking"})
	if st := m.Status(); st.Backend != "" || !strings.Contains(st.Reason, "ifupdown") {
		t.Errorf("status %+v", st)
	}
	if _, err := m.Create(context.Background(), Spec{Kind: "bridge", Name: "br0"}); !errors.Is(err, ErrRefused) {
		t.Errorf("err %v", err)
	}
}
