package baremetal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// Installer files, from the Debian 13 netboot image.
var installerFiles = []string{"linux", "initrd.gz"}

const installerPath = "/debian/dists/trixie/main/installer-amd64/current/images"

// InstallerReady reports whether the installer files are in dir.
func InstallerReady(dir string) bool {
	for _, f := range installerFiles {
		if st, err := os.Stat(filepath.Join(dir, "debian13", f)); err != nil || st.Size() == 0 {
			return false
		}
	}
	return true
}

// FetchInstaller downloads the Debian 13 netboot kernel and initrd from mirror into
// dir/debian13, each checked against the image's SHA256SUMS.
func FetchInstaller(ctx context.Context, client *http.Client, mirror, dir string) error {
	base := "https://" + mirror + installerPath
	get := func(url string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		res, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		if res.StatusCode != http.StatusOK {
			res.Body.Close()
			return nil, fmt.Errorf("%s: %s", url, res.Status)
		}
		return res, nil
	}
	res, err := get(base + "/SHA256SUMS")
	if err != nil {
		return err
	}
	sums, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	res.Body.Close()
	if err != nil {
		return err
	}
	want := map[string]string{}
	for line := range strings.Lines(string(sums)) {
		f := strings.Fields(line)
		if len(f) == 2 {
			want[strings.TrimPrefix(f[1], "./")] = f[0]
		}
	}
	out := filepath.Join(dir, "debian13")
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	for _, name := range installerFiles {
		rel := "netboot/debian-installer/amd64/" + name
		sum, ok := want[rel]
		if !ok {
			return fmt.Errorf("SHA256SUMS lists no %s", rel)
		}
		res, err := get(base + "/" + rel)
		if err != nil {
			return err
		}
		tmp := filepath.Join(out, name+".part")
		f, err := os.Create(tmp)
		if err != nil {
			res.Body.Close()
			return err
		}
		h := sha256.New()
		_, err = io.Copy(io.MultiWriter(f, h), io.LimitReader(res.Body, 512<<20))
		res.Body.Close()
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err == nil && hex.EncodeToString(h.Sum(nil)) != sum {
			err = fmt.Errorf("%s does not match its SHA-256 checksum", rel)
		}
		if err != nil {
			_ = os.Remove(tmp)
			return err
		}
		if err := os.Rename(tmp, filepath.Join(out, name)); err != nil {
			return err
		}
	}
	return nil
}

// IPXE is the script a machine with mac gets: the installer while VirtoMir installs it, a
// boot from its own disk otherwise (so it never installs twice).
func IPXE(h Host, known bool, s Settings) string {
	if !known || h.State != StateInstalling {
		why := "VirtoMir is not installing it"
		if !known {
			why = "VirtoMir does not know it"
		}
		return "#!ipxe\necho This machine boots from its disk: " + why + ".\nexit\n"
	}
	b := s.Base()
	return fmt.Sprintf(`#!ipxe
echo VirtoMir installs Debian 13 and VirtoMir on %[1]s
kernel %[2]s/boot/files/debian13/linux auto=true priority=critical preseed/url=%[2]s/boot/%[3]s/preseed.cfg hostname=%[1]s domain= BOOTIF=01-${netX/mac:hexhyp} --- quiet
initrd %[2]s/boot/files/debian13/initrd.gz
boot
`, h.Name, b, h.Token)
}

// Preseed answers every question of the Debian installer. The disk is erased.
func Preseed(h Host, s Settings) string {
	var b strings.Builder
	w := func(line string) { b.WriteString("d-i " + line + "\n") }
	b.WriteString("# Written by VirtoMir for " + h.Name + " (ADR-0077). Installs Debian 13 and VirtoMir; erases the disk.\n")
	w("debian-installer/locale string en_US.UTF-8")
	w("keyboard-configuration/xkb-keymap select us")
	w("netcfg/choose_interface select auto")
	w("netcfg/get_hostname string " + h.Name)
	w("netcfg/hostname string " + h.Name)
	w("netcfg/get_domain string")
	w("hw-detect/load_firmware boolean true")
	w("mirror/country string manual")
	w("mirror/http/hostname string " + s.Mirror)
	w("mirror/http/directory string /debian")
	w("mirror/http/proxy string")
	w("clock-setup/utc boolean true")
	w("time/zone string Etc/UTC")
	// No root password: the user virtomir is an admin (sudo, and libvirt for VirtoMir).
	w("passwd/root-login boolean false")
	w("passwd/user-fullname string VirtoMir admin")
	w("passwd/username string virtomir")
	if h.PasswordHash != "" {
		w("passwd/user-password-crypted password " + h.PasswordHash)
	} else {
		w("passwd/user-password-crypted password !")
	}
	w("passwd/user-default-groups string sudo")
	if h.Disk != "" {
		w("partman-auto/disk string " + h.Disk)
	} else {
		w(`partman/early_command string debconf-set partman-auto/disk "$(list-devices disk | head -n1)"`)
	}
	for _, l := range []string{
		"partman-auto/method string regular",
		"partman-auto/choose_recipe select atomic",
		"partman-lvm/device_remove_lvm boolean true",
		"partman-md/device_remove_md boolean true",
		"partman-auto/purge_lvm_from_device boolean true",
		"partman-efi/non_efi_system boolean true",
		"partman-partitioning/confirm_write_new_label boolean true",
		"partman/choose_partition select finish",
		"partman/confirm boolean true",
		"partman/confirm_nooverwrite boolean true",
		"pkgsel/include string curl ca-certificates openssh-server",
		"pkgsel/upgrade select none",
		"grub-installer/only_debian boolean true",
		"grub-installer/bootdev string default",
		"finish-install/reboot_in_progress note",
	} {
		w(l)
	}
	b.WriteString("tasksel tasksel/first multiselect standard, ssh-server\n")
	b.WriteString("popularity-contest popularity-contest/participate boolean false\n")
	w(fmt.Sprintf("preseed/late_command string in-target sh -c 'curl -fsS %s/boot/%s/late.sh -o /root/virtomir-late.sh && sh /root/virtomir-late.sh > /var/log/virtomir-install.log 2>&1'", s.Base(), h.Token))
	return b.String()
}

