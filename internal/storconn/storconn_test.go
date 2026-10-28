package storconn

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type fakeRunner struct {
	mu    sync.Mutex
	calls []string
	out   map[string]string // command line prefix → output
	fail  map[string]string // command line prefix → error text
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	line := name + " " + strings.Join(args, " ")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, line)
	best, out := "", ""
	for k, v := range f.out {
		if strings.HasPrefix(line, k) && len(k) > len(best) {
			best, out = k, v
		}
	}
	for k, v := range f.fail {
		if strings.HasPrefix(line, k) {
			return []byte(out), errors.New(v)
		}
	}
	return []byte(out), nil
}

func (f *fakeRunner) called(prefix string) bool {
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func have(cmds ...string) func(string) (string, error) {
	return func(name string) (string, error) {
		for _, c := range cmds {
			if c == name {
				return "/usr/bin/" + c, nil
			}
		}
		return "", errors.New("not found")
	}
}

func useLookPath(t *testing.T, f func(string) (string, error)) {
	old := LookPath
	LookPath = f
	t.Cleanup(func() { LookPath = old })
}

func TestParsers(t *testing.T) {
	ts := parseSendTargets("10.0.0.5", "10.0.0.5:3260,1 iqn.2026-01.lab:vms\n10.0.1.5:3260,2 iqn.2026-01.lab:vms\n10.0.0.5:3260,1 iqn.2026-01.lab:iso\n")
	if len(ts) != 2 || ts[0].Name != "iqn.2026-01.lab:vms" || strings.Join(ts[0].Portals, " ") != "10.0.0.5:3260 10.0.1.5:3260" {
		t.Errorf("sendtargets: %+v", ts)
	}
	ex := parseShowmount("10.0.0.6", "/srv/vms  10.0.0.0/24\n/export/iso *\n")
	if len(ex) != 2 || ex[0].Name != "/srv/vms" || ex[0].Detail != "10.0.0.0/24" {
		t.Errorf("showmount: %+v", ex)
	}
	sh := parseSMBList("10.0.0.7", "Disk|vms|VM images\nDisk|ADMIN$|Remote Admin\nIPC|IPC$|IPC Service\nPrinter|laser|\nDisk|iso|\n")
	if len(sh) != 2 || sh[0].Name != "vms" || sh[0].Detail != "VM images" || sh[1].Name != "iso" {
		t.Errorf("smb: %+v", sh)
	}
}

func TestDiscover(t *testing.T) {
	useLookPath(t, have("iscsiadm", "showmount", "mount.nfs", "smbclient", "mount.cifs"))
	r := &fakeRunner{
		out: map[string]string{
			"iscsiadm -m discovery":         "10.0.0.5:3260,1 iqn.2026-01.lab:vms\n",
			"smbclient -L //10.0.0.7 -g -N": "Disk|vms|\n",
		},
		fail: map[string]string{
			"showmount":                     "clnt_create: RPC: Program not registered",
			"smbclient -L //10.0.0.8 -g -N": "session setup failed: NT_STATUS_ACCESS_DENIED",
		},
	}
	got := Discover(context.Background(), r, map[string][]string{
		"10.0.0.5": {KindISCSI}, "10.0.0.6": {KindNFS}, "10.0.0.7": {KindSMB}, "10.0.0.8": {KindSMB},
	}, nil)
	want := []Found{
		{Kind: KindISCSI, Server: "10.0.0.5", Name: "iqn.2026-01.lab:vms", Portals: []string{"10.0.0.5:3260"}},
		{Kind: KindNFS, Server: "10.0.0.6", Name: "/", Detail: "NFSv4"},
		{Kind: KindSMB, Server: "10.0.0.7", Name: "vms"},
		{Kind: KindSMB, Server: "10.0.0.8", NeedsAuth: true},
	}
	if len(got) != len(want) {
		t.Fatalf("found %+v", got)
	}
	for i := range want {
		g := got[i]
		g.Host = ""
		if g.Kind != want[i].Kind || g.Server != want[i].Server || g.Name != want[i].Name || g.NeedsAuth != want[i].NeedsAuth ||
			g.Detail != want[i].Detail || strings.Join(g.Portals, ",") != strings.Join(want[i].Portals, ",") {
			t.Errorf("%d: %+v, want %+v", i, g, want[i])
		}
	}
	if r.called("systemd-run") {
		t.Error("installed packages that were there")
	}
}

func TestDiscoverWithCredentialsNeverPutsThePasswordOnTheCommandLine(t *testing.T) {
	useLookPath(t, have("smbclient", "mount.cifs"))
	r := &fakeRunner{out: map[string]string{"smbclient": "Disk|private|\n"}}
	got := Discover(context.Background(), r, map[string][]string{"10.0.0.8": {KindSMB}}, map[string]Credentials{"10.0.0.8": {Username: `LAB\onur`, Password: "s3cret"}})
	if len(got) != 1 || got[0].Name != "private" || !got[0].NeedsAuth {
		t.Fatalf("found %+v", got)
	}
	for _, c := range r.calls {
		if strings.Contains(c, "s3cret") {
			t.Errorf("password on a command line: %s", c)
		}
	}
	if !r.called("smbclient -L //10.0.0.8 -g -A ") {
		t.Errorf("calls %q", r.calls)
	}
}

func TestEnsureInstallsWhatIsMissing(t *testing.T) {
	useLookPath(t, have("dnf", "showmount"))
	r := &fakeRunner{}
	if err := Ensure(context.Background(), r, KindNFS); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 1 || r.calls[0] != "systemd-run --quiet --wait --collect --unit=virtomir-install-nfs dnf install -y -q nfs-utils" {
		t.Errorf("calls %q", r.calls)
	}
	useLookPath(t, have("apt-get"))
	r = &fakeRunner{}
	if err := Ensure(context.Background(), r, KindSMB); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 1 || r.calls[0] != "systemd-run --quiet --wait --collect --unit=virtomir-install-smb env DEBIAN_FRONTEND=noninteractive apt-get install -y -q smbclient cifs-utils" {
		t.Errorf("calls %q", r.calls)
	}
}

func TestProbe(t *testing.T) {
	old := Dial
	Dial = func(_ context.Context, addr string) bool {
		return addr == "10.0.0.5:3260" || addr == "10.0.0.7:445" || addr == "10.0.0.7:2049" || addr == "nas.lab:445"
	}
	t.Cleanup(func() { Dial = old })
	got := Probe(context.Background(), []netip.Prefix{netip.MustParsePrefix("10.0.0.0/24")}, "nas.lab")
	if len(got) != 3 || strings.Join(got["10.0.0.5"], ",") != "iscsi" || strings.Join(got["10.0.0.7"], ",") != "nfs,smb" || got["nas.lab"][0] != "smb" {
		t.Errorf("probe %v", got)
	}
}

func TestScanPrefix(t *testing.T) {
	if p := scanPrefix(netip.MustParseAddr("172.17.3.9"), 16); p.String() != "172.17.3.0/24" {
		t.Errorf("a /16 is scanned as %s", p)
	}
	if p := scanPrefix(netip.MustParseAddr("10.1.2.3"), 23); p.String() != "10.1.2.0/23" {
		t.Errorf("a /23: %s", p)
	}
	if n := len(addresses([]netip.Prefix{netip.MustParsePrefix("10.0.0.0/24")})); n != 254 {
		t.Errorf("a /24 has %d hosts", n)
	}
}

func TestPoolName(t *testing.T) {
	taken := map[string]bool{"smb-nas-vms": true}
	if n := PoolName(Found{Kind: KindSMB, Server: "10.0.0.7", Host: "nas.lab.local", Name: "vms"}, func(s string) bool { return taken[s] }); n != "smb-nas-vms-2" {
		t.Errorf("smb: %s", n)
	}
	if n := PoolName(Found{Kind: KindNFS, Server: "10.0.0.6", Name: "/srv/vms"}, func(string) bool { return false }); n != "nfs-10-0-0-6-srv-vms" {
		t.Errorf("nfs: %s", n)
	}
	if n := PoolName(Found{Kind: KindISCSI, Server: "10.0.0.5", Name: "iqn.2003-01.org.linux-iscsi.lab:sn.5b1e"}, func(string) bool { return false }); n != "iscsi-10-0-0-5-sn-5b1e" {
		t.Errorf("iscsi: %s", n)
	}
}

func TestMountSMB(t *testing.T) {
	oldBase, oldUnit, oldCred := Base, UnitDir, CredDir
	Base, UnitDir, CredDir = filepath.Join(t.TempDir(), "srv"), t.TempDir(), filepath.Join(t.TempDir(), "cred")
	t.Cleanup(func() { Base, UnitDir, CredDir = oldBase, oldUnit, oldCred })
	r := &fakeRunner{out: map[string]string{"systemd-escape": "srv-virtomir-smb.mount\n"}}
	dir, err := MountSMB(context.Background(), r, "smb", "10.0.0.7", "vms", Credentials{Username: "onur", Password: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	unit, _ := os.ReadFile(filepath.Join(UnitDir, "srv-virtomir-smb.mount"))
	for _, want := range []string{"What=//10.0.0.7/vms\n", "Where=" + dir + "\n", "Type=cifs\n", "Options=credentials=" + filepath.Join(CredDir, "smb") + ",", "nofail,_netdev\n"} {
		if !strings.Contains(string(unit), want) {
			t.Errorf("unit lacks %q:\n%s", want, unit)
		}
	}
	if strings.Contains(string(unit), "s3cret") {
		t.Error("password in the unit")
	}
	if st, err := os.Stat(filepath.Join(CredDir, "smb")); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("credentials file: %v %v", st.Mode(), err)
	}
	if !r.called("systemctl enable --now srv-virtomir-smb.mount") || !r.called("smbclient //10.0.0.7/vms -c exit -A ") {
		t.Errorf("calls %q", r.calls)
	}
	if err := UnmountSMB(context.Background(), r, "smb"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(CredDir, "smb")); !os.IsNotExist(err) {
		t.Error("password left behind")
	}
	if err := UnmountSMB(context.Background(), r, "smb"); !errors.Is(err, ErrNotSMB) {
		t.Errorf("second unmount: %v", err)
	}
}

func TestMountSMBAsGuestAndWithAWrongPassword(t *testing.T) {
	oldBase, oldUnit, oldCred := Base, UnitDir, CredDir
	Base, UnitDir, CredDir = filepath.Join(t.TempDir(), "srv"), t.TempDir(), filepath.Join(t.TempDir(), "cred")
	t.Cleanup(func() { Base, UnitDir, CredDir = oldBase, oldUnit, oldCred })
	r := &fakeRunner{out: map[string]string{"systemd-escape": "pub.mount\n"}}
	if _, err := MountSMB(context.Background(), r, "pub", "10.0.0.7", "public", Credentials{}); err != nil {
		t.Fatal(err)
	}
	unit, _ := os.ReadFile(filepath.Join(UnitDir, "pub.mount"))
	if !strings.Contains(string(unit), "Options=guest,") || !r.called("smbclient //10.0.0.7/public -c exit -N") {
		t.Errorf("guest: %s %q", unit, r.calls)
	}
	if _, err := os.Stat(filepath.Join(CredDir, "pub")); !os.IsNotExist(err) {
		t.Error("a credentials file for a guest")
	}
	r = &fakeRunner{out: map[string]string{"smbclient": "session setup failed: NT_STATUS_LOGON_FAILURE"}, fail: map[string]string{"smbclient": "exit status 1"}}
	if _, err := MountSMB(context.Background(), r, "priv", "10.0.0.7", "private", Credentials{Username: "onur", Password: "wrong"}); !errors.Is(err, ErrAuth) {
		t.Errorf("wrong password: %v", err)
	}
	if r.called("systemctl") {
		t.Error("mounted after a refused sign-in")
	}
}
