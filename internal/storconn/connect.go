package storconn

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	// Base is where network shares are mounted (/srv/virtomir/<pool>, like carved pools).
	Base = "/srv/virtomir"
	// UnitDir holds the mount units of SMB shares.
	UnitDir = "/etc/systemd/system"
	// CredDir keeps SMB passwords, readable by root only.
	CredDir = "/etc/virtomir/smb"
)

// Prepare installs and starts what a protocol needs and lets QEMU use it under SELinux.
func Prepare(ctx context.Context, r Runner, kind string) error {
	if err := Ensure(ctx, r, kind); err != nil {
		return err
	}
	switch kind {
	case KindISCSI:
		// The initiator daemon: socket-activated on Fedora/RHEL, a service on Debian.
		if _, err := r.Run(ctx, "systemctl", "enable", "--now", "iscsid.socket"); err != nil {
			if _, err := r.Run(ctx, "systemctl", "enable", "--now", "iscsid.service"); err != nil {
				return fmt.Errorf("start the iSCSI initiator (iscsid): %w", err)
			}
		}
	case KindNFS:
		selinuxBool(ctx, r, "virt_use_nfs")
	case KindSMB:
		selinuxBool(ctx, r, "virt_use_samba")
	}
	return nil
}

// selinuxBool turns on an SELinux boolean for good; without SELinux there is none.
func selinuxBool(ctx context.Context, r Runner, name string) {
	if _, err := r.Run(ctx, "selinuxenabled"); err != nil {
		return
	}
	_, _ = r.Run(ctx, "setsebool", "-P", name, "1")
}

var nameJunk = regexp.MustCompile(`[^A-Za-z0-9]+`)

// PoolName names a pool after what it connects: smb-nas-vms, nfs-10-0-0-5-srv-vm, … A name
// that is taken gets a number.
func PoolName(f Found, taken func(string) bool) string {
	server := f.Server
	if f.Host != "" {
		server, _, _ = strings.Cut(f.Host, ".") // the host part of its DNS name
	}
	what := f.Name
	if f.Kind == KindISCSI {
		if _, after, ok := strings.Cut(what, ":"); ok {
			what = after // the target's own part of the IQN
		}
	}
	base := strings.Trim(nameJunk.ReplaceAllString(strings.ToLower(f.Kind+"-"+server+"-"+what), "-"), "-")
	if len(base) > 48 {
		base = strings.TrimRight(base[:48], "-")
	}
	name := base
	for i := 2; taken(name); i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	return name
}

var smbFromRE = regexp.MustCompile(`(?m)^# Storage pool connected by VirtoMir to (.+)\.$`)

// MountSMB mounts a share below Base on every boot and returns the directory. The password
// sits in a root-only file under CredDir, never in the unit.
func MountSMB(ctx context.Context, r Runner, pool, server, share string, c Credentials) (string, error) {
	dir := filepath.Join(Base, pool)
	if _, err := os.Stat(dir); err == nil {
		return "", invalidf("%s already exists", dir)
	}
	if err := checkSMBLogin(ctx, r, server, share, c); err != nil {
		return "", err
	}
	auth := "guest" // no user: an anonymous session
	if c.Username != "" {
		if err := os.MkdirAll(CredDir, 0o700); err != nil {
			return "", err
		}
		cred := filepath.Join(CredDir, pool)
		if err := os.WriteFile(cred, []byte(credText(c)), 0o600); err != nil {
			return "", err
		}
		auth = "credentials=" + cred
	}
	uid, gid := qemuIDs()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	b, err := r.Run(ctx, "systemd-escape", "-p", "--suffix=mount", dir)
	unit := strings.TrimSpace(string(b))
	if err != nil || unit == "" {
		return "", fmt.Errorf("name the mount unit for %s: %v", dir, err)
	}
	what := "//" + server + "/" + share
	text := fmt.Sprintf(`# Storage pool connected by VirtoMir to %s.
[Unit]
Description=VirtoMir storage pool %s
Wants=network-online.target
After=network-online.target

[Mount]
What=%s
Where=%s
Type=cifs
Options=%s,uid=%s,gid=%s,file_mode=0660,dir_mode=0770,nofail,_netdev

[Install]
WantedBy=remote-fs.target
`, what, pool, what, dir, auth, uid, gid)
	if err := os.WriteFile(filepath.Join(UnitDir, unit), []byte(text), 0o644); err != nil {
		return "", err
	}
	if _, err := r.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		return "", err
	}
	if _, err := r.Run(ctx, "systemctl", "enable", "--now", unit); err != nil {
		_ = UnmountSMB(ctx, r, pool)
		return "", fmt.Errorf("mount %s: %w", what, err)
	}
	return dir, nil
}

