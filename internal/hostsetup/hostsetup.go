// Package hostsetup does the host-side chores VirtoMir used to leave to the admin: installing
// the packages a feature needs (restic, FRR, iPXE + dnsmasq, cephadm + podman), turning on the
// IOMMU on the kernel command line for PCI passthrough, loading a software watchdog for HA,
// and the root SSH key ZFS replication sends with. Every command goes through a Runner and
// every file through Root, so all of it is tested without touching the host.
package hostsetup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Runner runs a command and returns its standard output.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Exec runs commands on this host.
type Exec struct{}

// Run runs name with args; an error carries the command's standard error.
func (Exec) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return out, fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), lastLines(msg, 3))
	}
	return out, nil
}

// ErrInvalid marks a request VirtoMir refuses (unknown feature, malformed key).
var ErrInvalid = errors.New("invalid")

// ErrUnsupported marks a host VirtoMir cannot change by itself (no apt or dnf, an unknown
// boot loader); the message says what to do instead.
var ErrUnsupported = errors.New("unsupported")

// Setup does the chores on one host.
type Setup struct {
	Run Runner
	// Look finds a command on PATH (exec.LookPath on a real host).
	Look func(string) (string, error)
	// Root is where the host's files are ("" = /).
	Root string
}

// Host is the real host.
func Host() *Setup { return &Setup{Run: Exec{}, Look: exec.LookPath} }

func (s *Setup) path(p string) string { return filepath.Join(orRoot(s.Root), p) }

func orRoot(r string) string {
	if r == "" {
		return "/"
	}
	return r
}

func (s *Setup) has(cmd string) bool {
	_, err := s.Look(cmd)
	return err == nil
}

// ---- Packages -------------------------------------------------------------------------------

// Feature is something VirtoMir can set up by installing packages.
type Feature struct {
	// Title names it for the user.
	Title string
	Apt   []string
	Dnf   []string
	// Commands are what must be on PATH afterwards (and tell whether it is installed).
	Commands []string
	// AnyFile: one of these must exist as well (files a package ships outside PATH; the
	// places differ between Debian and Fedora).
	AnyFile []string
}

// Features VirtoMir installs by itself. The names are the API's.
var Features = map[string]Feature{
	"offsite":   {Title: "Off-site copies (restic)", Apt: []string{"restic"}, Dnf: []string{"restic"}, Commands: []string{"restic"}},
	"physnet":   {Title: "BGP / EVPN routing (FRR)", Apt: []string{"frr"}, Dnf: []string{"frr"}, Commands: []string{"vtysh"}},
	"baremetal": {Title: "Network installs (dnsmasq + iPXE)", Apt: []string{"dnsmasq", "ipxe"}, Dnf: []string{"dnsmasq", "ipxe-bootimgs-x86"}, Commands: []string{"dnsmasq"}, AnyFile: []string{"/usr/lib/ipxe/undionly.kpxe", "/usr/share/ipxe/undionly.kpxe"}},
	"ceph":      {Title: "Ceph (cephadm + podman)", Apt: []string{"cephadm", "podman"}, Dnf: []string{"cephadm", "podman"}, Commands: []string{"cephadm", "podman"}},
}

// Installed tells whether every command of the feature is on PATH.
func (s *Setup) Installed(feature string) bool {
	f, ok := Features[feature]
	if !ok {
		return false
	}
	for _, c := range f.Commands {
		if !s.has(c) {
			return false
		}
	}
	return s.hasAnyFile(f)
}

func (s *Setup) hasAnyFile(f Feature) bool {
	return len(f.AnyFile) == 0 || slices.ContainsFunc(f.AnyFile, func(p string) bool { return fileExists(s.path(p)) })
}

