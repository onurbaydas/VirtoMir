package insights

import (
	"math"
	"testing"
	"time"

	"github.com/onurbaydas/virtomir/internal/history"
)

func TestDecode(t *testing.T) {
	v1 := 10.5
	v2 := 20.0
	nan := math.NaN()
	inf := math.Inf(1)
	res := history.Result{
		Start: 1000,
		Step:  60,
		Series: map[string][]*float64{
			"test.cpu": {&v1, nil, &nan, &v2, &inf},
		},
	}

	pts := Decode(res, "test.cpu")
	if len(pts) != 2 {
		t.Fatalf("expected 2 decoded points, got %d", len(pts))
	}
	if pts[0].V != 10.5 || pts[0].T.Unix() != 1000 {
		t.Errorf("unexpected point 0: %+v", pts[0])
	}
	if pts[1].V != 20.0 || pts[1].T.Unix() != 1180 {
		t.Errorf("unexpected point 1: %+v", pts[1])
	}

	// Missing series
	if Decode(res, "unknown") != nil {
		t.Errorf("expected nil for unknown series")
	}
}

func TestLinearTrend(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Clean linear ramp: y = 2.5 * day + 10
	var pts []Point
	for i := 0; i <= 10; i++ {
		pts = append(pts, Point{
			T: t0.Add(time.Duration(i) * 24 * time.Hour),
			V: 10.0 + 2.5*float64(i),
		})
	}
	slope, intercept, r := LinearTrend(pts)
	if math.Abs(slope-2.5) > 1e-6 {
		t.Errorf("expected slope 2.5, got %f", slope)
	}
	if math.Abs(intercept-10.0) > 1e-6 {
		t.Errorf("expected intercept 10.0, got %f", intercept)
	}
	if math.Abs(r-1.0) > 1e-6 {
		t.Errorf("expected r 1.0, got %f", r)
	}

	// Flat line
	var flatPts []Point
	for i := 0; i <= 5; i++ {
		flatPts = append(flatPts, Point{
			T: t0.Add(time.Duration(i) * 24 * time.Hour),
			V: 42.0,
		})
	}
	slopeFlat, interceptFlat, rFlat := LinearTrend(flatPts)
	if slopeFlat != 0 {
		t.Errorf("expected flat slope 0, got %f", slopeFlat)
	}
	if interceptFlat != 42.0 {
		t.Errorf("expected flat intercept 42, got %f", interceptFlat)
	}
	if rFlat != 0 {
		t.Errorf("expected flat r 0, got %f", rFlat)
	}

	// Under 2 points
	s0, i0, r0 := LinearTrend(nil)
	if s0 != 0 || i0 != 0 || r0 != 0 {
		t.Errorf("expected zeros for nil points")
	}
}

func TestForecastCapacity_CleanLinearRamp(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// 20 days of data, 1 sample every hour (480 points)
	// y = 20 + 3.0 * day
	var pts []Point
	for h := 0; h < 20*24; h++ {
		day := float64(h) / 24.0
		pts = append(pts, Point{
			T: t0.Add(time.Duration(h) * time.Hour),
			V: 20.0 + 3.0*day,
		})
	}

	capacity := 140.0
	now := pts[len(pts)-1].T
	fc := ForecastCapacity(pts, capacity, 24*time.Hour, now)

	if fc.InsufficientData {
		t.Fatalf("expected InsufficientData=false, got true")
	}

	// Slope should match true slope (3.0 units/day) within +/- 5%
	errRatio := math.Abs(fc.Slope-3.0) / 3.0
	if errRatio > 0.05 {
		t.Errorf("expected slope within 5%% of 3.0, got %f (error: %.2f%%)", fc.Slope, errRatio*100)
	}

	// Confidence > 0.8
	if fc.Confidence <= 0.8 {
		t.Errorf("expected confidence > 0.8, got %f", fc.Confidence)
	}

	// Current is approx 20 + 3*20 = 80. Capacity is 140. Days until full ~ 60 / 3 = 20 days
	if fc.DaysUntilFull <= 0 {
		t.Errorf("expected positive DaysUntilFull, got %f", fc.DaysUntilFull)
	}
}

