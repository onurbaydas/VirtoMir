// Package schedule runs automatic snapshots and backups of machines and keeps only the
// newest few of each (retention). It only ever deletes what it created itself: snapshots
// named "auto-…" and backups marked automatic.
package schedule

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

type Kind string

const (
	Snapshots Kind = "snapshot"
	Backups   Kind = "backup"
)

// Schedule is one recurring job for one machine.
type Schedule struct {
	ID      string `json:"id"`
	Kind    Kind   `json:"kind"`
	Domain  string `json:"domain"` // UUID
	Enabled bool   `json:"enabled"`
	// Every: "hours" (every Hours hours), "daily" (at At) or "weekly" (Weekday at At).
	Every   string `json:"every"`
	Hours   int    `json:"hours,omitempty"`
	At      string `json:"at,omitempty"`      // "HH:MM", local time
	Weekday int    `json:"weekday,omitempty"` // 0 = Sunday
	Keep    int    `json:"keep"`              // how many automatic ones to keep

	LastRun    *time.Time `json:"lastRun,omitempty"`
	LastError  string     `json:"lastError,omitempty"`
	LastResult string     `json:"lastResult,omitempty"`
	NextRun    *time.Time `json:"nextRun,omitempty"` // computed, not stored
}

// Settings are global for the scheduler.
type Settings struct {
	// BackupDir is where backups go (an absolute path; NFS mounts work).
	BackupDir string `json:"backupDir"`
}

// Runner does the actual work (implemented by an adapter over *virt.Client).
type Runner interface {
	Snapshot(uuid, name, description string) error
	ListAutoSnapshots(uuid string) ([]string, error) // names starting with AutoPrefix, oldest first
	DeleteSnapshot(uuid, name string) error
	Backup(ctx context.Context, dir, uuid string, report func(string)) error
	PruneBackups(dir, uuid string, keep int) (int, error)
}

// AutoPrefix marks snapshots made by schedules; retention only ever touches these.
const AutoPrefix = "auto-"

// Starter runs a job in the background (implemented by *jobs.Manager).
type Starter func(kind, title string, fn func(ctx context.Context, report func(string)) (string, error))

type Scheduler struct {
	path   string
	log    *slog.Logger
	runner Runner
	start  Starter
	now    func() time.Time

	mu        sync.Mutex
	settings  Settings
	schedules []Schedule
	running   map[string]bool
}

// Open loads path. defaultBackupDir is used until an admin picks another location
// (virtomird passes <state-dir>/backups).
func Open(path, defaultBackupDir string, runner Runner, start Starter, log *slog.Logger) (*Scheduler, error) {
	s := &Scheduler{path: path, runner: runner, start: start, log: log, now: time.Now,
		settings: Settings{BackupDir: defaultBackupDir}, schedules: []Schedule{}, running: map[string]bool{}}
	if path == "" {
		return s, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	var file struct {
		Settings  Settings   `json:"settings"`
		Schedules []Schedule `json:"schedules"`
	}
	if err := json.Unmarshal(b, &file); err != nil {
		return s, fmt.Errorf("schedules file %s: %w", path, err)
	}
	if file.Settings.BackupDir != "" {
		s.settings = file.Settings
	}
	if file.Schedules != nil {
		s.schedules = file.Schedules
	}
	return s, nil
}

func (s *Scheduler) saveLocked() {
	if s.path == "" {
		return
	}
	file := struct {
		Settings  Settings   `json:"settings"`
		Schedules []Schedule `json:"schedules"`
	}{s.settings, s.schedules}
	for i := range file.Schedules {
		file.Schedules[i].NextRun = nil
	}
	b, _ := json.MarshalIndent(file, "", "  ")
	err := os.MkdirAll(filepath.Dir(s.path), 0o750)
	if err == nil {
		tmp := s.path + ".tmp"
		if err = os.WriteFile(tmp, b, 0o600); err == nil {
			err = os.Rename(tmp, s.path)
		}
	}
	if err != nil && s.log != nil {
		s.log.Warn("save schedules", "err", err)
	}
}

func (sc Schedule) validate() error {
	if sc.Kind != Snapshots && sc.Kind != Backups {
		return errors.New("kind must be snapshot or backup")
	}
	if sc.Domain == "" {
		return errors.New("pick a machine")
	}
	switch sc.Every {
	case "hours":
		if !slices.Contains([]int{1, 2, 3, 4, 6, 8, 12}, sc.Hours) {
			return errors.New("every 1, 2, 3, 4, 6, 8 or 12 hours")
		}
	case "daily", "weekly":
		if _, err := time.Parse("15:04", sc.At); err != nil {
			return errors.New("time must look like 03:30")
		}
		if sc.Every == "weekly" && (sc.Weekday < 0 || sc.Weekday > 6) {
			return errors.New("weekday must be 0 (Sunday) to 6")
		}
	default:
		return errors.New("every must be hours, daily or weekly")
	}
	if sc.Keep < 1 || sc.Keep > 365 {
		return errors.New("keep between 1 and 365")
	}
	return nil
}

// Next is the first run time after t.
func (sc Schedule) Next(t time.Time) time.Time {
	switch sc.Every {
	case "hours":
		next := t.Truncate(time.Hour).Add(time.Hour) // on the hour: 00:00, 06:00, 12:00 …
		for next.Hour()%sc.Hours != 0 {
			next = next.Add(time.Hour)
		}
		return next
	default:
		at, _ := time.Parse("15:04", sc.At)
		next := time.Date(t.Year(), t.Month(), t.Day(), at.Hour(), at.Minute(), 0, 0, t.Location())
		if !next.After(t) {
			next = next.AddDate(0, 0, 1)
		}
		if sc.Every == "weekly" {
			for int(next.Weekday()) != sc.Weekday {
				next = next.AddDate(0, 0, 1)
			}
		}
		return next
	}
}

// List returns the schedules (all, or those of one machine) with their next run.
func (s *Scheduler) List(uuid string) []Schedule {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Schedule{}
	now := s.now()
	for _, sc := range s.schedules {
		if uuid != "" && sc.Domain != uuid {
			continue
		}
		if sc.Enabled {
			base := now
			if sc.LastRun != nil && sc.LastRun.After(base) {
				base = *sc.LastRun
			}
			n := sc.Next(base)
			sc.NextRun = &n
		}
		out = append(out, sc)
	}
	return out
}

func (s *Scheduler) Settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings
}