// Install installs the feature's packages with the host's package manager, then does what the
// feature needs beyond the package (FRR: the BGP daemon on and running). report tells the
// user which step runs.
func (s *Setup) Install(ctx context.Context, feature string, report func(string)) error {
	f, ok := Features[feature]
	if !ok {
		return fmt.Errorf("%w: VirtoMir does not know how to set up %q", ErrInvalid, feature)
	}
	switch {
	case s.has("apt-get"):
		report("Updating the package lists…")
		if _, err := s.Run.Run(ctx, "apt-get", "update", "-q"); err != nil {
			return err
		}
		report("Installing " + strings.Join(f.Apt, ", ") + "…")
		args := append([]string{"DEBIAN_FRONTEND=noninteractive", "apt-get", "install", "-y", "-q", "--no-install-recommends"}, f.Apt...)
		if _, err := s.Run.Run(ctx, "env", args...); err != nil {
			return err
		}
	case s.has("dnf"):
		if feature == "ceph" || feature == "offsite" {
			// cephadm and restic are in EPEL on RHEL-likes; on Fedora this is a no-op.
			_, _ = s.Run.Run(ctx, "dnf", "install", "-y", "-q", "epel-release")
		}
		report("Installing " + strings.Join(f.Dnf, ", ") + "…")
		if _, err := s.Run.Run(ctx, "dnf", append([]string{"install", "-y", "-q"}, f.Dnf...)...); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: this host has neither apt nor dnf; install %s with its package manager", ErrUnsupported, strings.Join(f.Apt, ", "))
	}
	if feature == "physnet" {
		report("Turning on FRR's BGP daemon…")
		if err := s.enableBGPD(); err != nil {
			return err
		}
		if _, err := s.Run.Run(ctx, "systemctl", "enable", "frr"); err != nil {
			return err
		}
		if _, err := s.Run.Run(ctx, "systemctl", "restart", "frr"); err != nil {
			return err
		}
	}
	for _, c := range f.Commands {
		if !s.has(c) {
			return fmt.Errorf("the packages were installed but %s is still missing", c)
		}
	}
	if !s.hasAnyFile(f) {
		return fmt.Errorf("the packages were installed but %s is still missing", f.AnyFile[len(f.AnyFile)-1])
	}
	return nil
}

var bgpdLine = regexp.MustCompile(`(?m)^bgpd=.*$`)

// enableBGPD sets bgpd=yes in /etc/frr/daemons (the package ships it off).
func (s *Setup) enableBGPD() error {
	p := s.path("/etc/frr/daemons")
	b, err := os.ReadFile(p)
	if err != nil {
		return fmt.Errorf("read FRR's daemons file: %w", err)
	}
	var out []byte
	if bgpdLine.Match(b) {
		out = bgpdLine.ReplaceAll(b, []byte("bgpd=yes"))
	} else {
		out = append(append(b, '\n'), "bgpd=yes\n"...)
	}
	return os.WriteFile(p, out, 0o640)
}

// ---- IOMMU ----------------------------------------------------------------------------------

// IOMMUResult says what EnableIOMMU did.
type IOMMUResult struct {
	// Params were added to the kernel command line (empty: they were there already).
	Params []string `json:"params"`
	// Method is how: grubby, grub (with its config file) or kernel-cmdline; active when the
	// IOMMU was already on and nothing was changed.
	Method string `json:"method"`
	// RebootNeeded is true until the host has started with the new command line.
	RebootNeeded bool `json:"rebootNeeded"`
	// Backup is the copy of the file VirtoMir changed, to undo it by hand.
	Backup string `json:"backup,omitempty"`
}

// iommuParams are the kernel parameters for this CPU (iommu=pt keeps host devices fast).
func (s *Setup) iommuParams() []string {
	b, _ := os.ReadFile(s.path("/proc/cpuinfo"))
	if strings.Contains(string(b), "AuthenticAMD") {
		return []string{"amd_iommu=on", "iommu=pt"}
	}
	return []string{"intel_iommu=on", "iommu=pt"}
}

// iommuActive tells whether the running kernel already has an IOMMU (dmar0, ivhd0…).
func (s *Setup) iommuActive() bool {
	e, _ := os.ReadDir(s.path("/sys/class/iommu"))
	return len(e) > 0
}

