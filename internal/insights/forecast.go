// Package insights provides operations intelligence math for VirtoMir:
// capacity forecasting with linear and seasonal decomposition,
// right-sizing recommendations based on usage percentiles, and
// robust anomaly detection using MAD z-scores (ADR-0066).
package insights

import (
	"math"
	"sort"
	"time"

	"github.com/onurbaydas/virtomir/internal/history"
)

// Point represents a single timestamped metric value.
type Point struct {
	T time.Time
	V float64
}

// Forecast holds capacity forecasting results for a metric series.
type Forecast struct {
	Metric           string  `json:"metric"`
	Slope            float64 `json:"slope"`
	Intercept        float64 `json:"intercept"`
	DaysUntilFull    float64 `json:"daysUntilFull"`
	Confidence       float64 `json:"confidence"`
	InsufficientData bool    `json:"insufficientData"`
	Seasonal         bool    `json:"seasonal"`
}

// Decode extracts valid (non-nil, non-NaN, non-Inf) points for the named series
// from a history query result, sorted by timestamp (res.Start + int64(i)*res.Step).
func Decode(res history.Result, name string) []Point {
	vals, ok := res.Series[name]
	if !ok || len(vals) == 0 {
		return nil
	}
	pts := make([]Point, 0, len(vals))
	for i, ptr := range vals {
		if ptr == nil {
			continue
		}
		v := *ptr
		if math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		t := time.Unix(res.Start+int64(i)*res.Step, 0).UTC()
		pts = append(pts, Point{T: t, V: v})
	}
	sort.Slice(pts, func(i, j int) bool {
		return pts[i].T.Before(pts[j].T)
	})
	return pts
}

// LinearTrend computes ordinary least squares linear regression of values vs time in days
// (relative to the first point). Slope is returned in units per day.
// r is the Pearson correlation coefficient.
func LinearTrend(pts []Point) (slope, intercept, r float64) {
	n := len(pts)
	if n < 2 {
		if n == 1 {
			return 0, pts[0].V, 0
		}
		return 0, 0, 0
	}

	t0 := pts[0].T
	var sumX, sumY float64
	xVals := make([]float64, n)
	for i, p := range pts {
		x := p.T.Sub(t0).Seconds() / 86400.0
		xVals[i] = x
		sumX += x
		sumY += p.V
	}
	meanX := sumX / float64(n)
	meanY := sumY / float64(n)

	var ssxx, ssyy, ssxy float64
	for i, p := range pts {
		dx := xVals[i] - meanX
		dy := p.V - meanY
		ssxx += dx * dx
		ssyy += dy * dy
		ssxy += dx * dy
	}

	if ssxx <= 0 {
		return 0, meanY, 0
	}

	slope = ssxy / ssxx
	intercept = meanY - slope*meanX

	if ssyy <= 0 {
		r = 0
	} else {
		denom := math.Sqrt(ssxx * ssyy)
		if denom > 0 {
			r = ssxy / denom
			if r > 1 {
				r = 1
			} else if r < -1 {
				r = -1
			}
		}
	}
	return slope, intercept, r
}

// SeasonalDecompose decomposes a time series into trend, seasonal, and residual components
// using moving-average decomposition over the specified period (e.g. 24h daily or 7d weekly).
func SeasonalDecompose(pts []Point, period time.Duration) (trend []float64, seasonal []float64, residualVar float64) {
	n := len(pts)
	if n < 2 {
		return nil, nil, 0
	}
	if period <= 0 {
		period = 24 * time.Hour
	}

	span := pts[n-1].T.Sub(pts[0].T)
	dt := span.Seconds() / float64(n-1)
	if dt <= 0 {
		return nil, nil, 0
	}

	m := int(math.Round(period.Seconds() / dt))
	if m < 2 || m > n {
		olsSlope, olsIntercept, _ := LinearTrend(pts)
		trend = make([]float64, n)
		seasonal = make([]float64, n)
		t0 := pts[0].T
		var sumSq float64
		for i, p := range pts {
			x := p.T.Sub(t0).Seconds() / 86400.0
			trend[i] = olsSlope*x + olsIntercept
			diff := p.V - trend[i]
			sumSq += diff * diff
		}
		return trend, seasonal, sumSq / float64(n)
	}

	k := m / 2
	trend = make([]float64, n)
	t0 := pts[0].T

	if m%2 == 1 {
		invM := 1.0 / float64(m)
		for i := k; i <= n-1-k; i++ {
			var s float64
			for j := -k; j <= k; j++ {
				s += pts[i+j].V
			}
			trend[i] = s * invM
		}
	} else {
		invM := 1.0 / float64(m)
		for i := k; i <= n-1-k; i++ {
			s := 0.5*pts[i-k].V + 0.5*pts[i+k].V
			for j := -k + 1; j <= k-1; j++ {
				s += pts[i+j].V
			}
			trend[i] = s * invM
		}
	}

	// Fit an OLS line on valid trend points to extrapolate the edges smoothly
	countValid := n - 2*k
	if countValid >= 2 {
		var tSumX, tSumY float64
		for i := k; i <= n-1-k; i++ {
			x := pts[i].T.Sub(t0).Seconds() / 86400.0
			tSumX += x
			tSumY += trend[i]
		}
		tMeanX := tSumX / float64(countValid)
		tMeanY := tSumY / float64(countValid)
		var tSSXX, tSSXY float64
		for i := k; i <= n-1-k; i++ {
			x := pts[i].T.Sub(t0).Seconds() / 86400.0
			dx := x - tMeanX
			dy := trend[i] - tMeanY
			tSSXX += dx * dx
			tSSXY += dx * dy
		}
		var tSlope, tIntercept float64
		if tSSXX > 0 {
			tSlope = tSSXY / tSSXX
			tIntercept = tMeanY - tSlope*tMeanX
		} else {
			tSlope = 0
			tIntercept = tMeanY
		}
		for i := 0; i < k; i++ {
			x := pts[i].T.Sub(t0).Seconds() / 86400.0
			trend[i] = tSlope*x + tIntercept
		}
		for i := n - k; i < n; i++ {
			x := pts[i].T.Sub(t0).Seconds() / 86400.0
			trend[i] = tSlope*x + tIntercept
		}
	} else {
		for i := 0; i < k; i++ {
			trend[i] = trend[k]
		}
		for i := n - k; i < n; i++ {
			trend[i] = trend[n-1-k]
		}
	}

	detrended := make([]float64, n)
	for i := range pts {
		detrended[i] = pts[i].V - trend[i]
	}

	phaseSum := make([]float64, m)
	phaseCount := make([]int, m)
	for i := range pts {
		p := i % m
		phaseSum[p] += detrended[i]
		phaseCount[p]++
	}

	phaseAvg := make([]float64, m)
	var sumPhaseAvg float64
	for p := 0; p < m; p++ {
		if phaseCount[p] > 0 {
			phaseAvg[p] = phaseSum[p] / float64(phaseCount[p])
		}
		sumPhaseAvg += phaseAvg[p]
	}
	meanSeasonal := sumPhaseAvg / float64(m)
	for p := 0; p < m; p++ {
		phaseAvg[p] -= meanSeasonal
	}

	seasonal = make([]float64, n)
	for i := range pts {
		seasonal[i] = phaseAvg[i%m]
	}

	var sumRes float64
	for i := range pts {
		res := pts[i].V - trend[i] - seasonal[i]
		sumRes += res
	}
	meanRes := sumRes / float64(n)
	var sumResSq float64
	for i := range pts {
		res := pts[i].V - trend[i] - seasonal[i]
		diff := res - meanRes
		sumResSq += diff * diff
	}
	residualVar = sumResSq / float64(n)

	return trend, seasonal, residualVar
}

