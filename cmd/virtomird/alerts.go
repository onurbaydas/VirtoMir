package main

import (
	"context"
	"sync"
	"time"

	"github.com/onurbaydas/virtomir/internal/alerts"
	"github.com/onurbaydas/virtomir/internal/live"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// watchAlerts feeds every live snapshot into the alert engine. Pool usage comes from a
// slower loop (once a minute), so listing pools never delays the 1 s sampler.
func watchAlerts(ctx context.Context, hub *live.Hub, eng *alerts.Engine, client *virt.Client) {
	var mu sync.Mutex
	var pools []alerts.Pool
	fresh := false

	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			if ps, err := client.ListPools(); err == nil {
				out := make([]alerts.Pool, 0, len(ps))
				for _, p := range ps {
					free := 0.0
					if p.CapacityBytes > 0 {
						free = float64(p.AvailableBytes) / float64(p.CapacityBytes) * 100
					}
					out = append(out, alerts.Pool{Name: p.Name, Active: p.Active, FreePct: free, FreeBytes: p.AvailableBytes, TotalBytes: p.CapacityBytes})
				}
				mu.Lock()
				pools, fresh = out, true
				mu.Unlock()
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()

	hub.AddObserver(func(s live.Snapshot) {
		o := alerts.Observation{Time: s.Time, Connected: s.Connected, HostCPU: s.Host.CPUPercent}
		if s.Host.MemTotalKiB > 0 {
			o.HostMem = float64(s.Host.MemUsedKiB) / float64(s.Host.MemTotalKiB) * 100
		}
		for _, d := range s.Domains {
			o.Machines = append(o.Machines, alerts.Machine{UUID: d.UUID, Name: d.Name, State: string(d.State), Autostart: d.Autostart, CPU: d.CPUPercent})
		}
		mu.Lock()
		if fresh {
			o.Pools, o.PoolsFresh, fresh = pools, true, false
		}
		mu.Unlock()
		eng.Observe(o)
	})
}