// missingParams are the ones not already in line.
func missingParams(line string, want []string) []string {
	have := strings.Fields(line)
	var out []string
	for _, p := range want {
		if !slices.Contains(have, p) {
			out = append(out, p)
		}
	}
	return out
}

var grubDefault = regexp.MustCompile(`(?m)^GRUB_CMDLINE_LINUX_DEFAULT=(["']?)(.*?)(["']?)$`)

// EnableIOMMU adds the IOMMU parameters to the kernel command line of every installed kernel,
// keeping a backup of the file it edits. The firmware switch (VT-d / AMD-Vi) cannot be set
// from Linux; the result says a reboot is needed.
func (s *Setup) EnableIOMMU(ctx context.Context) (IOMMUResult, error) {
	if s.iommuActive() {
		// Many kernels (Fedora's among them) turn it on without a parameter: change nothing.
		return IOMMUResult{Method: "active"}, nil
	}
	want := s.iommuParams()
	running, _ := os.ReadFile(s.path("/proc/cmdline"))
	res := IOMMUResult{RebootNeeded: len(missingParams(string(running), want)) > 0}

	switch {
	case s.has("grubby"): // Fedora, RHEL and the like
		res.Method = "grubby"
		out, err := s.Run.Run(ctx, "grubby", "--info=DEFAULT")
		if err != nil {
			return res, err
		}
		res.Params = missingParams(grubbyArgs(string(out)), want)
		if len(res.Params) == 0 {
			return res, nil
		}
		_, err = s.Run.Run(ctx, "grubby", "--update-kernel=ALL", "--args="+strings.Join(res.Params, " "))
		return res, err

	case fileExists(s.path("/etc/kernel/cmdline")): // systemd-boot, Proxmox
		res.Method = "kernel-cmdline"
		p := s.path("/etc/kernel/cmdline")
		b, err := os.ReadFile(p)
		if err != nil {
			return res, err
		}
		line := strings.TrimSpace(string(b))
		if res.Params = missingParams(line, want); len(res.Params) == 0 {
			return res, nil
		}
		if res.Backup, err = backup(p, b); err != nil {
			return res, err
		}
		if err := os.WriteFile(p, []byte(strings.TrimSpace(line+" "+strings.Join(res.Params, " "))+"\n"), 0o644); err != nil {
			return res, err
		}
		switch {
		case s.has("proxmox-boot-tool"):
			_, err = s.Run.Run(ctx, "proxmox-boot-tool", "refresh")
		case s.has("kernel-install"):
			_, err = s.Run.Run(ctx, "kernel-install", "add-all")
		}
		return res, err

	case fileExists(s.path("/etc/default/grub")): // Debian, Ubuntu
		res.Method = "grub"
		p := s.path("/etc/default/grub")
		b, err := os.ReadFile(p)
		if err != nil {
			return res, err
		}
		m := grubDefault.FindSubmatch(b)
		current := ""
		if m != nil {
			current = string(m[2])
		}
		if res.Params = missingParams(current, want); len(res.Params) == 0 {
			return res, nil
		}
		next := strings.TrimSpace(current + " " + strings.Join(res.Params, " "))
		var out []byte
		if m != nil {
			out = grubDefault.ReplaceAll(b, []byte(`GRUB_CMDLINE_LINUX_DEFAULT="`+strings.ReplaceAll(next, "$", "$$")+`"`))
		} else {
			out = append(append(b, '\n'), `GRUB_CMDLINE_LINUX_DEFAULT="`+next+"\"\n"...)
		}
		if res.Backup, err = backup(p, b); err != nil {
			return res, err
		}
		if err := os.WriteFile(p, out, 0o644); err != nil {
			return res, err
		}
		switch {
		case s.has("update-grub"):
			_, err = s.Run.Run(ctx, "update-grub")
		case s.has("grub-mkconfig"):
			_, err = s.Run.Run(ctx, "grub-mkconfig", "-o", "/boot/grub/grub.cfg")
		case s.has("grub2-mkconfig"):
			_, err = s.Run.Run(ctx, "grub2-mkconfig", "-o", "/boot/grub2/grub.cfg")
		default:
			err = fmt.Errorf("%w: /etc/default/grub was changed, but no grub-mkconfig was found to apply it", ErrUnsupported)
		}
		if err != nil {
			_ = os.WriteFile(p, b, 0o644) // put the old file back: nothing half-done
			res.Backup = ""
		}
		return res, err
	}
	return res, fmt.Errorf("%w: VirtoMir does not know this host's boot loader; add %s to the kernel command line and reboot", ErrUnsupported, strings.Join(want, " "))
}

