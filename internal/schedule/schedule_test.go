package schedule

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeRunner struct {
	mu        sync.Mutex
	snapshots map[string][]string
	backups   int
	pruned    int
	fail      error
}

func (f *fakeRunner) Snapshot(uuid, name, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	f.snapshots[uuid] = append(f.snapshots[uuid], name)
	return nil
}
func (f *fakeRunner) ListAutoSnapshots(uuid string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, n := range f.snapshots[uuid] {
		if strings.HasPrefix(n, AutoPrefix) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out, nil
}
func (f *fakeRunner) DeleteSnapshot(uuid, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshots[uuid] = slices.DeleteFunc(f.snapshots[uuid], func(n string) bool { return n == name })
	return nil
}
func (f *fakeRunner) Backup(context.Context, string, string, func(string)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.backups++
	return f.fail
}
func (f *fakeRunner) PruneBackups(string, string, int) (int, error) { f.pruned++; return 1, nil }

// syncStart runs jobs inline so tests are deterministic.
func syncStart(_, _ string, fn func(context.Context, func(string)) (string, error)) {
	_, _ = fn(context.Background(), func(string) {})
}

func TestNext(t *testing.T) {
	at := time.Date(2026, 10, 13, 14, 20, 0, 0, time.Local) // Tuesday
	cases := []struct {
		s    Schedule
		want time.Time
	}{
		{Schedule{Every: "hours", Hours: 1}, time.Date(2026, 10, 13, 15, 0, 0, 0, time.Local)},
		{Schedule{Every: "hours", Hours: 6}, time.Date(2026, 10, 13, 18, 0, 0, 0, time.Local)},
		{Schedule{Every: "daily", At: "03:30"}, time.Date(2026, 10, 14, 3, 30, 0, 0, time.Local)},
		{Schedule{Every: "daily", At: "23:00"}, time.Date(2026, 10, 13, 23, 0, 0, 0, time.Local)},
		{Schedule{Every: "weekly", At: "02:00", Weekday: 0}, time.Date(2026, 10, 18, 2, 0, 0, 0, time.Local)},
	}
	for _, c := range cases {
		if got := c.s.Next(at); !got.Equal(c.want) {
			t.Errorf("%+v: next = %s, want %s", c.s, got, c.want)
		}
	}
}

func TestValidate(t *testing.T) {
	ok := Schedule{Kind: Snapshots, Domain: "u", Every: "daily", At: "03:00", Keep: 7}
	if err := ok.validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Schedule{
		{Kind: "x", Domain: "u", Every: "daily", At: "03:00", Keep: 7},
		{Kind: Snapshots, Every: "daily", At: "03:00", Keep: 7},
		{Kind: Snapshots, Domain: "u", Every: "hours", Hours: 5, Keep: 7},
		{Kind: Snapshots, Domain: "u", Every: "daily", At: "25:00", Keep: 7},
		{Kind: Snapshots, Domain: "u", Every: "daily", At: "03:00", Keep: 0},
	} {
		if err := bad.validate(); err == nil {
			t.Errorf("%+v must be invalid", bad)
		}
	}
}

func TestRetentionKeepsOnlyAutoSnapshots(t *testing.T) {
	r := &fakeRunner{snapshots: map[string][]string{"u": {"before-upgrade"}}}
	s, _ := Open("", "/b", r, syncStart, nil)
	now := time.Date(2026, 10, 13, 10, 0, 0, 0, time.Local)
	s.now = func() time.Time { return now }
	sc, err := s.Put(Schedule{Kind: Snapshots, Domain: "u", Enabled: true, Every: "hours", Hours: 1, Keep: 2})
	if err != nil {
		t.Fatal(err)
	}
	s.tick(context.Background()) // first tick only arms the schedule
	if len(r.snapshots["u"]) != 1 {
		t.Fatalf("ran too early: %v", r.snapshots["u"])
	}
	for i := 1; i <= 4; i++ {
		now = now.Add(time.Hour)
		s.tick(context.Background())
	}
	got := r.snapshots["u"]
	if len(got) != 3 || got[0] != "before-upgrade" || !strings.HasPrefix(got[2], AutoPrefix+"20261013-14") {
		t.Errorf("snapshots = %v (manual one must stay, 2 newest auto ones kept)", got)
	}
	if l := s.List(""); l[0].ID != sc.ID || l[0].LastResult == "" || l[0].NextRun == nil {
		t.Errorf("status = %+v", l[0])
	}
}

func TestBackupScheduleAndErrors(t *testing.T) {
	r := &fakeRunner{snapshots: map[string][]string{}}
	path := filepath.Join(t.TempDir(), "schedules.json")
	s, _ := Open(path, "/b", r, syncStart, nil)
	sc, _ := s.Put(Schedule{Kind: Backups, Domain: "u", Enabled: true, Every: "daily", At: "03:00", Keep: 3})
	if err := s.RunNow(sc.ID); err != nil {
		t.Fatal(err)
	}
	if r.backups != 1 || r.pruned != 1 {
		t.Errorf("backups=%d pruned=%d", r.backups, r.pruned)
	}
	r.fail = errors.New("disk full")
	_ = s.RunNow(sc.ID)
	if l := s.List("u"); l[0].LastError != "disk full" {
		t.Errorf("error not recorded: %+v", l[0])
	}
	// Survives a restart.
	s2, _ := Open(path, "/b", r, syncStart, nil)
	if l := s2.List(""); len(l) != 1 || l[0].Keep != 3 || l[0].LastError != "disk full" {
		t.Errorf("reloaded = %+v", l)
	}
	if err := s2.SetSettings(Settings{BackupDir: "relative/path"}); err == nil {
		t.Error("relative backup dir accepted")
	}
}
