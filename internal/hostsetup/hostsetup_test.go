package hostsetup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fake records commands and answers from a table; on installs it makes the commands appear.
type fake struct {
	calls   []string
	answers map[string]string
	fail    map[string]error
	onPath  map[string]bool
	after   map[string][]string // a command that makes others appear (an install)
}

func (f *fake) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	c := strings.TrimSpace(name + " " + strings.Join(args, " "))
	f.calls = append(f.calls, c)
	for k, err := range f.fail {
		if strings.HasPrefix(c, k) {
			return nil, err
		}
	}
	for k, cmds := range f.after {
		if strings.HasPrefix(c, k) {
			for _, x := range cmds {
				f.onPath[x] = true
			}
		}
	}
	return []byte(f.answers[c]), nil
}

func newSetup(t *testing.T, onPath ...string) (*Setup, *fake) {
	f := &fake{answers: map[string]string{}, fail: map[string]error{}, onPath: map[string]bool{}, after: map[string][]string{}}
	for _, c := range onPath {
		f.onPath[c] = true
	}
	s := &Setup{Run: f, Root: t.TempDir(), Look: func(c string) (string, error) {
		if f.onPath[c] {
			return "/usr/bin/" + c, nil
		}
		return "", errors.New("not found")
	}}
	return s, f
}

