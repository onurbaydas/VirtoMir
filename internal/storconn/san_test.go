package storconn

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Two FC paths to one LUN held by multipath, an empty single-path FC disk, an FC disk with
// LVM on it, and the system disk.
const sanLsblk = `{"blockdevices":[
 {"name":"sda","path":"/dev/sda","size":512110190592,"type":"disk","tran":"sata","wwn":"0x5000","pttype":"gpt","children":[{"name":"sda1","path":"/dev/sda1","size":512110190592,"type":"part","mountpoint":"/"}]},
 {"name":"sdb","path":"/dev/sdb","size":107374182400,"type":"disk","tran":"fc","wwn":"0x600a","children":[{"name":"mpatha","path":"/dev/mapper/mpatha","size":107374182400,"type":"mpath"}]},
 {"name":"sdc","path":"/dev/sdc","size":107374182400,"type":"disk","tran":"fc","wwn":"0x600a","children":[{"name":"mpatha","path":"/dev/mapper/mpatha","size":107374182400,"type":"mpath"}]},
 {"name":"sdd","path":"/dev/sdd","size":53687091200,"type":"disk","tran":"fc","wwn":"0x600b"},
 {"name":"sde","path":"/dev/sde","size":53687091200,"type":"disk","tran":"fc","wwn":"0x600c","fstype":"LVM2_member"}
]}`

func fakeSys(t *testing.T) {
	t.Helper()
	old := SysRoot
	SysRoot = t.TempDir()
	t.Cleanup(func() { SysRoot = old })
	for host, f := range map[string]map[string]string{
		"host3": {"port_name": "0x21000024ff3a1b2c", "port_state": "Online", "speed": "16 Gbit"},
		"host4": {"port_name": "0x21000024ff3a1b2d", "port_state": "Linkdown", "speed": "unknown"},
	} {
		dir := filepath.Join(SysRoot, "class", "fc_host", host)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for k, v := range f {
			_ = os.WriteFile(filepath.Join(dir, k), []byte(v+"\n"), 0o644)
		}
		_ = os.MkdirAll(filepath.Join(SysRoot, "class", "scsi_host", host), 0o755)
	}
}

func TestAdaptersAndRescan(t *testing.T) {
	fakeSys(t)
	useLookPath(t, have())
	as := Adapters()
	if len(as) != 2 || as[0].Host != "host3" || as[0].WWPN != "0x21000024ff3a1b2c" || as[0].State != "Online" || as[1].State != "Linkdown" {
		t.Fatalf("adapters %+v", as)
	}
	Rescan(context.Background(), &fakeRunner{})
	if b, _ := os.ReadFile(filepath.Join(SysRoot, "class", "scsi_host", "host3", "scan")); string(b) != "- - -" {
		t.Errorf("host3 not scanned: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(SysRoot, "class", "fc_host", "host4", "issue_lip")); string(b) != "1" {
		t.Errorf("no loop initialization on host4: %q", b)
	}
}

func TestLUNs(t *testing.T) {
	r := &fakeRunner{out: map[string]string{"lsblk": sanLsblk}}
	luns, err := LUNs(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	want := []LUN{
		{Path: "/dev/mapper/mpatha", SizeBytes: 100 << 30, Tran: "fc", Paths: 2, Empty: true},
		{Path: "/dev/sdd", SizeBytes: 50 << 30, Tran: "fc", Paths: 1, Empty: true},
		{Path: "/dev/sde", SizeBytes: 50 << 30, Tran: "fc", Paths: 1, Empty: false},
	}
	if len(luns) != len(want) {
		t.Fatalf("luns %+v", luns)
	}
	for i := range want {
		if luns[i] != want[i] {
			t.Errorf("%d: %+v, want %+v", i, luns[i], want[i])
		}
	}
}

func TestMultipathTurnsOnOnlyForASecondPath(t *testing.T) {
	two := `{"blockdevices":[{"name":"sdb","path":"/dev/sdb","size":1,"type":"disk","tran":"fc","wwn":"0x600a"},{"name":"sdc","path":"/dev/sdc","size":1,"type":"disk","tran":"fc","wwn":"0x600a"}]}`
	useLookPath(t, have("multipath", "mpathconf"))
	r := &fakeRunner{out: map[string]string{"lsblk": two}}
	on, err := EnableMultipath(context.Background(), r)
	if err != nil || !on || !r.called("mpathconf --enable --with_multipathd y") {
		t.Errorf("two paths: %v %v %q", on, err, r.calls)
	}
	r = &fakeRunner{out: map[string]string{"lsblk": sanLsblk}} // already held by multipath
	if on, _ := EnableMultipath(context.Background(), r); on || r.called("mpathconf") {
		t.Errorf("multipath already on: %q", r.calls)
	}
}

func TestSANFound(t *testing.T) {
	fakeSys(t)
	useLookPath(t, have("multipath", "mpathconf"))
	r := &fakeRunner{out: map[string]string{"lsblk": sanLsblk}}
	got := SANFound(context.Background(), r, func(p string) bool { return p == "/dev/sdd" })
	var names []string
	for _, f := range got {
		names = append(names, f.Kind+" "+f.Name+" "+f.Detail+" "+f.Error)
	}
	want := []string{
		"fc 0x21000024ff3a1b2c host3 · Online · 16 Gbit ",
		"fc 0x21000024ff3a1b2d host4 · Linkdown · unknown the port is Linkdown: check its cable and the switch port",
		"fc /dev/mapper/mpatha 2 paths ",
	}
	if strings.Join(names, "\n") != strings.Join(want, "\n") {
		t.Errorf("found:\n%s\nwant:\n%s", strings.Join(names, "\n"), strings.Join(want, "\n"))
	}
	if !got[0].Adapter || got[2].Adapter || got[2].SizeBytes != 100<<30 {
		t.Errorf("found %+v", got)
	}
}

func TestPoolNameOfALUN(t *testing.T) {
	if n := PoolName(Found{Kind: KindFC, Name: "/dev/mapper/mpatha"}, func(s string) bool { return s == "fc-mpatha" }); n != "fc-mpatha-2" {
		t.Errorf("fc: %s", n)
	}
}
