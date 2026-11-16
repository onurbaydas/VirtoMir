// Package timeline answers "what happened to this machine?": one list, newest first, of
// what people did to it, how its definition changed (also outside VirtoMir), when it
// started, stopped, crashed or was paused and why, its snapshots, backups and alerts.
//
// Almost all of that is recorded elsewhere already (the activity log, the configuration
// history, libvirt's snapshots, the backup folders, the alert engine). This package keeps
// the one thing nobody kept, what libvirt reports about a machine's life (Store), and puts
// the sources together so that one thing that happened is one line (Fold).
package timeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/onurbaydas/virtomir/internal/atomicfile"
)

// Kinds of event.
const (
	KindAction     = "action"     // someone did something through VirtoMir (the activity log)
	KindDefinition = "definition" // the saved definition changed (configuration history)
	KindRunning    = "running"    // the running configuration changed: hot-plug
	KindLifecycle  = "lifecycle"  // started, stopped, paused, resumed, crashed, shutdown
	KindSnapshot   = "snapshot"
	KindBackup     = "backup"
	KindAlert      = "alert"
)

// Event is one thing that happened to a machine.
type Event struct {
	Time time.Time `json:"time"`
	Kind string    `json:"kind"`
	// What: lifecycle: started, stopped, …; definition and running: who changed it
	// (virtomir, outside, first seen); alert: its severity.
	What string `json:"what,omitempty"`
	// Detail: lifecycle: why (crashed, ioerror, guest, …); snapshot: its name; alert: its
	// message; backup: its note.
	Detail string `json:"detail,omitempty"`
	User   string `json:"user,omitempty"`

	// An action: the request, as the activity log has it.
	Method string `json:"method,omitempty"`
	Path   string `json:"path,omitempty"`
	Status int    `json:"status,omitempty"`
	Error  string `json:"error,omitempty"`
	Token  string `json:"token,omitempty"`

	// Version is the configuration version this event is, or that this action made: the
	// change itself can be looked at and undone in the change history.
	Version string `json:"version,omitempty"`
	// Ref names the snapshot, backup or alert.
	Ref string `json:"ref,omitempty"`
	// Until: when an alert ended (nil: still on).
	Until *time.Time `json:"until,omitempty"`
	// A backup: its size, whether a schedule made it, and what its last check found.
	SizeBytes  uint64 `json:"sizeBytes,omitempty"`
	Automatic  bool   `json:"automatic,omitempty"`
	CheckError string `json:"checkError,omitempty"`
}

func (e Event) ok() bool { return e.Status > 0 && e.Status < 400 }