// ErrAuth: the server wants a user name and password, or refused the ones given.
var ErrAuth = fmt.Errorf("%w: the server asks for a user name and password, or refused the ones given", ErrInvalid)

// checkSMBLogin signs in to the share once before it is mounted, so a wrong password is
// told as such instead of as a failed mount unit.
func checkSMBLogin(ctx context.Context, r Runner, server, share string, c Credentials) error {
	args := []string{"//" + server + "/" + share, "-c", "exit"}
	if c.Username == "" {
		args = append(args, "-N")
	} else {
		auth, err := authFile(c)
		if err != nil {
			return err
		}
		defer os.Remove(auth)
		args = append(args, "-A", auth)
	}
	if out, err := r.Run(ctx, "smbclient", args...); err != nil {
		if authError(err.Error() + string(out)) { // smbclient reports on standard output
			return ErrAuth
		}
		return fmt.Errorf("sign in to //%s/%s: %w", server, share, err)
	}
	return nil
}

// AuthFailed turns a login refused by an iSCSI target into ErrAuth.
func AuthFailed(err error) error {
	if err != nil && authError(err.Error()) {
		return ErrAuth
	}
	return err
}

// qemuIDs are the user and group QEMU runs as (qemu on Fedora/RHEL, libvirt-qemu on
// Debian/Ubuntu): files on a share belong to them, as nothing can be chowned there.
func qemuIDs() (string, string) {
	for _, name := range []string{"qemu", "libvirt-qemu"} {
		if u, err := user.Lookup(name); err == nil {
			return u.Uid, u.Gid
		}
	}
	return "0", "0"
}

// UnmountSMB undoes MountSMB; a pool that is no SMB share VirtoMir mounted is ErrNotSMB.
func UnmountSMB(ctx context.Context, r Runner, pool string) error {
	dir := filepath.Join(Base, pool)
	b, err := r.Run(ctx, "systemd-escape", "-p", "--suffix=mount", dir)
	unit := strings.TrimSpace(string(b))
	if err != nil || unit == "" {
		return fmt.Errorf("name the mount unit for %s: %v", dir, err)
	}
	unitFile := filepath.Join(UnitDir, unit)
	text, err := os.ReadFile(unitFile)
	if err != nil || !smbFromRE.Match(text) {
		return ErrNotSMB
	}
	if _, err := r.Run(ctx, "systemctl", "disable", "--now", unit); err != nil {
		return err
	}
	if err := os.Remove(unitFile); err != nil {
		return err
	}
	_, _ = r.Run(ctx, "systemctl", "daemon-reload")
	_ = os.Remove(filepath.Join(CredDir, pool))
	_ = os.Remove(dir)
	return nil
}

// ErrNotSMB: the pool is no share VirtoMir mounted.
var ErrNotSMB = fmt.Errorf("%w: this pool is no SMB share VirtoMir connected", ErrInvalid)

// SMBShare is the share ("//server/share") the pool is, when VirtoMir mounted it; else "".
func SMBShare(ctx context.Context, r Runner, pool string) string {
	b, err := r.Run(ctx, "systemd-escape", "-p", "--suffix=mount", filepath.Join(Base, pool))
	if err != nil {
		return ""
	}
	text, err := os.ReadFile(filepath.Join(UnitDir, strings.TrimSpace(string(b))))
	if err != nil {
		return ""
	}
	if m := smbFromRE.FindSubmatch(text); m != nil {
		return string(m[1])
	}
	return ""
}