// LateScript runs inside the installed system at the end of the installation: it
// installs VirtoMir and sets up the join on first boot.
func LateScript(h Host, s Settings) string {
	base, tok := s.Base(), h.Token
	repo := strings.Fields(s.Repo)
	var b strings.Builder
	fmt.Fprintf(&b, `#!/bin/sh
# Written by VirtoMir for %[1]s (ADR-0077).
set -eu
install -d -m 0755 /etc/apt/keyrings
curl -fsS %[2]s/boot/files/virtomir.asc -o /etc/apt/keyrings/virtomir.asc
echo "deb [signed-by=/etc/apt/keyrings/virtomir.asc] %[3]s %[4]s" > /etc/apt/sources.list.d/virtomir.list
apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install -y virtomir
usermod -aG libvirt virtomir
`, h.Name, base, repo[0], strings.Join(repo[1:], " "))
	if len(h.SSHKeys) > 0 {
		b.WriteString("install -d -m 0700 -o virtomir -g virtomir /home/virtomir/.ssh\ncat > /home/virtomir/.ssh/authorized_keys <<'EOF'\n")
		for _, k := range h.SSHKeys {
			b.WriteString(k + "\n")
		}
		b.WriteString("EOF\nchown virtomir:virtomir /home/virtomir/.ssh/authorized_keys\nchmod 0600 /home/virtomir/.ssh/authorized_keys\n")
	}
	if st := h.Static; st != nil {
		p := netip.MustParsePrefix(st.Address)
		fmt.Fprintf(&b, `IF=$(ip -o -4 route show default | awk '{print $5; exit}')
[ -n "$IF" ] || IF=$(ip -o link show | awk -F': ' '$2 != "lo" {print $2; exit}')
cat > /etc/network/interfaces <<EOF
auto lo
iface lo inet loopback

auto $IF
iface $IF inet static
    address %s/%d
`, p.Addr(), p.Bits())
		if st.Gateway != "" {
			fmt.Fprintf(&b, "    gateway %s\n", st.Gateway)
		}
		if len(st.DNS) > 0 {
			fmt.Fprintf(&b, "    dns-nameservers %s\n", strings.Join(st.DNS, " "))
		}
		b.WriteString("EOF\n")
	}
	fmt.Fprintf(&b, `cat > /usr/local/sbin/virtomir-join <<'EOF'
#!/bin/sh
# Joins the VirtoMir cluster once, on the first boot after the installation.
set -eu
VIRTOMIR_JOIN_CODE=$(curl -fsS -X POST %[1]s/boot/%[2]s/join)
export VIRTOMIR_JOIN_CODE
VIRTOMIRD_ARGS=""
[ -f /etc/default/virtomird ] && . /etc/default/virtomird
# The running service answers the cluster's call back while this joins; it then restarts
# to load the membership.
/usr/bin/virtomird $VIRTOMIRD_ARGS join
systemctl restart virtomird
curl -fsS -X POST %[1]s/boot/%[2]s/joined || true
systemctl disable virtomir-join.service
EOF
chmod 0755 /usr/local/sbin/virtomir-join
cat > /etc/systemd/system/virtomir-join.service <<'EOF'
[Unit]
Description=Join the VirtoMir cluster this host was installed for
Wants=network-online.target
After=network-online.target virtomird.service

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/virtomir-join
Restart=on-failure
RestartSec=30

[Install]
WantedBy=multi-user.target
EOF
systemctl enable virtomir-join.service
curl -fsS -X POST %[1]s/boot/%[2]s/installed || true
`, base, tok)
	return b.String()
}

// Server serves the boot files on the plain HTTP port. Machines are told apart by MAC
// (iPXE script) and by the token of their installation (everything else).
type Server struct {
	Store *Store
	// Dir holds the installer files (dir/debian13).
	Dir string
	// RepoKey is the armored key the VirtoMir repository is signed with.
	RepoKey []byte
	// Invite makes a join code for the host named name.
	Invite func(ctx context.Context, name string) (string, error)
	Log    *slog.Logger

	mu     sync.Mutex
	booted map[string]string // IP → ID of the host being installed that booted from it
}

