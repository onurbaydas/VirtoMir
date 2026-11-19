package virt

import (
	"strings"
	"testing"
)

func TestMemoryFits(t *testing.T) {
	const host = 256 << 20 // 256 GiB in KiB
	for _, c := range []struct {
		mib, hostKiB uint64
		ok           bool
	}{
		{2048, host, true},
		{256 << 10, host, true},    // exactly all of it
		{256<<10 + 1, host, false}, // a MiB more
		{5000 << 10, host, false},  // 5000 GiB
		{5000 << 10, 0, true},      // host size unknown: no guess
	} {
		err := memoryFits(c.mib, c.hostKiB)
		if (err == nil) != c.ok {
			t.Errorf("memoryFits(%d MiB, %d KiB) = %v, want ok=%v", c.mib, c.hostKiB, err, c.ok)
		}
	}
	if err := memoryFits(5000<<10, host); err == nil || !strings.Contains(err.Error(), "256 GiB") {
		t.Errorf("message should name the host's memory: %v", err)
	}
}
