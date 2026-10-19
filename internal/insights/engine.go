// Package insights provides operations intelligence for VirtoMir: capacity
// forecasting, right-sizing and anomaly detection (ADR-0066).
//
// Persistence-free design: the Engine holds no state of its own. It recomputes
// every forecast on demand from the bounded history rings in a history.Store.
// The rings themselves are saved periodically by the daemon (see saveHistory),
// so a restart keeps the data the Engine queries. Other cluster members answer
// for their own hosts (the API forwards to them), so there is no cluster series.
package insights

import (
	"fmt"
	"time"

	"github.com/onurbaydas/virtomir/internal/history"
)

// Querier answers history queries (satisfied by *history.Store).
type Querier interface {
	Query(names []string, rng time.Duration, now time.Time) (history.Result, error)
}

// Machine describes a virtual machine for operations-intelligence queries.
type Machine struct {
	UUID    string
	Name    string
	VCPUs   int
	MemMiB  uint64
	Running bool
}

// Pool describes a storage pool for capacity forecasting.
type Pool struct {
	Name          string
	CapacityBytes uint64
}

// Series name constants mirror the live package's recording names so the
// Engine stays self-contained (no dependency on the live package).
const (
	seriesHostCPU = "host.cpu"
	seriesHostMem = "host.mem"
)

// vmSeries returns the name of a machine's series, e.g. vmSeries(uuid, "cpu").
func vmSeries(uuid, metric string) string { return "vm." + uuid + "." + metric }

// PoolSeries is the history series of a storage pool's used bytes; the daemon records it
// (see recordPools in cmd/virtomird).
func PoolSeries(pool string) string { return "pool." + pool + ".used" }

// Engine provides operations intelligence by querying bounded history rings on
// demand. The analysis window is 30 days, matching the coarsest history.Tier.
type Engine struct {
	q      Querier
	window time.Duration
}

// New returns an Engine backed by q.
func New(q Querier) *Engine {
	return &Engine{q: q, window: 30 * 24 * time.Hour}
}

// HostForecast forecasts this host's CPU and memory use (percentages, capacity 100).
func (e *Engine) HostForecast(now time.Time) (cpu, mem Forecast, err error) {
	res, err := e.q.Query([]string{seriesHostCPU, seriesHostMem}, e.window, now)
	if err != nil {
		return Forecast{}, Forecast{}, err
	}
	cpu = ForecastCapacity(Decode(res, seriesHostCPU), 100.0, 24*time.Hour, now)
	cpu.Metric = seriesHostCPU
	mem = ForecastCapacity(Decode(res, seriesHostMem), 100.0, 24*time.Hour, now)
	mem.Metric = seriesHostMem
	return cpu, mem, nil
}

// PoolForecast forecasts when a storage pool fills up, from its recorded used bytes.
func (e *Engine) PoolForecast(p Pool, now time.Time) (Forecast, error) {
	name := PoolSeries(p.Name)
	if p.CapacityBytes == 0 {
		return Forecast{Metric: name, InsufficientData: true, DaysUntilFull: -1}, nil
	}
	res, err := e.q.Query([]string{name}, e.window, now)
	if err != nil {
		return Forecast{}, err
	}
	f := ForecastCapacity(Decode(res, name), float64(p.CapacityBytes), 24*time.Hour, now)
	f.Metric = name
	return f, nil
}

// minRightSizeHistory: a machine is only resized from at least a week of its own use.
const minRightSizeHistory = 7 * 24 * time.Hour

// VMRightSize produces CPU and memory right-sizing recommendations for a machine from
// the engine window. Guest memory is recorded as a percentage of the balloon; without a
// guest memory series (no balloon driver reporting), memory is left as it is.
func (e *Engine) VMRightSize(m Machine, now time.Time) Recommendation {
	cpuName := vmSeries(m.UUID, "cpu")
	memName := vmSeries(m.UUID, "mem")
	res, err := e.q.Query([]string{cpuName, memName}, e.window, now)
	if err != nil {
		return Recommendation{
			UUID:            m.UUID,
			CurrentVCPUs:    m.VCPUs,
			CurrentMemMiB:   m.MemMiB,
			SuggestedVCPUs:  m.VCPUs,
			SuggestedMemMiB: m.MemMiB,
			Reasons:         []string{fmt.Sprintf("history query failed: %v", err)},
		}
	}

	cpuPts := Decode(res, cpuName)
	if len(cpuPts) < 2 || cpuPts[len(cpuPts)-1].T.Sub(cpuPts[0].T) < minRightSizeHistory {
		return Recommendation{
			UUID: m.UUID, CurrentVCPUs: m.VCPUs, CurrentMemMiB: m.MemMiB, SuggestedVCPUs: m.VCPUs, SuggestedMemMiB: m.MemMiB,
			Reasons: []string{"not enough history yet: right-sizing needs 7 days of the machine running"},
		}
	}
	cpuVals := pointsToValues(cpuPts)
	memVals := pointsToValues(Decode(res, memName))

	rec := RightSize(cpuVals, memVals, struct {
		VCPUs  int
		MemMiB uint64
	}{VCPUs: m.VCPUs, MemMiB: m.MemMiB}, 0)

	rec.UUID = m.UUID
	if len(memVals) == 0 {
		rec.Reasons = append(rec.Reasons, "guest agent memory data unavailable")
	}
	return rec
}

// VMDetect runs anomaly detection over the last 24 h for six VM metric series,
// returning anomalies keyed by their short metric name (e.g. "disk.latency").
func (e *Engine) VMDetect(m Machine, now time.Time) map[string][]Anomaly {
	metrics := []string{"cpu", "disk.latency", "disk.read", "disk.write", "net.rx", "net.tx"}
	var names []string
	for _, metric := range metrics {
		names = append(names, vmSeries(m.UUID, metric))
	}
	res, err := e.q.Query(names, 24*time.Hour, now)
	if err != nil {
		return make(map[string][]Anomaly)
	}

	result := make(map[string][]Anomaly)
	for _, metric := range metrics {
		seriesName := vmSeries(m.UUID, metric)
		pts := Decode(res, seriesName)
		var keep []Anomaly
		for _, a := range Detect(pts, seriesName, 24*time.Hour, 5.0, now) {
			if significant(metric, a) {
				keep = append(keep, a)
			}
		}
		if len(keep) > 0 {
			result[metric] = keep
		}
	}
	return result
}

// significant reports whether an anomaly is a rise big enough to matter. A nearly constant
// series has a tiny MAD, so noise on an idle machine (26 → 104 B/s) scores z in the millions;
// a real problem also moves the value by an absolute amount.
func significant(metric string, a Anomaly) bool {
	rise, ratio := a.Value-a.BaselineMedian, 0.0
	if a.BaselineMedian > 0 {
		ratio = a.Value / a.BaselineMedian
	}
	switch metric {
	case "cpu":
		return rise >= 20 // percentage points of the machine's vCPUs
	case "disk.latency":
		return rise >= 10 && (ratio == 0 || ratio >= 2) // ms
	default: // bytes per second
		return rise >= 5<<20 && (ratio == 0 || ratio >= 3)
	}
}

// pointsToValues extracts the float64 values from a slice of Points, preserving
// order.
func pointsToValues(pts []Point) []float64 {
	vals := make([]float64, len(pts))
	for i, p := range pts {
		vals[i] = p.V
	}
	return vals
}
