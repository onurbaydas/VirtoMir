package insights

import (
	"math"
	"testing"
	"time"
)

func TestMADZ_ConstantSeries_Fallback(t *testing.T) {
	// Completely constant series: MAD = 0, stddev = 0 -> returns 0 without panic
	constSeries := []float64{10.0, 10.0, 10.0, 10.0, 10.0}
	zConst := MADZ(constSeries, 10.0)
	if zConst != 0 {
		t.Errorf("expected z=0 for constant series, got %f", zConst)
	}

	zConstSpike := MADZ(constSeries, 50.0)
	if zConstSpike != 0 {
		t.Errorf("expected z=0 for constant series with zero stddev, got %f", zConstSpike)
	}

	// Series with zero MAD but positive stddev: falls back to standard z without panic
	mostlyConst := []float64{10.0, 10.0, 10.0, 10.0, 20.0}
	zFallback := MADZ(mostlyConst, 30.0)
	if zFallback <= 0 || math.IsNaN(zFallback) || math.IsInf(zFallback, 0) {
		t.Errorf("expected valid positive z-score from fallback, got %f", zFallback)
	}
}

func TestDetect_FlatBaselineWith10xSpike(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	baselineStart := now.Add(-24 * time.Hour)

	// 144 points spaced by 10 minutes over 24h: baseline around 10.0
	var pts []Point
	tSpike := now.Add(-2 * time.Hour)

	for i := 0; i < 144; i++ {
		curT := baselineStart.Add(time.Duration(i) * 10 * time.Minute)
		v := 10.0
		// Introduce a 10x spike at tSpike
		if curT.Equal(tSpike) {
			v = 100.0
		}
		// Introduce duplicate spike 5 minutes later (same 15-minute bucket)
		if curT.Equal(tSpike.Add(5 * time.Minute)) {
			v = 80.0
		}
		pts = append(pts, Point{T: curT, V: v})
	}
	// Add an explicit duplicate point 5 min after spike
	pts = append(pts, Point{T: tSpike.Add(5 * time.Minute), V: 80.0})

	anomalies := Detect(pts, "vm.cpu", 24*time.Hour, 5.0, now)

	// Exactly ONE anomaly should trigger, suppressing duplicate within 15 min
	if len(anomalies) != 1 {
		t.Fatalf("expected exactly 1 anomaly, got %d: %+v", len(anomalies), anomalies)
	}

	a := anomalies[0]
	if a.Value != 100.0 {
		t.Errorf("expected anomaly value 100.0 (highest |z| in bucket), got %f", a.Value)
	}
	if a.Z <= 5.0 {
		t.Errorf("expected Z > 5.0, got %f", a.Z)
	}
	if a.Metric != "vm.cpu" {
		t.Errorf("expected metric vm.cpu, got %s", a.Metric)
	}
}

func TestDetect_ConstantSeries_NoPanic(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	var pts []Point
	for i := 0; i < 100; i++ {
		pts = append(pts, Point{
			T: now.Add(-time.Duration(100-i) * 10 * time.Minute),
			V: 25.0,
		})
	}

	anomalies := Detect(pts, "host.mem", 24*time.Hour, 5.0, now)
	if len(anomalies) != 0 {
		t.Errorf("expected 0 anomalies on constant series, got %d", len(anomalies))
	}
}

func TestDetect_SmoothGradualDrift(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	// Smooth gradual drift from 10.0 to 20.0 over 24h
	var pts []Point
	for i := 0; i < 144; i++ {
		pts = append(pts, Point{
			T: now.Add(-time.Duration(144-i) * 10 * time.Minute),
			V: 10.0 + 10.0*(float64(i)/144.0),
		})
	}

	anomalies := Detect(pts, "host.cpu", 24*time.Hour, 5.0, now)
	if len(anomalies) != 0 {
		t.Errorf("expected 0 anomalies for smooth gradual drift, got %d: %+v", len(anomalies), anomalies)
	}
}

func TestMedian(t *testing.T) {
	if m := Median([]float64{3, 1, 2}); m != 2.0 {
		t.Errorf("expected median 2, got %f", m)
	}
	if m := Median([]float64{1, 2, 3, 4}); m != 2.5 {
		t.Errorf("expected median 2.5, got %f", m)
	}
	if m := Median(nil); m != 0 {
		t.Errorf("expected 0 for nil, got %f", m)
	}
}
