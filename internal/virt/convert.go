package virt

import (
	"fmt"

	"github.com/digitalocean/go-libvirt"
)

// DomainState is libvirt's virDomainState as a stable, human-readable string.
type DomainState string

const (
	StateUnknown      DomainState = "unknown"
	StateRunning      DomainState = "running"
	StateBlocked      DomainState = "blocked"
	StatePaused       DomainState = "paused"
	StateShuttingDown DomainState = "shutting-down"
	StateShutoff      DomainState = "shutoff"
	StateCrashed      DomainState = "crashed"
	StateSuspended    DomainState = "pm-suspended"
)

func domainStateFrom(s libvirt.DomainState) DomainState {
	switch s {
	case libvirt.DomainRunning:
		return StateRunning
	case libvirt.DomainBlocked:
		return StateBlocked
	case libvirt.DomainPaused:
		return StatePaused
	case libvirt.DomainShutdown:
		return StateShuttingDown
	case libvirt.DomainShutoff:
		return StateShutoff
	case libvirt.DomainCrashed:
		return StateCrashed
	case libvirt.DomainPmsuspended:
		return StateSuspended
	default:
		return StateUnknown
	}
}

// formatVersion decodes libvirt's packed version (major*1_000_000 + minor*1_000 + release).
func formatVersion(v uint64) string {
	return fmt.Sprintf("%d.%d.%d", v/1_000_000, v/1_000%1_000, v%1_000)
}

func formatUUID(u libvirt.UUID) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}

// int8sToString converts a NUL-terminated C char array.
func int8sToString(b []int8) string {
	out := make([]byte, 0, len(b))
	for _, c := range b {
		if c == 0 {
			break
		}
		out = append(out, byte(c))
	}
	return string(out)
}
