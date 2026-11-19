package virt

import (
	"errors"
	"testing"
)

func TestCheckGrowNeverShrinks(t *testing.T) {
	const gib = 1 << 30
	if err := checkGrow(30*gib, 20*gib, true); err != nil {
		t.Errorf("growing 20 → 30 GiB: %v", err)
	}
	for _, c := range []struct{ to, now uint64 }{{20 * gib, 20 * gib}, {10 * gib, 20 * gib}} {
		var inv *InvalidError
		if err := checkGrow(c.to, c.now, true); !errors.As(err, &inv) {
			t.Errorf("%d → %d GiB must be refused as invalid, got %v", c.now/gib, c.to/gib, err)
		}
	}
	// Size unknown (not in a pool, QEMU did not answer): nothing is resized.
	if err := checkGrow(30*gib, 0, false); err == nil {
		t.Error("an unknown current size must refuse the resize")
	}
}
