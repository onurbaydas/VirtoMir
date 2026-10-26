package main

import (
	"context"
	"log/slog"
	"runtime"
	"time"

	"github.com/onurbaydas/virtomir/internal/overlay"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// keepTunnels gives the active VXLAN networks their tunnel devices (ADR-0054), after
// network changes and reboots alike, by checking every few seconds.
func keepTunnels(ctx context.Context, c *virt.Client, log *slog.Logger) {
	if runtime.GOOS != "linux" {
		return
	}
	r := overlay.New(overlay.Exec{}, log)
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	last := ""
	// Most hosts have no VXLAN network: after one clean-up of stale devices, an empty set
	// costs nothing more until a network appears.
	idle := false
	for {
		var msg string
		if nets, err := c.VXLANTunnels(); err == nil && !(idle && len(nets) == 0) {
			idle = len(nets) == 0
			want := make([]overlay.Tunnel, len(nets))
			for i, n := range nets {
				want[i] = overlay.Tunnel{VNI: n.VNI, Bridge: n.Bridge, Peers: n.Peers}
			}
			if err := r.Reconcile(ctx, want); err != nil {
				msg = err.Error()
			}
		}
		if msg != "" && msg != last {
			log.Warn("vxlan tunnels", "err", msg)
		}
		last = msg
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