func remoteIP(r *http.Request) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

func (b *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	text := func(code int, s string) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(code)
		_, _ = io.WriteString(w, s)
	}
	path := strings.TrimPrefix(r.URL.Path, "/boot/")
	switch {
	case r.Method == http.MethodGet && path == "ipxe" && !strings.HasPrefix(r.UserAgent(), "iPXE"):
		// The Debian installer asks DHCP again, gets the same boot file name and takes it
		// for its preseed URL (it wins over preseed/url): answer the machine that just
		// booted from this address with its preseed.
		b.mu.Lock()
		id := b.booted[remoteIP(r)]
		b.mu.Unlock()
		h, ok := b.Store.Host(id)
		if !ok || h.State != StateInstalling {
			text(http.StatusNotFound, "this is the iPXE boot script, and no installation booted from "+remoteIP(r)+"\n")
			return
		}
		b.Store.Log(h.ID, false, "the installer fetched its answers; it erases the disk and installs Debian 13")
		text(http.StatusOK, Preseed(h, b.Store.Settings()))
		return
	case r.Method == http.MethodGet && path == "ipxe" && r.URL.Query().Get("mac") == "":
		// DHCP hands out a plain URL; iPXE fills in its MAC when it runs this.
		text(http.StatusOK, "#!ipxe\nchain "+b.Store.Settings().Base()+"/boot/ipxe?mac=${netX/mac}\n")
		return
	case r.Method == http.MethodGet && path == "ipxe":
		h, ok := b.Store.ByMAC(r.URL.Query().Get("mac"))
		if ok && h.State == StateInstalling {
			b.mu.Lock()
			if b.booted == nil {
				b.booted = map[string]string{}
			}
			b.booted[remoteIP(r)] = h.ID
			b.mu.Unlock()
			b.Store.Log(h.ID, false, "booted from the network ("+remoteIP(r)+"); the installer starts")
		}
		text(http.StatusOK, IPXE(h, ok, b.Store.Settings()))
		return
	case r.Method == http.MethodGet && path == "files/virtomir.asc":
		text(http.StatusOK, string(b.RepoKey))
		return
	case r.Method == http.MethodGet && (path == "files/debian13/linux" || path == "files/debian13/initrd.gz"):
		http.ServeFile(w, r, filepath.Join(b.Dir, "debian13", filepath.Base(path)))
		return
	}
	token, what, _ := strings.Cut(path, "/")
	h, ok := b.Store.ByToken(token)
	if !ok {
		text(http.StatusNotFound, "no installation with this token\n")
		return
	}
	// Each step of an installation, with the method it takes and the states it fits.
	steps := map[string]struct {
		method string
		states []string
	}{
		"preseed.cfg": {http.MethodGet, []string{StateInstalling}},
		"late.sh":     {http.MethodGet, []string{StateInstalling}},
		"installed":   {http.MethodPost, []string{StateInstalling}},
		"join":        {http.MethodPost, []string{StateInstalled, StateJoining}},
		"joined":      {http.MethodPost, []string{StateJoining}},
	}
	step, ok := steps[what]
	switch {
	case !ok:
		text(http.StatusNotFound, "no such boot file\n")
		return
	case r.Method != step.method:
		text(http.StatusMethodNotAllowed, step.method+" only\n")
		return
	case !slices.Contains(step.states, h.State):
		text(http.StatusConflict, h.Name+" is "+h.State+"\n")
		return
	}
	s := b.Store.Settings()
	switch what {
	case "preseed.cfg":
		b.Store.Log(h.ID, false, "the installer fetched its answers; it erases the disk and installs Debian 13")
		text(http.StatusOK, Preseed(h, s))
	case "late.sh":
		b.Store.Log(h.ID, false, "Debian is installed; installing VirtoMir")
		text(http.StatusOK, LateScript(h, s))
	case "installed":
		_, _ = b.Store.SetState(h.ID, StateInstalled, "installed; it restarts from its disk and joins the cluster")
		text(http.StatusOK, "ok\n")
	case "join":
		code, err := b.Invite(r.Context(), h.Name)
		if err != nil {
			b.Store.Log(h.ID, true, "no join code: "+err.Error())
			text(http.StatusServiceUnavailable, err.Error()+"\n")
			return
		}
		_, _ = b.Store.SetState(h.ID, StateJoining, "started from its disk; joining the cluster")
		text(http.StatusOK, code)
	case "joined":
		_, _ = b.Store.Update(h.ID, func(x *Host) error {
			x.State, x.Token, x.Message = StateJoined, "", ""
			x.Events = append(x.Events, Event{At: b.Store.now().UTC(), Message: "joined the cluster"})
			return nil
		})
		text(http.StatusOK, "ok\n")
	}
}

// ErrNoIPXE: proxy DHCP needs the iPXE boot programs (Debian package ipxe).
var ErrNoIPXE = errors.New("proxy DHCP needs iPXE: apt install ipxe (it puts undionly.kpxe and ipxe.efi in /usr/lib/ipxe)")
