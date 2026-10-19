package insights

import (
	"math"
	"sort"
	"time"
)

// Anomaly records an identified statistical anomaly in a metric series.
type Anomaly struct {
	Time           time.Time `json:"time"`
	Metric         string    `json:"metric"`
	Value          float64   `json:"value"`
	BaselineMedian float64   `json:"baselineMedian"`
	Z              float64   `json:"z"`
}

// Median calculates the 50th percentile of vs.
func Median(vs []float64) float64 {
	n := len(vs)
	if n == 0 {
		return 0
	}
	clean := make([]float64, 0, n)
	for _, v := range vs {
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			clean = append(clean, v)
		}
	}
	cn := len(clean)
	if cn == 0 {
		return 0
	}
	sort.Float64s(clean)
	if cn%2 == 1 {
		return clean[cn/2]
	}
	return (clean[cn/2-1] + clean[cn/2]) / 2.0
}

// MADZ computes the robust z-score: 0.6745*(v - median) / MAD.
// If MAD == 0, it falls back to standard z = (v - mean)/stddev (or 0 if stddev == 0).
func MADZ(vs []float64, v float64) float64 {
	clean := make([]float64, 0, len(vs))
	var sum float64
	for _, x := range vs {
		if !math.IsNaN(x) && !math.IsInf(x, 0) {
			clean = append(clean, x)
			sum += x
		}
	}
	n := len(clean)
	if n == 0 {
		return 0
	}

	med := Median(clean)
	devs := make([]float64, n)
	for i, x := range clean {
		devs[i] = math.Abs(x - med)
	}
	mad := Median(devs)

	if mad > 0 {
		return 0.6745 * (v - med) / mad
	}

	// Fallback to standard z = (v - mean) / stddev
	mean := sum / float64(n)
	var sumSq float64
	for _, x := range clean {
		d := x - mean
		sumSq += d * d
	}
	stddev := math.Sqrt(sumSq / float64(n))
	if stddev == 0 {
		return 0
	}
	return (v - mean) / stddev
}

// Detect identifies metric anomalies against a baseline computed over trailing baselineWindow (default 24h).
// Only points newer than baseline start are evaluated. An anomaly triggers when |z| >= threshold (default 5.0)
// AND value > baseline p99 (to avoid false positives on flat series with tiny MADs).
// Multiple triggers within the same 15-minute bucket are suppressed, keeping only the point with highest |z|.
func Detect(pts []Point, metric string, baselineWindow time.Duration, threshold float64, now time.Time) []Anomaly {
	if len(pts) == 0 {
		return nil
	}
	if baselineWindow <= 0 {
		baselineWindow = 24 * time.Hour
	}
	if threshold <= 0 {
		threshold = 5.0
	}
	if now.IsZero() {
		now = pts[len(pts)-1].T
	}
	baselineStart := now.Add(-baselineWindow)

	var baselineVals []float64
	var evalPts []Point
	for _, p := range pts {
		if !p.T.Before(baselineStart) && !p.T.After(now) {
			baselineVals = append(baselineVals, p.V)
			evalPts = append(evalPts, p)
		}
	}

	if len(baselineVals) == 0 {
		return nil
	}

	med := Median(baselineVals)
	usage := Percentiles(baselineVals)
	p99 := usage.P99

	bucketMap := make(map[int64]Anomaly)

	for _, p := range evalPts {
		z := MADZ(baselineVals, p.V)
		if math.Abs(z) >= threshold && p.V > p99 {
			bucket := p.T.Unix() / (15 * 60)
			existing, ok := bucketMap[bucket]
			if !ok || math.Abs(z) > math.Abs(existing.Z) {
				bucketMap[bucket] = Anomaly{
					Time:           p.T,
					Metric:         metric,
					Value:          p.V,
					BaselineMedian: med,
					Z:              z,
				}
			}
		}
	}

	anomalies := make([]Anomaly, 0, len(bucketMap))
	for _, a := range bucketMap {
		anomalies = append(anomalies, a)
	}
	sort.Slice(anomalies, func(i, j int) bool {
		return anomalies[i].Time.Before(anomalies[j].Time)
	})
	return anomalies
}
