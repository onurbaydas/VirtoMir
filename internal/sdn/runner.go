// Package sdn is VirtoMir's software-defined networking on OVN/OVS (D3, ADR-0072): per-
// project VPCs with distributed routing, OVN-native DHCP, SNAT and floating IPs, security
// groups as port groups with stateful ACLs, load balancers, and IPsec for the tunnels.
// It drives the stock tools (ovs-vsctl, ovn-nbctl, ovn-sbctl) through a Runner, so every
// command it would run is tested without OVN installed. Experimental: not tested on a
// real OVN cluster yet.
package sdn

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

// NB runs ovn-nbctl against the northbound database at db ("" = the local default).
type NB struct {
	R  Runner
	DB string
}

// Run runs ovn-nbctl with args.
func (n NB) Run(ctx context.Context, args ...string) ([]byte, error) {
	if n.DB != "" {
		args = append([]string{"--db=" + n.DB}, args...)
	}
	return n.R.Run(ctx, "ovn-nbctl", args...)
}
