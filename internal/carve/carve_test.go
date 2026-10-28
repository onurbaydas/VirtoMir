package carve

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRunner struct {
	calls []string
	out   map[string]string // command name → output
	fail  map[string]bool   // command name → error
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if f.fail[name] {
		return nil, errors.New(name + " failed")
	}
	return []byte(f.out[name]), nil
}

func useTempDirs(t *testing.T) {
	t.Helper()
	oldBase, oldUnit := Base, UnitDir
	Base, UnitDir = filepath.Join(t.TempDir(), "srv"), t.TempDir()
	t.Cleanup(func() { Base, UnitDir = oldBase, oldUnit })
}

func TestSources(t *testing.T) {
	r := &fakeRunner{out: map[string]string{
		"vgs": `{"report":[{"vg":[{"vg_name":"data","vg_size":"500000000000","vg_free":"200000000000"},{"vg_name":"full","vg_size":"100000000000","vg_free":"0"}]}]}`,
		"zfs": "tank\t1000\t3000000000\nsmall\t10\t100\n",
	}}
	got := Sources(context.Background(), r, []Disk{{Path: "/dev/sdb", Type: "ssd", SizeBytes: 960 << 30}, {Path: "/dev/sdc", SizeBytes: 1 << 20}})
	want := []Source{
		{Kind: KindDisk, ID: "/dev/sdb", SizeBytes: 960 << 30, FreeBytes: 960 << 30, Detail: "ssd"},
		{Kind: KindVG, ID: "data", SizeBytes: 500000000000, FreeBytes: 200000000000},
		{Kind: KindZFS, ID: "tank", SizeBytes: 3000001000, FreeBytes: 3000000000},
	}
	if len(got) != len(want) {
		t.Fatalf("sources = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("source %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if s := Sources(context.Background(), &fakeRunner{fail: map[string]bool{"vgs": true, "zfs": true}}, nil); len(s) != 0 {
		t.Errorf("no LVM, no ZFS, no disks: %+v", s)
	}
}

func TestCheck(t *testing.T) {
	src := []Source{{Kind: KindVG, ID: "data", FreeBytes: 10 << 30}}
	ok := Request{Name: "fast", Kind: KindVG, Source: "data", SizeBytes: 10 << 30}
	if err := Check(ok, src); err != nil {
		t.Fatal(err)
	}
	for name, req := range map[string]Request{
		"too big":      {Name: "fast", Kind: KindVG, Source: "data", SizeBytes: 11 << 30},
		"too small":    {Name: "fast", Kind: KindVG, Source: "data", SizeBytes: 1 << 20},
		"other source": {Name: "fast", Kind: KindVG, Source: "gone", SizeBytes: 2 << 30},
		"other kind":   {Name: "fast", Kind: KindZFS, Source: "data", SizeBytes: 2 << 30},
		"bad name":     {Name: "../etc", Kind: KindVG, Source: "data", SizeBytes: 2 << 30},
	} {
		if err := Check(req, src); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestCarveVG(t *testing.T) {
	useTempDirs(t)
	r := &fakeRunner{out: map[string]string{"blkid": "1234-abcd\n", "systemd-escape": "srv-virtomir-fast.mount\n"}}
	dir, err := Carve(context.Background(), r, Request{Name: "fast", Kind: KindVG, Source: "data", SizeBytes: 10<<30 + 123}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(Base, "fast") {
		t.Errorf("dir = %s", dir)
	}
	want := []string{
		"lvcreate -y -W y -n fast -L 10737418240b data", // rounded down to whole extents
		"mkfs.xfs -q /dev/data/fast",
		"blkid -s UUID -o value /dev/data/fast",
		"systemd-escape -p --suffix=mount " + dir,
		"systemctl daemon-reload",
		"systemctl enable --now srv-virtomir-fast.mount",
		"restorecon " + Base,
		"restorecon -R " + dir,
	}
	if strings.Join(r.calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(r.calls, "\n"), strings.Join(want, "\n"))
	}
	unit, err := os.ReadFile(filepath.Join(UnitDir, "srv-virtomir-fast.mount"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"What=/dev/disk/by-uuid/1234-abcd", "Where=" + dir, "Options=defaults,nofail", "WantedBy=local-fs.target"} {
		if !strings.Contains(string(unit), line+"\n") {
			t.Errorf("mount unit lacks %q:\n%s", line, unit)
		}
	}
}

func TestCarveDiskMakesAVolumeGroup(t *testing.T) {
	useTempDirs(t)
	r := &fakeRunner{out: map[string]string{"blkid": "u", "systemd-escape": "x.mount"}}
	if _, err := Carve(context.Background(), r, Request{Name: "fast", Kind: KindDisk, Source: "/dev/sdb", SizeBytes: 2 << 30}, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) < 4 || r.calls[0] != "pvcreate -y /dev/sdb" || r.calls[1] != "vgcreate virtomir-fast /dev/sdb" ||
		r.calls[3] != "lvcreate -y -W y -n fast -L 2147483648b virtomir-fast" {
		t.Errorf("calls = %q", r.calls)
	}
}

func TestCarveAllOfADisk(t *testing.T) {
	useTempDirs(t)
	// A 4 GiB disk: its volume group keeps one extent for metadata.
	r := &fakeRunner{out: map[string]string{"blkid": "u", "systemd-escape": "x.mount", "vgs": "  4290772992\n"}}
	if _, err := Carve(context.Background(), r, Request{Name: "all", Kind: KindDisk, Source: "/dev/sdb", SizeBytes: 4 << 30}, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if r.calls[3] != "lvcreate -y -W y -n all -L 4290772992b virtomir-all" {
		t.Errorf("calls = %q", r.calls)
	}
}

func TestCarveTakesBackWhatItMadeOnFailure(t *testing.T) {
	useTempDirs(t)
	r := &fakeRunner{fail: map[string]bool{"mkfs.xfs": true}}
	if _, err := Carve(context.Background(), r, Request{Name: "fast", Kind: KindDisk, Source: "/dev/sdb", SizeBytes: 2 << 30}, func(string) {}); err == nil {
		t.Fatal("no error")
	}
	n := len(r.calls)
	if got := strings.Join(r.calls[n-3:], "\n"); got != "lvremove -y /dev/virtomir-fast/fast\nvgremove -y virtomir-fast\npvremove -y /dev/sdb" {
		t.Errorf("undo:\n%s", got)
	}
}

func TestCarveZFS(t *testing.T) {
	useTempDirs(t)
	r := &fakeRunner{}
	dir, err := Carve(context.Background(), r, Request{Name: "vms", Kind: KindZFS, Source: "tank", SizeBytes: 50 << 30}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if r.calls[0] != "zfs create -o quota=53687091200 -o mountpoint="+dir+" tank/vms" {
		t.Errorf("calls = %q", r.calls)
	}
}

func TestCarveStopsAtTheFirstFailure(t *testing.T) {
	useTempDirs(t)
	r := &fakeRunner{fail: map[string]bool{"mkfs.xfs": true}}
	_, err := Carve(context.Background(), r, Request{Name: "fast", Kind: KindVG, Source: "data", SizeBytes: 2 << 30}, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "mkfs.xfs failed") {
		t.Fatalf("err = %v", err)
	}
	for _, c := range r.calls {
		if strings.HasPrefix(c, "blkid") || strings.HasPrefix(c, "systemctl") {
			t.Errorf("went on after the failure: %q", r.calls)
		}
	}
}

func TestCarveRefusesAnExistingDirectory(t *testing.T) {
	useTempDirs(t)
	if err := os.MkdirAll(filepath.Join(Base, "fast"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := &fakeRunner{}
	if _, err := Carve(context.Background(), r, Request{Name: "fast", Kind: KindVG, Source: "data", SizeBytes: 2 << 30}, func(string) {}); !errors.Is(err, ErrInvalid) || len(r.calls) != 0 {
		t.Errorf("err = %v, calls = %q", err, r.calls)
	}
}

func carvedUnit(t *testing.T, unit, dev string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(UnitDir, unit), []byte(mountUnit("u", filepath.Join(Base, "fast"), dev)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveGivesADiskBack(t *testing.T) {
	useTempDirs(t)
	carvedUnit(t, "srv-virtomir-fast.mount", "/dev/virtomir-fast/fast")
	if err := os.MkdirAll(filepath.Join(Base, "fast"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := &fakeRunner{out: map[string]string{"systemd-escape": "srv-virtomir-fast.mount\n", "vgs": "  0\n", "pvs": "  /dev/sdb\n"}}
	if err := Remove(context.Background(), r, "fast", func(string) {}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"systemd-escape -p --suffix=mount " + filepath.Join(Base, "fast"),
		"systemctl disable --now srv-virtomir-fast.mount",
		"systemctl daemon-reload",
		"lvremove -y /dev/virtomir-fast/fast",
		"vgs --noheadings -o lv_count virtomir-fast",
		"pvs --noheadings -o pv_name -S vg_name=virtomir-fast",
		"vgremove -y virtomir-fast",
		"pvremove -y /dev/sdb",
	}
	if strings.Join(r.calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(r.calls, "\n"), strings.Join(want, "\n"))
	}
	if _, err := os.Stat(filepath.Join(UnitDir, "srv-virtomir-fast.mount")); !os.IsNotExist(err) {
		t.Error("mount unit left behind")
	}
}

func TestRemoveKeepsAVolumeGroupItDidNotMake(t *testing.T) {
	useTempDirs(t)
	carvedUnit(t, "x.mount", "/dev/data/fast")
	r := &fakeRunner{out: map[string]string{"systemd-escape": "x.mount", "vgs": "  0\n"}}
	if err := Remove(context.Background(), r, "fast", func(string) {}); err != nil {
		t.Fatal(err)
	}
	if last := r.calls[len(r.calls)-1]; last != "lvremove -y /dev/data/fast" {
		t.Errorf("touched the volume group: %q", r.calls)
	}
}

func TestRemoveZFS(t *testing.T) {
	useTempDirs(t)
	r := &fakeRunner{out: map[string]string{"systemd-escape": "x.mount", "zfs": "tank\t/tank\ntank/fast\t" + filepath.Join(Base, "fast") + "\n"}}
	if err := Remove(context.Background(), r, "fast", func(string) {}); err != nil {
		t.Fatal(err)
	}
	if last := r.calls[len(r.calls)-1]; last != "zfs destroy tank/fast" {
		t.Errorf("calls = %q", r.calls)
	}
}

func TestRemoveRefuses(t *testing.T) {
	useTempDirs(t)
	r := &fakeRunner{out: map[string]string{"systemd-escape": "x.mount", "zfs": "tank\t/tank\n"}}
	if err := Remove(context.Background(), r, "fast", func(string) {}); !errors.Is(err, ErrNotCarved) {
		t.Errorf("a pool VirtoMir did not carve: %v", err)
	}
	carvedUnit(t, "x.mount", "/dev/data/fast")
	if err := os.MkdirAll(filepath.Join(Base, "fast"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(Base, "fast", "web.qcow2"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r.calls = nil
	if err := Remove(context.Background(), r, "fast", func(string) {}); !errors.Is(err, ErrInvalid) || len(r.calls) != 0 {
		t.Errorf("a pool with disks in it: %v, calls %q", err, r.calls)
	}
}
