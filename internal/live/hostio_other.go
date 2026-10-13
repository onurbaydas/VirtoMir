//go:build !linux

package live

// Host disk/network counters come from /proc, which only exists on Linux.
func defaultHostIO() func() (hostCounters, error) { return nil }
