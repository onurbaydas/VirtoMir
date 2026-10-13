package history

import (
	"math"
	"path/filepath"
	"testing"
	"time"
)

var t0 = time.Unix(1_800_000_000, 0) // aligned to 10 s, 1 min, 10 min and 1 h

func val(t *testing.T, p *float64) float64 {
	t.Helper()
	if p == nil {
		t.Fatal("expected a value, got none")
	}
	return *p
}

func TestAveragesIntoBuckets(t *testing.T) {
	s := New()
	// 10 one-second samples in the first 10 s bucket: 0..9 → average 4.5.
	for i := range 10 {
		s.Add("cpu", t0.Add(time.Duration(i)*time.Second), float64(i))
	}
	s.Add("cpu", t0.Add(10*time.Second), 100) // next bucket
	res, err := s.Query([]string{"cpu"}, time.Hour, t0.Add(10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if res.Step != 10 {
		t.Fatalf("1 h must use the 10 s tier, got step %d", res.Step)
	}
	v := res.Series["cpu"]
	if len(v) != 360 {
		t.Fatalf("len = %d", len(v))
	}
	if got := val(t, v[358]); got != 4.5 {
		t.Errorf("bucket average = %v, want 4.5", got)
	}
	if got := val(t, v[359]); got != 100 {
		t.Errorf("current bucket = %v", got)
	}
	if v[0] != nil {
		t.Error("old buckets must be empty")
	}
	if res.Start != t0.Unix()-358*10 {
		t.Errorf("start = %d", res.Start)
	}
}

func TestTierChoice(t *testing.T) {
	cases := map[time.Duration]int64{
		time.Hour:           10,
		6 * time.Hour:       10,
		24 * time.Hour:      60,
		7 * 24 * time.Hour:  600,
		30 * 24 * time.Hour: 3600,
	}
	for rng, step := range cases {
		i, ok := TierFor(rng)
		if !ok || int64(Tiers[i].Step/time.Second) != step {
			t.Errorf("%s: tier %d ok=%v", rng, i, ok)
		}
	}
	if _, ok := TierFor(60 * 24 * time.Hour); ok {
		t.Error("60 days is more than we keep")
	}
}

func TestGapsAreEmpty(t *testing.T) {
	s := New()
	s.Add("x", t0, 1)
	s.Add("x", t0.Add(5*time.Minute), 2) // 30 buckets later
	res, _ := s.Query([]string{"x"}, time.Hour, t0.Add(5*time.Minute))
	v := res.Series["x"]
	n := len(v)
	if val(t, v[n-1]) != 2 || val(t, v[n-31]) != 1 {
		t.Errorf("ends wrong: %v %v", v[n-1], v[n-31])
	}
	for i := n - 30; i < n-1; i++ {
		if v[i] != nil {
			t.Fatalf("gap bucket %d has a value", i)
		}
	}
}

func TestRingWrapsAround(t *testing.T) {
	s := New()
	// 7 hours of 10 s samples: more than the 6 h tier holds.
	for i := range 7 * 360 {
		s.Add("x", t0.Add(time.Duration(i)*10*time.Second), float64(i))
	}
	now := t0.Add(time.Duration(7*360-1) * 10 * time.Second)
	res, _ := s.Query([]string{"x"}, 6*time.Hour, now)
	v := res.Series["x"]
	if val(t, v[len(v)-1]) != float64(7*360-1) || val(t, v[0]) != float64(360) {
		t.Errorf("wrapped ring: first %v last %v", *v[0], *v[len(v)-1])
	}
	// The coarser tiers still have the whole period.
	day, _ := s.Query([]string{"x"}, 24*time.Hour, now)
	filled := 0
	for _, p := range day.Series["x"] {
		if p != nil {
			filled++
		}
	}
	if filled != 7*60 {
		t.Errorf("1 min tier has %d points, want %d", filled, 7*60)
	}
}

func TestUnknownSeriesAndNaN(t *testing.T) {
	s := New()
	s.Add("x", t0, math.NaN())
	if s.Len() != 0 {
		t.Error("NaN must not create a series")
	}
	res, _ := s.Query([]string{"nope"}, time.Hour, t0)
	for _, p := range res.Series["nope"] {
		if p != nil {
			t.Fatal("unknown series must be empty")
		}
	}
}

func TestPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.gob")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Add("cpu", time.Now(), 42)
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	res, _ := s2.Query([]string{"cpu"}, time.Hour, time.Now())
	v := res.Series["cpu"]
	if val(t, v[len(v)-1]) != 42 {
		t.Error("value lost across save/open")
	}
}

func TestStaleSeriesDropped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.gob")
	s, _ := Open(path)
	s.Add("old", time.Now().Add(-DropAfter-time.Hour), 1)
	s.Add("new", time.Now(), 1)
	_ = s.Save()
	if s.Len() != 1 {
		t.Errorf("stale series kept: %d series", s.Len())
	}
}