// Fold puts the events of all sources in order, newest first, and makes one line of what
// is one thing:
//
//   - a change made through VirtoMir is in the activity log and, a moment later, in the
//     configuration history: the action stays and points at the version it made;
//   - a snapshot or backup someone asked for is an action and a snapshot or backup: the
//     snapshot or backup stays, with who asked for it;
//   - a start is reported by libvirt and seen by the configuration history: libvirt's
//     report stays. Where libvirt's reports were not kept yet (before this existed), the
//     history's "started" stands in for them.
//
// At most limit events are returned (0: all).
func Fold(events []Event, limit int) []Event {
	evs := slices.Clone(events)
	slices.SortStableFunc(evs, func(a, b Event) int { return b.Time.Compare(a.Time) })
	drop := make([]bool, len(evs))

	// near finds the action closest to t, at most before earlier and after later, that
	// match accepts and nothing else has claimed.
	claimed := make([]bool, len(evs))
	near := func(t time.Time, before, after time.Duration, match func(Event) bool) int {
		best, bestGap := -1, time.Duration(0)
		for i, e := range evs {
			if e.Kind != KindAction || claimed[i] || drop[i] || !e.ok() || !match(e) {
				continue
			}
			gap := e.Time.Sub(t)
			if gap < -before || gap > after {
				continue
			}
			if gap < 0 {
				gap = -gap
			}
			if best < 0 || gap < bestGap {
				best, bestGap = i, gap
			}
		}
		return best
	}
	post := func(suffix string) func(Event) bool {
		return func(e Event) bool { return e.Method == "POST" && strings.HasSuffix(e.Path, suffix) }
	}

	started := func(t time.Time) bool {
		for _, e := range evs {
			if e.Kind == KindLifecycle && e.What == "started" {
				if gap := e.Time.Sub(t); gap > -30*time.Second && gap < 30*time.Second {
					return true
				}
			}
		}
		return false
	}

	// Oldest first, so the first of two changes close together gets the first action.
	for i := len(evs) - 1; i >= 0; i-- {
		e := &evs[i]
		switch e.Kind {
		case KindDefinition, KindRunning:
			if e.Kind == KindRunning && (e.What == "started" || e.What == "first seen") {
				// How the machine came up, not a change. A start libvirt did not report
				// (before its reports were kept) is still a start.
				if e.What == "started" && !started(e.Time) {
					*e = Event{Time: e.Time, Kind: KindLifecycle, What: "started"}
				} else {
					drop[i] = true
				}
				continue
			}
			if e.What != "virtomir" {
				continue // outside VirtoMir, or the first version: a line of its own
			}
			// The version is saved right after the request that made it was answered.
			who := e.User
			if j := near(e.Time, 15*time.Second, 2*time.Second, func(a Event) bool { return who == "" || a.User == who }); j >= 0 {
				if evs[j].Version == "" && e.Kind == KindDefinition {
					evs[j].Version = e.Version
				}
				drop[i] = true
				// Not claimed: one request can change the definition and the running
				// configuration, and both versions are its.
			}
		case KindSnapshot:
			// A snapshot is dated when it begins; the request is logged when it is done
			// (saving the memory of a large machine takes minutes).
			if j := near(e.Time, 5*time.Second, 15*time.Minute, post("/snapshots")); j >= 0 {
				e.User, claimed[j], drop[j] = evs[j].User, true, true
			}
		case KindBackup:
			// A backup runs as a job: the request is answered at once, when it starts.
			if j := near(e.Time, 30*time.Second, 30*time.Second, post("/backup")); j >= 0 {
				e.User, claimed[j], drop[j] = evs[j].User, true, true
			}
		}
	}

	out := make([]Event, 0, len(evs))
	for i, e := range evs {
		if !drop[i] {
			out = append(out, e)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Occurrence is something libvirt reported about a machine's life.
type Occurrence struct {
	Time   time.Time `json:"time"`
	What   string    `json:"what"`             // started, stopped, paused, resumed, crashed, shutdown
	Detail string    `json:"detail,omitempty"` // why: booted, destroyed, crashed, ioerror, guest, …
}

// Keep is how many occurrences are kept per machine; older ones are dropped.
const Keep = 300

// Store keeps what libvirt reports about each machine's life, in <dir>/<uuid>.json. Reports
// that came while VirtoMir or its connection to libvirt was down are not in it.
type Store struct {
	dir string
	now func() time.Time
	mu  sync.Mutex
}

// Open returns the store in dir, which is made when missing.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	return &Store{dir: dir, now: time.Now}, nil
}

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func (s *Store) file(uuid string) (string, error) {
	uuid = strings.ToLower(uuid)
	if !uuidRE.MatchString(uuid) {
		return "", fmt.Errorf("bad machine id %q", uuid)
	}
	return filepath.Join(s.dir, uuid+".json"), nil
}

func (s *Store) read(path string) []Occurrence {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var list []Occurrence
	if json.Unmarshal(b, &list) != nil {
		return nil // a torn file: what happens from now on is kept again
	}
	return list
}

// Record keeps that what happened to the machine now (and why).
func (s *Store) Record(uuid, what, detail string) error {
	if what == "" {
		return nil
	}
	path, err := s.file(uuid)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	list := s.read(path)
	// libvirt can say the same thing twice in a row; a machine that does it for real (a
	// crash loop) is told once a second at most, so it cannot keep the disk busy.
	if n := len(list); n > 0 && list[n-1].What == what && list[n-1].Detail == detail && now.Sub(list[n-1].Time) < time.Second {
		return nil
	}
	list = append(list, Occurrence{Time: now, What: what, Detail: detail})
	if len(list) > Keep {
		list = list[len(list)-Keep:]
	}
	b, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return atomicfile.Write(path, b, 0o640)
}

// List returns what was kept about the machine, oldest first.
func (s *Store) List(uuid string) []Occurrence {
	path, err := s.file(uuid)
	if err != nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read(path)
}
