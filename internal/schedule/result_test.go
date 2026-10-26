package schedule

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type pruneFails struct{ *fakeRunner }

func (pruneFails) PruneBackups(string, string, func([]time.Time) []bool) (int, error) {
	return 0, errors.New("permission denied")
}

func TestRunResultsReachOnResult(t *testing.T) {
	r := &fakeRunner{snapshots: map[string][]string{}}
	s, _ := Open("", "/b", pruneFails{r}, syncStart, nil)
	var got []error
	s.OnResult = func(sc Schedule, err error) { got = append(got, err) }
	sc, err := s.Put(Schedule{Kind: Backups, Domain: "u1", Enabled: true, Every: "daily", At: "03:00", Keep: 3})
	if err != nil {
		t.Fatal(err)
	}

	// The backup works, removing old ones does not: a success whose result says so.
	if err := s.RunNow(sc.ID); err != nil {
		t.Fatal(err)
	}
	l := s.List("")[0]
	if len(got) != 1 || got[0] != nil || l.LastError != "" || !strings.Contains(l.LastResult, "removing old ones failed: permission denied") {
		t.Fatalf("got %v, schedule %+v", got, l)
	}

	r.fail = errors.New("disk full")
	_ = s.RunNow(sc.ID)
	if len(got) != 2 || got[1] == nil || s.List("")[0].LastError != "disk full" {
		t.Fatalf("got %v", got)
	}
}

func TestPanickingRunIsMarkedFailed(t *testing.T) {
	s, _ := Open("", "/b", panicRunner{&fakeRunner{snapshots: map[string][]string{}}}, func(_, _ string, fn func(context.Context, func(string)) (string, error)) {
		defer func() { _ = recover() }() // what the job manager does
		_, _ = fn(context.Background(), func(string) {})
	}, nil)
	sc, _ := s.Put(Schedule{Kind: Backups, Domain: "u1", Enabled: true, Every: "daily", At: "03:00", Keep: 3})
	_ = s.RunNow(sc.ID)
	if err := s.RunNow(sc.ID); err != nil {
		t.Fatalf("still marked running after a panic: %v", err)
	}
	if l := s.List("")[0]; !strings.Contains(l.LastError, "internal error") {
		t.Fatalf("schedule %+v", l)
	}
}

type panicRunner struct{ *fakeRunner }

func (panicRunner) Backup(context.Context, string, string, int, func(string)) error {
	panic("boom")
}
