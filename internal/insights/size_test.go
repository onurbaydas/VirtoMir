package insights

import (
	"math"
	"testing"
)

func TestPercentiles_HandCalculated(t *testing.T) {
	// Sample data: [10, 20, 30, 40, 50], N=5
	vs := []float64{10, 20, 30, 40, 50}
	u := Percentiles(vs)

	// Mean: (10+20+30+40+50)/5 = 30
	if math.Abs(u.Mean-30.0) > 1e-6 {
		t.Errorf("expected mean 30, got %f", u.Mean)
	}

	// Max: 50
	if u.Max != 50.0 {
		t.Errorf("expected max 50, got %f", u.Max)
	}

	// Hand-calculated with pos = p/100 * (N-1):
	// p95: pos = 0.95 * 4 = 3.8 -> 40 + 0.8*(50-40) = 48.0
	if math.Abs(u.P95-48.0) > 1e-6 {
		t.Errorf("expected p95 48.0, got %f", u.P95)
	}

	// p99: pos = 0.99 * 4 = 3.96 -> 40 + 0.96*(50-40) = 49.6
	if math.Abs(u.P99-49.6) > 1e-6 {
		t.Errorf("expected p99 49.6, got %f", u.P99)
	}

	// Single point
	u1 := Percentiles([]float64{25.0})
	if u1.P95 != 25.0 || u1.P99 != 25.0 || u1.Mean != 25.0 || u1.Max != 25.0 {
		t.Errorf("unexpected percentiles for single point: %+v", u1)
	}

	// Empty
	uEmpty := Percentiles(nil)
	if uEmpty.P95 != 0 || uEmpty.Mean != 0 {
		t.Errorf("expected zeros for empty percentiles, got %+v", uEmpty)
	}
}

func TestRightSize_LowUsage_Oversized(t *testing.T) {
	// Generate cpu samples where p95 = 10% and p99 = 25%
	// 101 samples: 0..95 have 8%, 96..98 have 10%, 99..100 have 25%
	cpuSamples := make([]float64, 101)
	for i := 0; i <= 95; i++ {
		cpuSamples[i] = 8.0
	}
	for i := 96; i <= 98; i++ {
		cpuSamples[i] = 10.0
	}
	for i := 99; i <= 100; i++ {
		cpuSamples[i] = 25.0
	}

	// Generate memory samples where p95 = 20%
	// 101 samples: 0..94 have 15%, 95..100 have 20%
	memSamples := make([]float64, 101)
	for i := 0; i <= 94; i++ {
		memSamples[i] = 15.0
	}
	for i := 95; i <= 100; i++ {
		memSamples[i] = 20.0
	}

	cur := struct {
		VCPUs  int
		MemMiB uint64
	}{
		VCPUs:  8,
		MemMiB: 4096,
	}

	rec := RightSize(cpuSamples, memSamples, cur, 16)

	if !rec.Oversized {
		t.Errorf("expected Oversized=true, got false")
	}
	if rec.Undersized {
		t.Errorf("expected Undersized=false, got true")
	}

	// CPU target: ceil(p99/100 * 8) = ceil(25/100 * 8) = ceil(2.0) = 2
	if rec.SuggestedVCPUs != 2 {
		t.Errorf("expected SuggestedVCPUs=2, got %d", rec.SuggestedVCPUs)
	}

	// Mem target: ceil(20/100 * 4096 / 128) * 128 = ceil(6.4) * 128 = 7 * 128 = 896
	if rec.SuggestedMemMiB != 896 {
		t.Errorf("expected SuggestedMemMiB=896, got %d", rec.SuggestedMemMiB)
	}

	if len(rec.Reasons) < 2 {
		t.Errorf("expected at least 2 reasons, got %d: %v", len(rec.Reasons), rec.Reasons)
	}
}

