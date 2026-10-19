package main

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/onurbaydas/virtomir/internal/alerts"
	"github.com/onurbaydas/virtomir/internal/insights"
	"github.com/onurbaydas/virtomir/internal/live"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// anomalyRecent: anomalies older than this no longer keep a vm.anomaly alert firing.
const anomalyRecent = 15 * time.Minute

// watchAlerts feeds every live snapshot into the alert engine. Pool usage comes from a
// slower loop (once a minute) and metric anomalies from one every 5 minutes, so neither
// ever delays the 1 s sampler.
func watchAlerts(ctx context.Context, hub *live.Hub, eng *alerts.Engine, client *virt.Client, ins *insights.Engine) {
	var mu sync.Mutex
	var pools []alerts.Pool
	fresh := false
	anomalies := map[string]string{} // machine UUID → what is unusual right now

	go func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			doms, err := client.ListDomains()
			if err != nil {
				continue
			}
			now := time.Now()
			found := map[string]string{}
			for _, d := range doms {
				if d.State != virt.StateRunning {
					continue
				}
				m := insights.Machine{UUID: d.UUID, Name: d.Name, VCPUs: d.VCPUs, Running: true}
				if msg := recentAnomaly(d.Name, ins.VMDetect(m, now), now); msg != "" {
					found[d.UUID] = msg
				}
			}
			mu.Lock()
			anomalies = found
			mu.Unlock()
		}
	}()

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
		mu.Lock()
		for _, d := range s.Domains {
			o.Machines = append(o.Machines, alerts.Machine{UUID: d.UUID, Name: d.Name, State: string(d.State), Autostart: d.Autostart, CPU: d.CPUPercent,
				Anomaly: anomalies[d.UUID]})
		}
		if fresh {
			o.Pools, o.PoolsFresh, fresh = pools, true, false
		}
		mu.Unlock()
		eng.Observe(o)
	})
}

// recentAnomaly describes the strongest anomaly of the last anomalyRecent, or "".
func recentAnomaly(name string, found map[string][]insights.Anomaly, now time.Time) string {
	var best *insights.Anomaly
	metric := ""
	for m, list := range found {
		for i := range list {
			a := &list[i]
			if now.Sub(a.Time) <= anomalyRecent && (best == nil || math.Abs(a.Z) > math.Abs(best.Z)) {
				best, metric = a, m
			}
		}
	}
	if best == nil {
		return ""
	}
	return fmt.Sprintf("%s: %s is %.4g, usually %.4g (robust z-score %.1f)", name, metric, best.Value, best.BaselineMedian, best.Z)
}
