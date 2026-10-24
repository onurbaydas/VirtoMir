// Package ceph is VirtoMir's hyper-converged Ceph (D4, ADR-0073): bootstrapping a cluster
// with cephadm on the VirtoMir hosts, adding hosts and OSDs (only on empty disks), health
// and capacity telemetry, RBD pools that become libvirt pools on every member, storage
// policies that name a pool for new disks, and noout around host maintenance.
// It drives the stock tools (cephadm, ceph, rbd, lsblk) through a Runner, so every command
// it would run is tested without Ceph installed. Experimental: not tested on hardware.
package ceph

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
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
		if errors.Is(err, exec.ErrNotFound) {
			return out, err
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return out, fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), msg)
	}
	return out, nil
}

// Errors for the API.
var (
	ErrInvalid      = errors.New("invalid")
	ErrNotFound     = errors.New("not found")
	ErrExists       = errors.New("exists")
	ErrBusy         = errors.New("in use")
	ErrDiskNotEmpty = errors.New("disk not empty")
)

func invalidf(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, a...)...)
}