// grubbyArgs is the args="…" line of grubby --info.
func grubbyArgs(info string) string {
	for _, l := range strings.Split(info, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "args="); ok {
			return strings.Trim(v, `"'`)
		}
	}
	return ""
}

// backup keeps a copy of a file before VirtoMir changes it (once: the first, original copy).
func backup(p string, b []byte) (string, error) {
	bak := p + ".virtomir-backup"
	if fileExists(bak) {
		return bak, nil
	}
	return bak, os.WriteFile(bak, b, 0o644)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// ---- Watchdog -------------------------------------------------------------------------------

// HasWatchdog tells whether /dev/watchdog exists.
func (s *Setup) HasWatchdog() bool { return fileExists(s.path("/dev/watchdog")) }

// EnableWatchdog loads the kernel's software watchdog (softdog) when the host has no hardware
// one, and keeps it loaded across reboots.
func (s *Setup) EnableWatchdog(ctx context.Context) error {
	switch {
	case !s.HasWatchdog():
		if _, err := s.Run.Run(ctx, "modprobe", "softdog"); err != nil {
			return err
		}
	case !fileExists(s.path("/sys/module/softdog")):
		return nil // a hardware watchdog: softdog would only add a second one at every boot
	}
	dir := s.path("/etc/modules-load.d")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "virtomir-softdog.conf"), []byte("# Loaded by VirtoMir for HA self-fencing\nsoftdog\n"), 0o644)
}

// ---- SSH key for replication -----------------------------------------------------------------

const keyFile = "/root/.ssh/id_ed25519"

// PublicKey returns root's SSH public key, making an ed25519 key first when root has none.
func (s *Setup) PublicKey(ctx context.Context, comment string) (string, error) {
	pub := s.path(keyFile + ".pub")
	if !fileExists(s.path(keyFile)) {
		if err := os.MkdirAll(filepath.Dir(s.path(keyFile)), 0o700); err != nil {
			return "", err
		}
		if _, err := s.Run.Run(ctx, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", comment, "-f", s.path(keyFile)); err != nil {
			return "", err
		}
	}
	b, err := os.ReadFile(pub)
	if err != nil {
		return "", fmt.Errorf("read root's public key: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

var pubKeyRE = regexp.MustCompile(`^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp(256|384|521)) [A-Za-z0-9+/]+={0,3}( [A-Za-z0-9@._+-]{1,100})?$`)

// Authorize lets the holder of key sign in as root (appends it to authorized_keys once).
func (s *Setup) Authorize(key string) error {
	key = strings.TrimSpace(key)
	if !pubKeyRE.MatchString(key) {
		return fmt.Errorf("%w: that is not an SSH public key", ErrInvalid)
	}
	dir := s.path("/root/.ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	p := filepath.Join(dir, "authorized_keys")
	b, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	body := strings.Fields(key)[1]
	for _, l := range strings.Split(string(b), "\n") {
		if f := strings.Fields(l); len(f) >= 2 && slices.Contains(f, body) {
			return nil // there already
		}
	}
	if len(b) > 0 && b[len(b)-1] != '\n' {
		b = append(b, '\n')
	}
	return os.WriteFile(p, append(b, key+"\n"...), 0o600)
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " · ")
}