// ForecastCapacity evaluates capacity exhaustion runway. If the time series spans < 14 days
// or contains < 50 points, it returns Forecast{InsufficientData: true, DaysUntilFull: -1}.
// If span >= 3*seasonalPeriod and seasonal residual variance drops vs plain linear, seasonal
// decomposition is applied (Seasonal=true), otherwise plain OLS is used.
func ForecastCapacity(pts []Point, capacity float64, seasonalPeriod time.Duration, now time.Time) Forecast {
	n := len(pts)
	if n < 50 {
		return Forecast{InsufficientData: true, DaysUntilFull: -1}
	}

	span := pts[n-1].T.Sub(pts[0].T)
	if span < 14*24*time.Hour {
		return Forecast{InsufficientData: true, DaysUntilFull: -1}
	}

	olsSlope, olsIntercept, olsR := LinearTrend(pts)
	t0 := pts[0].T

	var sumVal float64
	for _, p := range pts {
		sumVal += p.V
	}
	meanVal := sumVal / float64(n)

	var sumOlsDiffSq, sumValSq float64
	for _, p := range pts {
		x := p.T.Sub(t0).Seconds() / 86400.0
		pred := olsSlope*x + olsIntercept
		diff := p.V - pred
		sumOlsDiffSq += diff * diff
		vDiff := p.V - meanVal
		sumValSq += vDiff * vDiff
	}
	varOLS := sumOlsDiffSq / float64(n)
	valueVar := sumValSq / float64(n)
	valueStdDev := math.Sqrt(valueVar)

	useSeasonal := false
	var finalSlope, finalIntercept, finalR, finalResidualVar float64

	if seasonalPeriod > 0 && span >= 3*seasonalPeriod {
		trend, _, sResVar := SeasonalDecompose(pts, seasonalPeriod)
		if len(trend) == n && sResVar < varOLS-1e-9 {
			useSeasonal = true
			finalResidualVar = sResVar
			trendPts := make([]Point, n)
			for i := range pts {
				trendPts[i] = Point{T: pts[i].T, V: trend[i]}
			}
			finalSlope, finalIntercept, finalR = LinearTrend(trendPts)
		}
	}

	if !useSeasonal {
		finalSlope = olsSlope
		finalIntercept = olsIntercept
		finalR = olsR
		finalResidualVar = varOLS
	}

	current := pts[n-1].V
	var daysUntilFull float64
	if current >= capacity {
		daysUntilFull = 0
	} else if finalSlope <= 0 {
		daysUntilFull = -1
	} else {
		daysUntilFull = (capacity - current) / finalSlope
	}

	residualStdDev := math.Sqrt(finalResidualVar)
	var fitConf float64
	if valueStdDev > 1e-12 {
		fitConf = 1.0 - residualStdDev/valueStdDev
	}
	conf := math.Max(math.Abs(finalR), fitConf)
	if conf < 0 {
		conf = 0
	}
	if conf > 1 {
		conf = 1
	}

	return Forecast{
		Slope:            finalSlope,
		Intercept:        finalIntercept,
		DaysUntilFull:    daysUntilFull,
		Confidence:       conf,
		InsufficientData: false,
		Seasonal:         useSeasonal,
	}
}
