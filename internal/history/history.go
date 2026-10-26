// Package history keeps metric history beyond the live minute: every 1 s sample is averaged
// into fixed-size rings at several resolutions (10 s for 6 h, 1 min for a day, 10 min for a
// week, 1 h for a month). Memory per series is constant (~21 KB); the store is saved to disk
// periodically so a restart keeps the history.
package history

import (
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/onurbaydas/virtomir/internal/atomicfile"
)

// Tier is one resolution.
type Tier struct {
	Step   time.Duration
	Points int
}

// Tiers from finest to coarsest. Their spans are the ranges the UI offers.
var Tiers = []Tier{
	{10 * time.Second, 6 * 360}, // 6 h
	{time.Minute, 24 * 60},      // 24 h
	{10 * time.Minute, 7 * 144}, // 7 d
	{time.Hour, 30 * 24},        // 30 d
}

// DropAfter forgets series that got no samples for this long (deleted machines).
const DropAfter = 31 * 24 * time.Hour

// ring is one tier of one series. Slot i holds bucket b where b % len(vals) == i.
type ring struct {
	Vals []float32 // NaN: no data
	Last int64     // newest bucket written (unix seconds / step)
	// Running average of the newest bucket.
	AccSum   float64
	AccCount int
}

type series struct {
	Rings   []ring
	Updated int64 // unix seconds
}

type Store struct {
	mu     sync.Mutex
	series map[string]*series
	path   string
}

// New returns an in-memory store; Open adds persistence.
func New() *Store { return &Store{series: map[string]*series{}} }

// Open loads path if it exists. A missing file is fine; a broken one is reported and the
// store starts empty (history is valuable but never worth refusing to start).
func Open(path string) (*Store, error) {
	s := New()
	s.path = path
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	var file fileFormat
	err = gob.NewDecoder(f).Decode(&file)
	f.Close()
	if err != nil {
		// Keep the unreadable file for inspection; the next save starts a fresh one.
		return s, atomicfile.Quarantine(path, err)
	}
	if file.Version != formatVersion || !sameTiers(file.Tiers) {
		return s, nil // layout changed: start fresh rather than misread
	}
	s.series = file.Series
	return s, nil
}

const formatVersion = 1

type fileFormat struct {
	Version int
	Tiers   []Tier
	Series  map[string]*series
}

func sameTiers(t []Tier) bool { return slices.Equal(t, Tiers) }

// Save writes the store atomically (temp file + rename) and drops stale series.
func (s *Store) Save() error {
	if s.path == "" {
		return nil
	}
	s.mu.Lock()
	cutoff := time.Now().Add(-DropAfter).Unix()
	for k, v := range s.series {
		if v.Updated < cutoff {
			delete(s.series, k)
		}
	}
	file := fileFormat{Version: formatVersion, Tiers: Tiers, Series: s.series}
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o750); err != nil {
		return err
	}
	return atomicfile.WriteFunc(s.path, 0o600, func(w io.Writer) error {
		return gob.NewEncoder(w).Encode(&file)
	})
}

// Add records one sample. NaN values are ignored.
func (s *Store) Add(name string, t time.Time, v float64) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sr := s.series[name]
	if sr == nil {
		sr = &series{Rings: make([]ring, len(Tiers))}
		for i, tier := range Tiers {
			sr.Rings[i] = newRing(tier.Points)
		}
		s.series[name] = sr
	}
	sr.Updated = t.Unix()
	for i, tier := range Tiers {
		sr.Rings[i].add(t.Unix()/int64(tier.Step/time.Second), v)
	}
}

func newRing(n int) ring {
	r := ring{Vals: make([]float32, n), Last: -1}
	for i := range r.Vals {
		r.Vals[i] = float32(math.NaN())
	}
	return r
}

func (r *ring) add(bucket int64, v float64) {
	n := int64(len(r.Vals))
	switch {
	case bucket < r.Last:
		return // clock went backwards: ignore rather than corrupt older data
	case bucket > r.Last:
		// Clear the slots of buckets that got no samples in between.
		gap := bucket - r.Last
		if r.Last < 0 || gap >= n {
			for i := range r.Vals {
				r.Vals[i] = float32(math.NaN())
			}
		} else {
			for b := r.Last + 1; b <= bucket; b++ {
				r.Vals[b%n] = float32(math.NaN())
			}
		}
		r.Last, r.AccSum, r.AccCount = bucket, 0, 0
	}
	r.AccSum += v
	r.AccCount++
	r.Vals[bucket%n] = float32(r.AccSum / float64(r.AccCount))
}

// Result is a query answer: values[i] belongs to time Start + i*Step (nil: no data).
type Result struct {
	Start  int64                 `json:"start"` // unix seconds
	Step   int64                 `json:"step"`  // seconds
	Series map[string][]*float64 `json:"series"`
}

// TierFor picks the finest tier whose span covers rng.
func TierFor(rng time.Duration) (int, bool) {
	for i, t := range Tiers {
		if time.Duration(t.Points)*t.Step >= rng {
			return i, true
		}
	}
	return 0, false
}

// Query returns the last rng of each named series (unknown series come back all nil).
func (s *Store) Query(names []string, rng time.Duration, now time.Time) (Result, error) {
	ti, ok := TierFor(rng)
	if !ok {
		return Result{}, fmt.Errorf("range %s exceeds the kept history", rng)
	}
	tier := Tiers[ti]
	stepSec := int64(tier.Step / time.Second)
	count := int64(rng / tier.Step)
	end := now.Unix() / stepSec // current bucket (partial)
	first := end - count + 1
	res := Result{Start: first * stepSec, Step: stepSec, Series: make(map[string][]*float64, len(names))}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, name := range names {
		out := make([]*float64, count)
		if sr := s.series[name]; sr != nil {
			r := sr.Rings[ti]
			n := int64(len(r.Vals))
			for i := int64(0); i < count; i++ {
				b := first + i
				if b > r.Last || b <= r.Last-n {
					continue // newer than anything written, or overwritten long ago
				}
				if v := r.Vals[b%n]; !math.IsNaN(float64(v)) {
					f := float64(v)
					out[i] = &f
				}
			}
		}
		res.Series[name] = out
	}
	return res, nil
}

// Len is the number of series (for tests and diagnostics).
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.series)
}
