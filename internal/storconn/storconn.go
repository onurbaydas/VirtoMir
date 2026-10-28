// Package storconn finds network and SAN storage by itself and connects it: iSCSI targets,
// NFS exports and SMB shares on the networks this host is on, and Fibre Channel adapters
// with their LUNs. Nothing is typed: the host's subnets are scanned, each server is asked
// what it offers, and the tools a protocol needs are installed when missing.
package storconn

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Runner runs a command on the host (ceph.Exec does).
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Kinds of storage.
const (
	KindISCSI = "iscsi"
	KindNFS   = "nfs"
	KindSMB   = "smb"
	KindFC    = "fc"
)

// ErrInvalid marks a request that cannot be done as asked.
var ErrInvalid = errors.New("invalid request")

func invalidf(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// tool is a command a protocol needs and the package that has it.
type tool struct{ cmd, apt, dnf string }

var tools = map[string][]tool{
	KindISCSI:   {{"iscsiadm", "open-iscsi", "iscsi-initiator-utils"}},
	KindNFS:     {{"showmount", "nfs-common", "nfs-utils"}, {"mount.nfs", "nfs-common", "nfs-utils"}},
	KindSMB:     {{"smbclient", "smbclient", "samba-client"}, {"mount.cifs", "cifs-utils", "cifs-utils"}},
	"multipath": {{"multipath", "multipath-tools", "device-mapper-multipath"}},
}

// LookPath finds a command; tests replace it.
var LookPath = exec.LookPath

// Ensure installs what a protocol needs when it is missing (apt-get or dnf).
func Ensure(ctx context.Context, r Runner, kind string) error {
	var apt, dnf []string
	for _, t := range tools[kind] {
		if _, err := LookPath(t.cmd); err == nil {
			continue
		}
		apt, dnf = appendNew(apt, t.apt), appendNew(dnf, t.dnf)
	}
	if len(apt) == 0 {
		return nil
	}
	var cmd []string
	switch {
	case found("apt-get"):
		cmd = append([]string{"env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "install", "-y", "-q"}, apt...)
	case found("dnf"):
		cmd = append([]string{"dnf", "install", "-y", "-q"}, dnf...)
	default:
		return fmt.Errorf("%s needs %s, and this host has neither apt-get nor dnf to install it", kind, strings.Join(apt, ", "))
	}
	// In a unit of its own, outside virtomird's sandbox: with NoNewPrivileges the packages'
	// scripts cannot enter their SELinux domain (rpm_script_t) and the install fails. No
	// --pipe: SELinux does not let systemd take virtomird's pipes, and the bus call fails.
	unit := "virtomir-install-" + kind
	if _, err := r.Run(ctx, "systemd-run", append([]string{"--quiet", "--wait", "--collect", "--unit=" + unit}, cmd...)...); err != nil {
		return fmt.Errorf("install %s (journalctl -u %s): %w", strings.Join(dnf, ", "), unit, err)
	}
	return nil
}

func appendNew(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}

func found(name string) bool {
	_, err := LookPath(name)
	return err == nil
}