func TestRightSize_HighUsage_Undersized(t *testing.T) {
	// CPU: p95 = 92% (>85%)
	cpuSamples := make([]float64, 100)
	for i := 0; i < 100; i++ {
		cpuSamples[i] = 92.0
	}

	// Mem: p95 = 95% (>90%)
	memSamples := make([]float64, 100)
	for i := 0; i < 100; i++ {
		memSamples[i] = 95.0
	}

	cur := struct {
		VCPUs  int
		MemMiB uint64
	}{
		VCPUs:  4,
		MemMiB: 2048,
	}

	rec := RightSize(cpuSamples, memSamples, cur, 16)

	if rec.Oversized {
		t.Errorf("expected Oversized=false, got true")
	}
	if !rec.Undersized {
		t.Errorf("expected Undersized=true, got false")
	}

	// CPU target: ceil(92 / 80 * 4) = ceil(4.6) = 5
	if rec.SuggestedVCPUs != 5 {
		t.Errorf("expected SuggestedVCPUs=5, got %d", rec.SuggestedVCPUs)
	}

	// Mem target: ceil(2048 * 1.25 / 128) * 128 = ceil(2560 / 128) * 128 = 20 * 128 = 2560
	if rec.SuggestedMemMiB != 2560 {
		t.Errorf("expected SuggestedMemMiB=2560, got %d", rec.SuggestedMemMiB)
	}
}

func TestRightSize_Floors(t *testing.T) {
	// Tiny usage on a 1 vCPU, 512 MiB VM: must not drop below 1 vCPU or 512 MiB
	cpuSamples := []float64{1.0, 2.0, 3.0}
	memSamples := []float64{5.0, 10.0, 15.0}

	cur := struct {
		VCPUs  int
		MemMiB uint64
	}{
		VCPUs:  1,
		MemMiB: 512,
	}

	rec := RightSize(cpuSamples, memSamples, cur, 8)
	if rec.SuggestedVCPUs < 1 {
		t.Errorf("suggested vCPUs %d violated minimum floor of 1", rec.SuggestedVCPUs)
	}
	if rec.SuggestedMemMiB < 512 {
		t.Errorf("suggested memory %d violated minimum floor of 512", rec.SuggestedMemMiB)
	}
	if rec.Oversized {
		t.Errorf("expected Oversized=false since already at minimum floors, got true")
	}

	// Regression (live on virtomir-dev): a 256 MiB machine was told to grow to 512 MiB, with
	// no reason, because the floor raised it. The floor only limits a reduction.
	small := RightSize(cpuSamples, memSamples, struct {
		VCPUs  int
		MemMiB uint64
	}{1, 256}, 8)
	if small.SuggestedMemMiB != 256 || small.Undersized || small.Oversized {
		t.Errorf("256 MiB machine: %+v", small)
	}

	// 2 vCPUs, 1024 MiB with near-zero usage: drops to 1 vCPU and 512 MiB
	cur2 := struct {
		VCPUs  int
		MemMiB uint64
	}{
		VCPUs:  2,
		MemMiB: 1024,
	}
	rec2 := RightSize(cpuSamples, memSamples, cur2, 8)
	if rec2.SuggestedVCPUs != 1 {
		t.Errorf("expected 1 vCPU floor, got %d", rec2.SuggestedVCPUs)
	}
	if rec2.SuggestedMemMiB != 512 {
		t.Errorf("expected 512 MiB floor, got %d", rec2.SuggestedMemMiB)
	}
	if !rec2.Oversized {
		t.Errorf("expected Oversized=true, got false")
	}
}

func TestEstimatedSavings(t *testing.T) {
	recs := []Recommendation{
		{CurrentMemMiB: 4096, SuggestedMemMiB: 2048}, // 2 GiB freed
		{CurrentMemMiB: 8192, SuggestedMemMiB: 4096}, // 4 GiB freed
		{CurrentMemMiB: 1024, SuggestedMemMiB: 2048}, // upsizing, 0 freed
	}

	// 6 GiB * $2.50 = $15.00
	savings := EstimatedSavings(recs, 2.50)
	if math.Abs(savings-15.0) > 1e-6 {
		t.Errorf("expected savings 15.0, got %f", savings)
	}
}
