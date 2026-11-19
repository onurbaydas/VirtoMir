package baremetal

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Proxy DHCP (ModeProxy, experimental: not tested on a real network): dnsmasq answers the
// PXE requests of the machines being installed, next to the network's own DHCP server,
// which still hands out the addresses. Firmware PXE gets iPXE over TFTP, iPXE gets the
// script over HTTP.

// Unit is the systemd unit dnsmasq runs as.
const Unit = "virtomir-netboot-dhcp"

// ipxeDirs are where the iPXE boot programs live: Debian's ipxe package, then Fedora/RHEL's
// ipxe-bootimgs-x86.
var ipxeDirs = []string{"/usr/lib/ipxe", "/usr/share/ipxe"}

// ipxeNames are the names each boot program has in those packages (Fedora calls the UEFI one
// ipxe-x86_64.efi).
var ipxeNames = map[string][]string{
	"undionly.kpxe": {"undionly.kpxe"},
	"ipxe.efi":      {"ipxe.efi", "ipxe-x86_64.efi"},
}

// findIPXE is the path of boot program f on this host, or "".
func findIPXE(f string) string {
	for _, d := range ipxeDirs {
		for _, n := range ipxeNames[f] {
			if p := filepath.Join(d, n); fileExists(p) {
				return p
			}
		}
	}
	return ""
}

// Runner runs a program on this host.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Exec is Runner on the real host.
type Exec struct{}

func (Exec) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// DnsmasqConfig answers PXE on network (10.0.0.0, the boot network's address) only for macs.
func DnsmasqConfig(s Settings, network string, macs []string, tftp string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `# Written by VirtoMir (ADR-0077): network boot of the hosts it installs. Do not edit.
port=0
interface=%s
bind-interfaces
dhcp-range=%s,proxy
dhcp-userclass=set:ipxe,iPXE
enable-tftp
tftp-root=%s
log-dhcp
`, s.Interface, network, tftp)
	for _, m := range macs {
		fmt.Fprintf(&b, "dhcp-host=%s,set:virtomir\n", m)
	}
	fmt.Fprintf(&b, `dhcp-boot=tag:virtomir,tag:ipxe,%s/boot/ipxe
pxe-service=tag:virtomir,tag:!ipxe,x86PC,"VirtoMir install",undionly.kpxe
pxe-service=tag:virtomir,tag:!ipxe,X86-64_EFI,"VirtoMir install",ipxe.efi
pxe-service=tag:virtomir,tag:!ipxe,BC_EFI,"VirtoMir install",ipxe.efi
`, s.Base())
	return b.String()
}

// ApplyProxy runs dnsmasq for the machines being installed, or stops it when there are
// none (or the mode is external). It runs at every start; --no-ask-password keeps a daemon
// without root from popping up a polkit password dialog on the host's desktop.
func ApplyProxy(ctx context.Context, r Runner, s Settings, network string, macs []string, dir string) error {
	_, _ = r.Run(ctx, "systemctl", "--no-ask-password", "stop", Unit)
	if !s.Enabled || s.Mode != ModeProxy || len(macs) == 0 {
		return nil
	}
	tftp := filepath.Join(dir, "tftp")
	if err := os.MkdirAll(tftp, 0o755); err != nil {
		return err
	}
	for _, f := range []string{"undionly.kpxe", "ipxe.efi"} {
		src := findIPXE(f)
		if src == "" {
			return ErrNoIPXE
		}
		if err := copyFile(src, filepath.Join(tftp, f)); err != nil {
			return err
		}
	}
	conf := filepath.Join(dir, "dnsmasq.conf")
	if err := os.WriteFile(conf, []byte(DnsmasqConfig(s, network, macs, tftp)), 0o644); err != nil {
		return err
	}
	_, err := r.Run(ctx, "systemd-run", "--no-ask-password", "--unit="+Unit, "--property=Restart=on-failure", "--collect",
		"dnsmasq", "--keep-in-foreground", "--conf-file="+conf)
	return err
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
