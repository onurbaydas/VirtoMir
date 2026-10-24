package ceph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// ClientUser is the cephx user VirtoMir creates for libvirt: RBD rights only, not admin.
const ClientUser = "virtomir"

// BootstrapSpec is what bootstrapping a cluster on this host needs.
type BootstrapSpec struct {
	MonIP              string `json:"monIp"`
	ClusterNetwork     string `json:"clusterNetwork,omitempty"`
	InitialOSDDisk     string `json:"initialOsdDisk,omitempty"`
	SingleHostDefaults bool   `json:"singleHostDefaults"`
}

// Validate checks the addresses and the disk path.
func (b BootstrapSpec) Validate() error {
	if _, err := netip.ParseAddr(b.MonIP); err != nil {
		return invalidf("the monitor address %q is not an IP address", b.MonIP)
	}
	if b.ClusterNetwork != "" {
		if _, err := netip.ParsePrefix(b.ClusterNetwork); err != nil {
			return invalidf("the cluster network %q is not a CIDR like 10.10.0.0/24", b.ClusterNetwork)
		}
	}
	if b.InitialOSDDisk != "" && (!blockDevRE.MatchString(b.InitialOSDDisk) || strings.Contains(b.InitialOSDDisk, "..")) {
		return invalidf("%q is not a disk path like /dev/sdb", b.InitialOSDDisk)
	}
	return nil
}

// hostRE: what cephadm accepts as a host name.
var hostRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,252}$`)

// missing turns "command not found" into words for people.
func missing(err error, what string) error {
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("%s is not installed on this host", what)
	}
	return err
}

func trimmed(ctx context.Context, r Runner, name string, args ...string) (string, error) {
	out, err := r.Run(ctx, name, args...)
	return strings.TrimSpace(string(out)), err
}

// Bootstrap creates a Ceph cluster with this host (hostname) as its first monitor and
// manager, creates VirtoMir's RBD user and, with InitialOSDDisk, the first OSD. The disk
// is checked before cephadm runs: a disk with data stops everything.
func Bootstrap(ctx context.Context, r Runner, store *Store, hostname string, spec BootstrapSpec) (ClusterConfig, error) {
	if err := spec.Validate(); err != nil {
		return ClusterConfig{}, err
	}
	if _, ok := store.Config(); ok {
		return ClusterConfig{}, fmt.Errorf("%w: this host manages a Ceph cluster already", ErrExists)
	}
	var disk DiskInfo
	if spec.InitialOSDDisk != "" {
		if err := EnsureDiskEmpty(ctx, r, spec.InitialOSDDisk); err != nil {
			return ClusterConfig{}, err
		}
		disk = DiskInfo{Path: spec.InitialOSDDisk, Empty: true}
	}
	args := []string{"bootstrap", "--mon-ip", spec.MonIP, "--skip-dashboard", "--skip-monitoring-stack"}
	if spec.SingleHostDefaults {
		args = append(args, "--single-host-defaults")
	}
	if spec.ClusterNetwork != "" {
		args = append(args, "--cluster-network", spec.ClusterNetwork)
	}
	if _, err := r.Run(ctx, "cephadm", args...); err != nil {
		return ClusterConfig{}, missing(err, "cephadm")
	}
	cfg, err := readCluster(ctx, r)
	if err != nil {
		return ClusterConfig{}, err
	}
	cfg.BootstrapHost = hostname
	if err := store.SaveConfig(cfg); err != nil {
		return ClusterConfig{}, err
	}
	if disk.Path != "" {
		if err := AddOSD(ctx, r, hostname, disk); err != nil {
			return cfg, fmt.Errorf("the cluster is up, but the first OSD failed: %w", err)
		}
	}
	saved, _ := store.Config()
	return saved, nil
}

// AdoptCluster records an existing cluster this host can reach as admin (for clusters made
// outside VirtoMir).
func AdoptCluster(ctx context.Context, r Runner, store *Store, hostname string) (ClusterConfig, error) {
	cfg, err := readCluster(ctx, r)
	if err != nil {
		return ClusterConfig{}, err
	}
	cfg.BootstrapHost = hostname
	if err := store.SaveConfig(cfg); err != nil {
		return ClusterConfig{}, err
	}
	saved, _ := store.Config()
	return saved, nil
}

// readCluster asks a running cluster for its ID, monitors, SSH key and VirtoMir's key.
func readCluster(ctx context.Context, r Runner) (ClusterConfig, error) {
	var cfg ClusterConfig
	var err error
	if cfg.FSID, err = trimmed(ctx, r, "ceph", "fsid"); err != nil {
		return cfg, missing(err, "The ceph command")
	}
	if cfg.SSHPublicKey, err = trimmed(ctx, r, "ceph", "cephadm", "get-pub-key"); err != nil {
		return cfg, err
	}
	if _, err = r.Run(ctx, "ceph", "auth", "get-or-create", "client."+ClientUser,
		"mon", "profile rbd", "osd", "profile rbd", "mgr", "profile rbd"); err != nil {
		return cfg, err
	}
	cfg.ClientUser = ClientUser
	if cfg.ClientKey, err = trimmed(ctx, r, "ceph", "auth", "get-key", "client."+ClientUser); err != nil {
		return cfg, err
	}
	if cfg.MonEndpoints, err = monEndpoints(ctx, r); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// monEndpoints are the monitors' v1 addresses (host:6789), which libvirt understands.
func monEndpoints(ctx context.Context, r Runner) ([]string, error) {
	out, err := r.Run(ctx, "ceph", "mon", "dump", "-f", "json")
	if err != nil {
		return nil, err
	}
	var dump struct {
		Mons []struct {
			Name        string `json:"name"`
			Addr        string `json:"addr"`
			PublicAddrs struct {
				AddrVec []struct {
					Type string `json:"type"`
					Addr string `json:"addr"`
				} `json:"addrvec"`
			} `json:"public_addrs"`
		} `json:"mons"`
	}
	if err := json.Unmarshal(out, &dump); err != nil {
		return nil, fmt.Errorf("read ceph mon dump: %w", err)
	}
	var eps []string
	for _, m := range dump.Mons {
		addr := ""
		for _, a := range m.PublicAddrs.AddrVec {
			if a.Type == "v1" {
				addr = a.Addr
			}
		}
		if addr == "" {
			addr, _, _ = strings.Cut(m.Addr, "/")
		}
		if addr != "" {
			eps = append(eps, addr)
		}
	}
	if len(eps) == 0 {
		return nil, errors.New("the Ceph cluster lists no monitors")
	}
	return eps, nil
}

// AuthorizedKeysFile is where the cluster's SSH key goes so cephadm can manage this host.
var AuthorizedKeysFile = "/root/.ssh/authorized_keys"

var sshKeyRE = regexp.MustCompile(`^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp(256|384|521)) [A-Za-z0-9+/=]+( [^\n]*)?$`)

// AuthorizePeerKey lets the cluster's cephadm log in here as root, once.
func AuthorizePeerKey(pubKey string) error {
	pubKey = strings.TrimSpace(pubKey)
	if !sshKeyRE.MatchString(pubKey) {
		return invalidf("that is not an SSH public key")
	}
	if err := os.MkdirAll(filepath.Dir(AuthorizedKeysFile), 0o700); err != nil {
		return err
	}
	raw, err := os.ReadFile(AuthorizedKeysFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fields := strings.Fields(pubKey)
	for _, line := range strings.Split(string(raw), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == fields[0] && f[1] == fields[1] {
			return nil
		}
	}
	text := string(raw)
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	text += pubKey + "\n"
	tmp := AuthorizedKeysFile + ".virtomir.tmp"
	if err := os.WriteFile(tmp, []byte(text), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, AuthorizedKeysFile)
}

// CephHost is a host of the Ceph cluster as the orchestrator sees it.
type CephHost struct {
	Hostname string   `json:"hostname"`
	IP       string   `json:"ip"`
	Labels   []string `json:"labels"`
	Status   string   `json:"status"` // online, offline or maintenance
}

// Hosts lists the orchestrator's hosts.
func Hosts(ctx context.Context, r Runner) ([]CephHost, error) {
	out, err := r.Run(ctx, "ceph", "orch", "host", "ls", "-f", "json")
	if err != nil {
		return nil, missing(err, "The ceph command")
	}
	var raw []struct {
		Addr     string   `json:"addr"`
		Hostname string   `json:"hostname"`
		Labels   []string `json:"labels"`
		Status   string   `json:"status"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("read ceph orch host ls: %w", err)
	}
	hosts := make([]CephHost, 0, len(raw))
	for _, h := range raw {
		st := strings.ToLower(h.Status)
		if st == "" {
			st = "online"
		}
		hosts = append(hosts, CephHost{Hostname: h.Hostname, IP: h.Addr, Labels: append([]string{}, h.Labels...), Status: st})
	}
	return hosts, nil
}

