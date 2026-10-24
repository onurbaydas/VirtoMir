// Package storageeff is VirtoMir's storage efficiency (D15, ADR-0074): ZFS compression and
// deduplication per pool (with the RAM warning dedup deserves), storage tiers as labels on
// pools that disks move between, and NVMe/TCP namespaces that become pools. It drives zfs,
// zpool and nvme through a Runner, so every command is tested without them installed.
// NVMe/TCP is experimental: not tested on hardware.
package storageeff

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
	ErrInvalid  = errors.New("invalid")
	ErrNotFound = errors.New("not found")
)

func invalidf(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, a...)...)
}

// missing turns "command not found" into words for people.
func missing(err error, what string) error {
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("%s is not installed on this host", what)
	}
	return err
}
