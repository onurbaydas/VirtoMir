// Package physnet connects VirtoMir to the physical network (D16, ADR-0075): SR-IOV
// virtual functions on the host's NICs, BGP (and EVPN for the VXLAN overlays) through FRR,
// IP address pools that hand machines fixed addresses, and dynamic DNS (RFC 2136 with
// TSIG) for them. Commands run through a Runner and sysfs through a root directory, so
// everything is tested without the hardware. SR-IOV and BGP are experimental: not tested
// on hardware.
package physnet

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
	ErrExists   = errors.New("exists")
	ErrBusy     = errors.New("in use")
	ErrConflict = errors.New("conflict")
)

func invalidf(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, a...)...)
}

func missing(err error, what string) error {
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("%s is not installed on this host", what)
	}
	return err
}