func write(t *testing.T, s *Setup, p, body string) {
	t.Helper()
	full := s.path(p)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, s *Setup, p string) string {
	t.Helper()
	b, err := os.ReadFile(s.path(p))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestInstallWithApt(t *testing.T) {
	s, f := newSetup(t, "apt-get")
	f.after["env DEBIAN_FRONTEND=noninteractive apt-get install"] = []string{"restic"}
	var steps []string
	if err := s.Install(context.Background(), "offsite", func(m string) { steps = append(steps, m) }); err != nil {
		t.Fatal(err)
	}
	want := []string{"apt-get update -q", "env DEBIAN_FRONTEND=noninteractive apt-get install -y -q --no-install-recommends restic"}
	if !slices.Equal(f.calls, want) {
		t.Errorf("calls = %q, want %q", f.calls, want)
	}
	if len(steps) != 2 || !s.Installed("offsite") {
		t.Errorf("steps %q, installed %v", steps, s.Installed("offsite"))
	}
}

func TestInstallFRRTurnsOnBGP(t *testing.T) {
	s, f := newSetup(t, "dnf")
	f.after["dnf install -y -q frr"] = []string{"vtysh"}
	write(t, s, "/etc/frr/daemons", "zebra=yes\nbgpd=no\nospfd=no\n")
	if err := s.Install(context.Background(), "physnet", func(string) {}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, s, "/etc/frr/daemons"); got != "zebra=yes\nbgpd=yes\nospfd=no\n" {
		t.Errorf("daemons = %q", got)
	}
	if !slices.Contains(f.calls, "systemctl restart frr") {
		t.Errorf("FRR not restarted: %q", f.calls)
	}
}

func TestInstallRefusesWhatItCannot(t *testing.T) {
	s, _ := newSetup(t)
	if err := s.Install(context.Background(), "offsite", func(string) {}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("no package manager: %v", err)
	}
	if err := s.Install(context.Background(), "rm -rf", func(string) {}); !errors.Is(err, ErrInvalid) {
		t.Errorf("unknown feature: %v", err)
	}
	// Installed but the command still missing: say so, never "done".
	s2, _ := newSetup(t, "apt-get")
	if err := s2.Install(context.Background(), "offsite", func(string) {}); err == nil || !strings.Contains(err.Error(), "restic") {
		t.Errorf("missing command after install: %v", err)
	}
}

func TestEnableIOMMUOnDebianGrub(t *testing.T) {
	s, f := newSetup(t, "update-grub")
	write(t, s, "/proc/cpuinfo", "vendor_id : AuthenticAMD\n")
	write(t, s, "/proc/cmdline", "BOOT_IMAGE=/vmlinuz root=/dev/sda1 ro quiet\n")
	orig := "GRUB_DEFAULT=0\nGRUB_CMDLINE_LINUX_DEFAULT=\"quiet\"\nGRUB_CMDLINE_LINUX=\"\"\n"
	write(t, s, "/etc/default/grub", orig)
	res, err := s.EnableIOMMU(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, s, "/etc/default/grub"); !strings.Contains(got, `GRUB_CMDLINE_LINUX_DEFAULT="quiet amd_iommu=on iommu=pt"`) || !strings.Contains(got, `GRUB_CMDLINE_LINUX=""`) {
		t.Errorf("grub = %q", got)
	}
	if read(t, s, "/etc/default/grub.virtomir-backup") != orig || !res.RebootNeeded || res.Method != "grub" || !slices.Contains(f.calls, "update-grub") {
		t.Errorf("result %+v, calls %q", res, f.calls)
	}
	// A second run changes nothing.
	f.calls = nil
	if res, err = s.EnableIOMMU(context.Background()); err != nil || len(res.Params) != 0 || len(f.calls) != 0 {
		t.Errorf("second run: %+v %v %q", res, err, f.calls)
	}
}

func TestEnableIOMMUPutsGrubBackWhenItCannotApply(t *testing.T) {
	s, _ := newSetup(t, "update-grub")
	s.Run.(*fake).fail["update-grub"] = errors.New("update-grub: boom")
	orig := "GRUB_CMDLINE_LINUX_DEFAULT=\"quiet\"\n"
	write(t, s, "/etc/default/grub", orig)
	if _, err := s.EnableIOMMU(context.Background()); err == nil {
		t.Fatal("want the error")
	}
	if got := read(t, s, "/etc/default/grub"); got != orig {
		t.Errorf("grub not restored: %q", got)
	}
}

func TestEnableIOMMUWithGrubby(t *testing.T) {
	s, f := newSetup(t, "grubby")
	write(t, s, "/proc/cpuinfo", "vendor_id : GenuineIntel\n")
	f.answers["grubby --info=DEFAULT"] = "index=0\nkernel=\"/boot/vmlinuz\"\nargs=\"ro rhgb quiet iommu=pt\"\n"
	res, err := s.EnableIOMMU(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Params, []string{"intel_iommu=on"}) || !slices.Contains(f.calls, "grubby --update-kernel=ALL --args=intel_iommu=on") {
		t.Errorf("result %+v, calls %q", res, f.calls)
	}
}

// Fedora's kernel turns the IOMMU on with nothing on the command line: leave the boot alone.
func TestEnableIOMMUAlreadyActive(t *testing.T) {
	s, f := newSetup(t, "grubby")
	write(t, s, "/sys/class/iommu/dmar0/uevent", "")
	write(t, s, "/proc/cmdline", "ro rhgb quiet\n")
	res, err := s.EnableIOMMU(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Method != "active" || res.RebootNeeded || len(res.Params) > 0 || len(f.calls) > 0 {
		t.Errorf("result %+v, calls %q", res, f.calls)
	}
}

func TestEnableIOMMUUnknownBootLoader(t *testing.T) {
	s, _ := newSetup(t)
	if _, err := s.EnableIOMMU(context.Background()); !errors.Is(err, ErrUnsupported) {
		t.Errorf("got %v", err)
	}
}

func TestEnableWatchdog(t *testing.T) {
	s, f := newSetup(t)
	if err := s.EnableWatchdog(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.calls, "modprobe softdog") || !strings.Contains(read(t, s, "/etc/modules-load.d/virtomir-softdog.conf"), "softdog") {
		t.Errorf("calls %q", f.calls)
	}
	// A hardware watchdog: nothing to load.
	s2, f2 := newSetup(t)
	write(t, s2, "/dev/watchdog", "")
	_ = s2.EnableWatchdog(context.Background())
	if len(f2.calls) != 0 || fileExists(s2.path("/etc/modules-load.d/virtomir-softdog.conf")) {
		t.Errorf("loaded softdog next to a hardware watchdog: %q", f2.calls)
	}
}

func TestSSHKeys(t *testing.T) {
	s, f := newSetup(t)
	f.after["ssh-keygen"] = nil
	// ssh-keygen is faked: write the key it would have made.
	write(t, s, "/root/.ssh/id_ed25519", "PRIVATE")
	write(t, s, "/root/.ssh/id_ed25519.pub", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDemoKey virtomir@hv-01\n")
	key, err := s.PublicKey(context.Background(), "virtomir@hv-01")
	if err != nil || !strings.HasPrefix(key, "ssh-ed25519 ") || len(f.calls) != 0 {
		t.Fatalf("key %q err %v calls %q", key, err, f.calls)
	}
	if err := s.Authorize(key); err != nil {
		t.Fatal(err)
	}
	if err := s.Authorize(key); err != nil { // twice: still one line
		t.Fatal(err)
	}
	if got := read(t, s, "/root/.ssh/authorized_keys"); strings.Count(got, "AAAAC3NzaC1lZDI1NTE5AAAAIDemoKey") != 1 {
		t.Errorf("authorized_keys = %q", got)
	}
	for _, bad := range []string{"", "hello", "ssh-ed25519 AAAA\ncommand=\"rm -rf /\" ssh-ed25519 BBBB", `command="x" ssh-ed25519 AAAA`} {
		if err := s.Authorize(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("Authorize(%q) = %v", bad, err)
		}
	}
}