// AddHost has the orchestrator take over a host; its root must already trust the
// cluster's SSH key (AuthorizePeerKey there).
func AddHost(ctx context.Context, r Runner, hostname, ip string) error {
	if !hostRE.MatchString(hostname) {
		return invalidf("%q is not a host name", hostname)
	}
	if _, err := netip.ParseAddr(ip); err != nil {
		return invalidf("%q is not an IP address", ip)
	}
	if _, err := r.Run(ctx, "ceph", "orch", "host", "add", hostname, ip); err != nil {
		return err
	}
	hosts, err := Hosts(ctx, r)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(hosts, func(h CephHost) bool { return h.Hostname == hostname }) {
		return fmt.Errorf("ceph accepted host %s but does not list it", hostname)
	}
	return nil
}

// AddOSD makes an OSD of a disk on a host. disk is what that host reported about it (the
// API asks the host itself); a disk that is not empty is refused here, and ceph-volume
// refuses it again.
func AddOSD(ctx context.Context, r Runner, hostname string, disk DiskInfo) error {
	if !hostRE.MatchString(hostname) {
		return invalidf("%q is not a host name", hostname)
	}
	if !blockDevRE.MatchString(disk.Path) || strings.Contains(disk.Path, "..") {
		return invalidf("%q is not a disk path like /dev/sdb", disk.Path)
	}
	if !disk.Empty {
		return fmt.Errorf("%w: disk %s on %s is not empty (%s), so it was left untouched. Wipe it yourself if you are sure (wipefs -a)", ErrDiskNotEmpty, disk.Path, hostname, disk.RefusalReason)
	}
	_, err := r.Run(ctx, "ceph", "orch", "daemon", "add", "osd", hostname+":"+disk.Path)
	return err
}