func (s *Scheduler) SetSettings(st Settings) error {
	if !filepath.IsAbs(st.BackupDir) || strings.Contains(st.BackupDir, "..") {
		return errors.New("the backup location must be an absolute path")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings = st
	s.saveLocked()
	return nil
}

// Put creates or replaces a schedule (by ID).
func (s *Scheduler) Put(sc Schedule) (Schedule, error) {
	if err := sc.validate(); err != nil {
		return sc, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sc.NextRun = nil
	if sc.ID == "" {
		sc.ID = newID()
		s.schedules = append(s.schedules, sc)
		s.saveLocked()
		return sc, nil
	}
	i := slices.IndexFunc(s.schedules, func(x Schedule) bool { return x.ID == sc.ID })
	if i < 0 {
		return sc, fmt.Errorf("no schedule %s", sc.ID)
	}
	sc.LastRun, sc.LastError, sc.LastResult = s.schedules[i].LastRun, s.schedules[i].LastError, s.schedules[i].LastResult
	s.schedules[i] = sc
	s.saveLocked()
	return sc, nil
}

func (s *Scheduler) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.schedules, func(x Schedule) bool { return x.ID == id })
	if i < 0 {
		return fmt.Errorf("no schedule %s", id)
	}
	s.schedules = slices.Delete(s.schedules, i, i+1)
	s.saveLocked()
	return nil
}

// Run checks for due schedules every 30 seconds until ctx ends.
func (s *Scheduler) Run(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		s.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// tick starts every schedule that is due (missed runs while virtomird was down run once).
func (s *Scheduler) tick(ctx context.Context) {
	s.mu.Lock()
	now := s.now()
	var due []Schedule
	for i := range s.schedules {
		sc := &s.schedules[i]
		if !sc.Enabled || s.running[sc.ID] {
			continue
		}
		if sc.LastRun == nil {
			// Never ran: start from creation, i.e. wait for the first slot.
			t := now
			sc.LastRun = &t
			sc.LastResult = "waiting for the first run"
			continue
		}
		if !sc.Next(*sc.LastRun).After(now) {
			s.running[sc.ID] = true
			due = append(due, *sc)
		}
	}
	dir := s.settings.BackupDir
	s.mu.Unlock()

	for _, sc := range due {
		s.launch(ctx, sc, dir)
	}
}

func (s *Scheduler) launch(_ context.Context, sc Schedule, dir string) {
	title := map[Kind]string{Snapshots: "Automatic snapshot", Backups: "Automatic backup"}[sc.Kind]
	s.start(string(sc.Kind), title, func(ctx context.Context, report func(string)) (string, error) {
		result, err := s.execute(ctx, sc, dir, report)
		s.finish(sc.ID, result, err)
		return result, err
	})
}

// RunNow starts a schedule immediately (the "Run now" button).
func (s *Scheduler) RunNow(id string) error {
	s.mu.Lock()
	i := slices.IndexFunc(s.schedules, func(x Schedule) bool { return x.ID == id })
	if i < 0 {
		s.mu.Unlock()
		return fmt.Errorf("no schedule %s", id)
	}
	if s.running[id] {
		s.mu.Unlock()
		return errors.New("it is already running")
	}
	s.running[id] = true
	sc, dir := s.schedules[i], s.settings.BackupDir
	s.mu.Unlock()
	s.launch(context.Background(), sc, dir)
	return nil
}

func (s *Scheduler) execute(ctx context.Context, sc Schedule, dir string, report func(string)) (string, error) {
	switch sc.Kind {
	case Snapshots:
		name := AutoPrefix + s.now().Format("20060102-150405") // seconds: "Run now" twice a minute must not collide
		report("Taking snapshot " + name + "…")
		if err := s.runner.Snapshot(sc.Domain, name, "Taken by a VirtoMir schedule"); err != nil {
			return "", err
		}
		names, err := s.runner.ListAutoSnapshots(sc.Domain)
		if err != nil {
			return name, nil
		}
		removed := 0
		for len(names) > sc.Keep {
			if err := s.runner.DeleteSnapshot(sc.Domain, names[0]); err != nil {
				break
			}
			names, removed = names[1:], removed+1
		}
		return fmt.Sprintf("took %s, removed %d old", name, removed), nil
	case Backups:
		if err := s.runner.Backup(ctx, dir, sc.Domain, report); err != nil {
			return "", err
		}
		n, _ := s.runner.PruneBackups(dir, sc.Domain, sc.Keep)
		return fmt.Sprintf("backed up, removed %d old", n), nil
	}
	return "", fmt.Errorf("unknown kind %s", sc.Kind)
}

func (s *Scheduler) finish(id, result string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.running, id)
	i := slices.IndexFunc(s.schedules, func(x Schedule) bool { return x.ID == id })
	if i < 0 {
		return
	}
	now := s.now()
	sc := &s.schedules[i]
	sc.LastRun, sc.LastResult, sc.LastError = &now, result, ""
	if err != nil {
		sc.LastError, sc.LastResult = err.Error(), ""
	}
	s.saveLocked()
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
