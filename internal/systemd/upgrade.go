package systemd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// OS package upgrades for the rolling upgrade (ADR-0070). Experimental: not tested on
// hardware yet, but the real thing: the upgrade runs in a transient systemd unit, so it
// survives virtomird restarting while packages change (VirtoMir's own package included),
// and the reboot is part of the same unit, so it happens only after the upgrade finished.

// LookPath finds the package manager; tests replace it.
var LookPath = exec.LookPath

// Upgrader upgrades the host's packages.
type Upgrader struct {
	Log *slog.Logger
	// Run runs a command and returns its combined output; tests replace it.
	Run func(ctx context.Context, name string, args ...string) (string, error)
}

func runCommand(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s: %v: %s", name, err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// upgradeCommand is the shell command that upgrades this host's packages.
func upgradeCommand() (string, error) {
	switch {
	case found("apt-get"):
		return "DEBIAN_FRONTEND=noninteractive apt-get update -q && DEBIAN_FRONTEND=noninteractive apt-get -o Dpkg::Options::=--force-confold -o Dpkg::Options::=--force-confdef upgrade -y -q", nil
	case found("dnf"):
		return "dnf upgrade -y -q", nil
	case found("apt"):
		return "DEBIAN_FRONTEND=noninteractive apt update && DEBIAN_FRONTEND=noninteractive apt upgrade -y", nil
	}
	return "", errors.New("this host has neither apt-get nor dnf, so VirtoMir cannot upgrade its packages")
}

func found(name string) bool {
	_, err := LookPath(name)
	return err == nil
}

// Upgrade upgrades every package and, with reboot, restarts the host afterwards. It waits
// for the upgrade to finish (a reboot ends the connection first) and returns the unit name.
func (u *Upgrader) Upgrade(ctx context.Context, reboot bool) (string, error) {
	cmd, err := upgradeCommand()
	if err != nil {
		return "", err
	}
	if reboot {
		cmd += " && systemctl reboot"
	}
	unit := fmt.Sprintf("virtomir-osupgrade-%d", time.Now().Unix())
	run := u.Run
	if run == nil {
		run = runCommand
	}
	if u.Log != nil {
		u.Log.Info("upgrading the host's packages", "unit", unit, "reboot", reboot)
	}
	if _, err := run(ctx, "systemd-run", "--unit="+unit, "--collect", "--quiet", "--wait", "--service-type=oneshot",
		"/bin/sh", "-c", cmd); err != nil {
		return unit, fmt.Errorf("the package upgrade failed (journalctl -u %s): %w", unit, err)
	}
	return unit, nil
}
