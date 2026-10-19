package insights

import (
	"fmt"
	"math"
	"sort"
)

// Usage contains percentile and summary metrics for resource utilization series.
type Usage struct {
	P95  float64
	P99  float64
	Mean float64
	Max  float64
}

// Percentiles calculates P95, P99, Mean, and Max of vs using linear interpolation.
func Percentiles(vs []float64) Usage {
	n := len(vs)
	if n == 0 {
		return Usage{}
	}
	clean := make([]float64, 0, n)
	var sum float64
	for _, v := range vs {
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			clean = append(clean, v)
			sum += v
		}
	}
	cn := len(clean)
	if cn == 0 {
		return Usage{}
	}
	sort.Float64s(clean)
	mean := sum / float64(cn)
	maxVal := clean[cn-1]

	calcP := func(p float64) float64 {
		if cn == 1 {
			return clean[0]
		}
		pos := (p / 100.0) * float64(cn-1)
		k := int(math.Floor(pos))
		f := pos - float64(k)
		if k >= cn-1 {
			return clean[cn-1]
		}
		return clean[k] + f*(clean[k+1]-clean[k])
	}

	return Usage{
		P95:  calcP(95.0),
		P99:  calcP(99.0),
		Mean: mean,
		Max:  maxVal,
	}
}

// Recommendation holds sizing advice and human-readable rationale for a virtual machine.
type Recommendation struct {
	UUID            string   `json:"uuid"`
	CurrentVCPUs    int      `json:"currentVcpus"`
	CurrentMemMiB   uint64   `json:"currentMemMiB"`
	SuggestedVCPUs  int      `json:"suggestedVcpus"`
	SuggestedMemMiB uint64   `json:"suggestedMemMiB"`
	Oversized       bool     `json:"oversized"`
	Undersized      bool     `json:"undersized"`
	Reasons         []string `json:"reasons"`
}

// RightSize evaluates CPU and guest memory utilization against right-sizing thresholds.
// cpuSamples and guestMemSamples are percentage values (0 to 100).
func RightSize(cpuSamples, guestMemSamples []float64, cur struct {
	VCPUs  int
	MemMiB uint64
}, hostMaxVCPUs int) Recommendation {
	rec := Recommendation{
		CurrentVCPUs:    cur.VCPUs,
		CurrentMemMiB:   cur.MemMiB,
		SuggestedVCPUs:  cur.VCPUs,
		SuggestedMemMiB: cur.MemMiB,
		Reasons:         []string{},
	}

	// CPU Right-sizing
	if len(cpuSamples) > 0 {
		cpuUsage := Percentiles(cpuSamples)
		if cpuUsage.P95 < 15.0 && cpuUsage.P99 < 35.0 {
			target := int(math.Ceil(cpuUsage.P99 / 100.0 * float64(cur.VCPUs)))
			if target < 1 {
				target = 1
			}
			if target < cur.VCPUs {
				rec.SuggestedVCPUs = target
				rec.Reasons = append(rec.Reasons, fmt.Sprintf(
					"CPU is oversized (p95=%.1f%% < 15%%, p99=%.1f%% < 35%%): suggest reducing from %d to %d vCPUs",
					cpuUsage.P95, cpuUsage.P99, cur.VCPUs, target,
				))
			}
		} else if cpuUsage.P95 > 85.0 {
			target := int(math.Ceil(cpuUsage.P95 / 80.0 * float64(cur.VCPUs)))
			if hostMaxVCPUs > 0 && target > hostMaxVCPUs {
				target = hostMaxVCPUs
			}
			if target > cur.VCPUs {
				rec.SuggestedVCPUs = target
				rec.Reasons = append(rec.Reasons, fmt.Sprintf(
					"CPU is undersized (p95=%.1f%% > 85%%): suggest increasing from %d to %d vCPUs",
					cpuUsage.P95, cur.VCPUs, target,
				))
			}
		}
	}

	// Memory Right-sizing
	if len(guestMemSamples) > 0 {
		memUsage := Percentiles(guestMemSamples)
		if memUsage.P95 < 40.0 {
			raw := math.Ceil(memUsage.P95/100.0*float64(cur.MemMiB)/128.0) * 128.0
			target := uint64(raw)
			if target < 512 {
				target = 512
			}
			if target < cur.MemMiB {
				rec.SuggestedMemMiB = target
				rec.Reasons = append(rec.Reasons, fmt.Sprintf(
					"Memory is oversized (p95=%.1f%% < 40%%): suggest reducing from %d MiB to %d MiB",
					memUsage.P95, cur.MemMiB, target,
				))
			}
		} else if memUsage.P95 > 90.0 {
			raw := math.Ceil(float64(cur.MemMiB)*1.25/128.0) * 128.0
			target := uint64(raw)
			if target > cur.MemMiB {
				rec.SuggestedMemMiB = target
				rec.Reasons = append(rec.Reasons, fmt.Sprintf(
					"Memory is undersized (p95=%.1f%% > 90%%): suggest increasing from %d MiB to %d MiB",
					memUsage.P95, cur.MemMiB, target,
				))
			}
		}
	}

	// Never below 1 vCPU. Memory is floored at 512 MiB only when reducing (above): a machine
	// that already has less is left alone, never "grown" by the floor.
	if rec.SuggestedVCPUs < 1 {
		rec.SuggestedVCPUs = 1
	}

	rec.Oversized = rec.SuggestedVCPUs < cur.VCPUs || rec.SuggestedMemMiB < cur.MemMiB
	rec.Undersized = rec.SuggestedVCPUs > cur.VCPUs || rec.SuggestedMemMiB > cur.MemMiB

	return rec
}

// EstimatedSavings calculates total monetary savings by multiplying total GiB of memory freed
// by memPricePerGiB.
func EstimatedSavings(recs []Recommendation, memPricePerGiB float64) float64 {
	var totalGiBFreed float64
	for _, r := range recs {
		if r.SuggestedMemMiB < r.CurrentMemMiB {
			freedMiB := r.CurrentMemMiB - r.SuggestedMemMiB
			totalGiBFreed += float64(freedMiB) / 1024.0
		}
	}
	return totalGiBFreed * memPricePerGiB
}
