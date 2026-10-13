package virt

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/digitalocean/go-libvirt"
)

// Action is a power/lifecycle operation on a domain.
type Action string

const (
	ActionStart    Action = "start"
	ActionShutdown Action = "shutdown"  // ACPI power button; the guest decides
	ActionReboot   Action = "reboot"    // ACPI reboot request
	ActionForceOff Action = "force-off" // virDomainDestroy: pulls the plug
	ActionPause    Action = "pause"     // virDomainSuspend: freezes vCPUs, RAM stays allocated
	ActionResume   Action = "resume"
)

func ParseAction(s string) (Action, bool) {
	switch a := Action(s); a {
	case ActionStart, ActionShutdown, ActionReboot, ActionForceOff, ActionPause, ActionResume:
		return a, true
	}
	return "", false
}

func parseUUID(s string) (libvirt.UUID, error) {
	var u libvirt.UUID
	b, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	if err != nil || len(b) != len(u) {
		return u, fmt.Errorf("%w: invalid UUID %q", ErrNotFound, s)
	}
	copy(u[:], b)
	return u, nil
}

// lookup resolves a UUID string to a domain on a live connection.
func (c *Client) lookup(uuid string) (*libvirt.Libvirt, libvirt.Domain, error) {
	u, err := parseUUID(uuid)
	if err != nil {
		return nil, libvirt.Domain{}, err
	}
	l, err := c.conn()
	if err != nil {
		return nil, libvirt.Domain{}, err
	}
	d, err := l.DomainLookupByUUID(u)
	if err != nil {
		return nil, libvirt.Domain{}, classify(err)
	}
	return l, d, nil
}

// DomainAction runs a lifecycle action on the domain with the given UUID.
func (c *Client) DomainAction(uuid string, action Action) error {
	l, d, err := c.lookup(uuid)
	if err != nil {
		return err
	}

	switch action {
	case ActionStart:
		err = l.DomainCreate(d)
	case ActionShutdown:
		err = l.DomainShutdown(d)
	case ActionReboot:
		err = l.DomainReboot(d, libvirt.DomainRebootDefault)
	case ActionForceOff:
		err = l.DomainDestroy(d)
	case ActionPause:
		err = l.DomainSuspend(d)
	case ActionResume:
		err = l.DomainResume(d)
	default:
		return fmt.Errorf("unknown action %q", action)
	}
	if err != nil {
		return classify(err)
	}
	c.log.Info("domain action", "domain", d.Name, "action", action)
	return nil
}

// HostStats is a point-in-time sample of host resource counters.
type HostStats struct {
	// Cumulative CPU time over all host CPUs, in nanoseconds.
	CPUBusyNs, CPUTotalNs uint64
	// Memory in KiB. Used excludes page cache and buffers (like `free`).
	MemTotalKiB, MemUsedKiB uint64
}

func (c *Client) HostStats() (HostStats, error) {
	l, err := c.conn()
	if err != nil {
		return HostStats{}, err
	}
	var s HostStats

	allCPUs := int32(libvirt.NodeCPUStatsAllCpus)
	_, n, err := l.NodeGetCPUStats(allCPUs, 0, 0)
	if err != nil {
		return HostStats{}, fmt.Errorf("node cpu stats: %w", err)
	}
	cpu, _, err := l.NodeGetCPUStats(allCPUs, n, 0)
	if err != nil {
		return HostStats{}, fmt.Errorf("node cpu stats: %w", err)
	}
	var idle uint64
	for _, p := range cpu {
		switch p.Field {
		case "kernel", "user", "iowait":
			s.CPUTotalNs += p.Value
			if p.Field != "iowait" {
				s.CPUBusyNs += p.Value
			}
		case "idle":
			idle = p.Value
		}
	}
	s.CPUTotalNs += idle

	allCells := int32(libvirt.NodeMemoryStatsAllCells)
	_, n, err = l.NodeGetMemoryStats(0, allCells, 0)
	if err != nil {
		return HostStats{}, fmt.Errorf("node memory stats: %w", err)
	}
	mem, _, err := l.NodeGetMemoryStats(n, allCells, 0)
	if err != nil {
		return HostStats{}, fmt.Errorf("node memory stats: %w", err)
	}
	var free, buffers, cached uint64
	for _, p := range mem {
		switch p.Field {
		case "total":
			s.MemTotalKiB = p.Value
		case "free":
			free = p.Value
		case "buffers":
			buffers = p.Value
		case "cached":
			cached = p.Value
		}
	}
	if avail := free + buffers + cached; avail <= s.MemTotalKiB {
		s.MemUsedKiB = s.MemTotalKiB - avail
	}
	return s, nil
}

// LifecycleEvents streams domain lifecycle changes (started, stopped, defined, …) until ctx
// is cancelled or the libvirt connection drops; then the channel is closed.
// Only the fact that something changed matters to callers, so the payload is the domain name.
func (c *Client) LifecycleEvents(ctx context.Context) (<-chan string, error) {
	l, err := c.conn()
	if err != nil {
		return nil, err
	}
	subCtx, cancel := context.WithCancel(ctx)
	in, err := l.LifecycleEvents(subCtx)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("subscribe to lifecycle events: %w", err)
	}
	out := make(chan string)
	go func() {
		defer close(out)
		defer func() {
			// go-libvirt's sender blocks on an unbuffered channel: cancel, then drain until it closes.
			cancel()
			for range in {
			}
		}()
		for {
			select {
			case ev, ok := <-in:
				if !ok {
					return
				}
				select {
				case out <- ev.Dom.Name:
				case <-ctx.Done():
					return
				}
			case <-l.Disconnected():
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}