func TestForecastCapacity_FlatSeries(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// 20 days of flat 50.0
	var pts []Point
	for h := 0; h < 20*24; h++ {
		pts = append(pts, Point{
			T: t0.Add(time.Duration(h) * time.Hour),
			V: 50.0,
		})
	}

	fc := ForecastCapacity(pts, 100.0, 24*time.Hour, pts[len(pts)-1].T)
	if fc.InsufficientData {
		t.Fatalf("expected InsufficientData=false")
	}
	if fc.DaysUntilFull != -1 {
		t.Errorf("expected DaysUntilFull=-1 for flat series, got %f", fc.DaysUntilFull)
	}
}

func TestForecastCapacity_SyntheticSineWave(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// 21 days of hourly data (504 points) with strong 24h daily sine wave
	var pts []Point
	for h := 0; h < 21*24; h++ {
		day := float64(h) / 24.0
		// Trend is flat or slightly upward with a clear 24h sine wave
		v := 50.0 + 15.0*math.Sin(2.0*math.Pi*day)
		pts = append(pts, Point{
			T: t0.Add(time.Duration(h) * time.Hour),
			V: v,
		})
	}

	trend, seasonal, sResVar := SeasonalDecompose(pts, 24*time.Hour)
	if len(trend) != len(pts) || len(seasonal) != len(pts) {
		t.Fatalf("decomposition length mismatch")
	}

	// Calculate OLS residual variance
	s, i, _ := LinearTrend(pts)
	var sumOlsSq float64
	for _, p := range pts {
		x := p.T.Sub(t0).Seconds() / 86400.0
		diff := p.V - (s*x + i)
		sumOlsSq += diff * diff
	}
	olsVar := sumOlsSq / float64(len(pts))

	if sResVar >= olsVar {
		t.Errorf("expected seasonal residual variance (%f) < OLS variance (%f)", sResVar, olsVar)
	}

	fc := ForecastCapacity(pts, 200.0, 24*time.Hour, pts[len(pts)-1].T)
	if !fc.Seasonal {
		t.Errorf("expected Seasonal=true for 21-day sine wave, got false")
	}
}

func TestForecastCapacity_Under14Days(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// 10 days of data (span < 14 days)
	var pts []Point
	for h := 0; h < 10*24; h++ {
		pts = append(pts, Point{
			T: t0.Add(time.Duration(h) * time.Hour),
			V: 10.0 + float64(h),
		})
	}

	fc := ForecastCapacity(pts, 500.0, 24*time.Hour, pts[len(pts)-1].T)
	if !fc.InsufficientData {
		t.Errorf("expected InsufficientData=true for 10-day series, got false")
	}
	if fc.DaysUntilFull != -1 {
		t.Errorf("expected DaysUntilFull=-1, got %f", fc.DaysUntilFull)
	}

	// Series with span >= 14 days but < 50 points
	var sparsePts []Point
	for d := 0; d < 16; d++ {
		sparsePts = append(sparsePts, Point{
			T: t0.Add(time.Duration(d) * 24 * time.Hour),
			V: 10.0 + float64(d),
		})
	}
	fcSparse := ForecastCapacity(sparsePts, 500.0, 24*time.Hour, sparsePts[len(sparsePts)-1].T)
	if !fcSparse.InsufficientData {
		t.Errorf("expected InsufficientData=true for series with < 50 points, got false")
	}
}

func TestForecastCapacity_AlreadyFull(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var pts []Point
	for h := 0; h < 20*24; h++ {
		pts = append(pts, Point{
			T: t0.Add(time.Duration(h) * time.Hour),
			V: 100.0 + float64(h),
		})
	}

	fc := ForecastCapacity(pts, 50.0, 24*time.Hour, pts[len(pts)-1].T)
	if fc.DaysUntilFull != 0 {
		t.Errorf("expected DaysUntilFull=0 when already full, got %f", fc.DaysUntilFull)
	}
}
