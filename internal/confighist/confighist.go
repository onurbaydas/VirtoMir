// Package confighist keeps every version of each machine's definition (ADR-0021), so any
// change — made in VirtoMir, with virsh or by hand — can be compared and undone. Versions
// live in <dir>/<uuid>/<unixnano>.xml with a small .json beside each.
//
// Changes are noticed from libvirt's events as they happen (a slow scan catches what was
// missed while disconnected). Running machines also get "live" versions — the running
// configuration, which hot-plugged devices change without touching the saved one — in
// <dir>/<uuid>/live-<unixnano>.xml, kept apart from the definitions (they cannot be restored).
package confighist

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/onurbaydas/virtomir/internal/atomicfile"
)

// Keep is how many versions are kept per machine; older ones are dropped.
const Keep = 100

// Version is one saved definition.
type Version struct {
	ID   string    `json:"id"`
	Time time.Time `json:"time"`
	// User who made the change through VirtoMir; empty when it was noticed by the periodic
	// scan (virsh, another tool, or the first version VirtoMir saw).
	User   string `json:"user,omitempty"`
	Source string `json:"source"` // "virtomir", "outside" or "first seen"
	// Via is how the change was noticed: "api" (made through VirtoMir), "event" (a libvirt
	// event), "scan" (the periodic comparison) or "" in versions saved before it existed.
	Via  string `json:"via,omitempty"`
	Live bool   `json:"live,omitempty"` // the running configuration, not the saved definition
	Size int    `json:"size"`
}

// Via values.
const (
	ViaAPI   = "api"
	ViaEvent = "event"
	ViaScan  = "scan"
)

// ActiveSource tells whether a machine is running (implemented by *virt.Client). Without
// it no live versions are kept.
type ActiveSource interface {
	DomainActive(uuid string) (bool, error)
}

// Source reads machine definitions (implemented by *virt.Client).
type Source interface {
	DomainUUIDs() ([]string, error)
	DomainXML(uuid string, inactive bool) (string, error)
}

type Store struct {
	dir string
	src Source
	log *slog.Logger
	now func() time.Time

	mu       sync.Mutex
	last     map[string]string // uuid → newest saved XML (cache)
	lastLive map[string]string // uuid → newest live XML, normalized (cache)
	// Who last changed a machine through VirtoMir: a hot-plug event that follows within
	// apiWindow is theirs, not "outside".
	byAPI map[string]apiChange
	// Machines that stopped since their last live version: the next one is a new start.
	restarted map[string]bool
	lastErr   string // the last capture failure logged at Warn (see logFailure)
}

type apiChange struct {
	user string
	at   time.Time
}

const apiWindow = 15 * time.Second

func Open(dir string, src Source, log *slog.Logger) (*Store, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	return &Store{dir: dir, src: src, log: log, now: time.Now, last: map[string]string{}, lastLive: map[string]string{}, byAPI: map[string]apiChange{}, restarted: map[string]bool{}}, nil
}

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var idRE = regexp.MustCompile(`^[0-9]{1,20}$`)
var liveIDRE = regexp.MustCompile(`^live-[0-9]{1,20}$`)

// IsLiveID reports whether a version ID names a live (running configuration) version.
func IsLiveID(id string) bool { return liveIDRE.MatchString(id) }

func (s *Store) machineDir(uuid string) (string, error) {
	uuid = strings.ToLower(uuid)
	if !uuidRE.MatchString(uuid) {
		return "", fmt.Errorf("bad machine id %q", uuid)
	}
	return filepath.Join(s.dir, uuid), nil
}

// Capture saves the machine's definition (and, while it runs, its live configuration)
// after a change made through VirtoMir by user.
func (s *Store) Capture(uuid, user string) error {
	s.mu.Lock()
	s.byAPI[strings.ToLower(uuid)] = apiChange{user, s.now()}
	s.mu.Unlock()
	err := s.CaptureVia(uuid, user, ViaAPI)
	if lerr := s.CaptureLive(uuid, ViaAPI); err == nil && !errors.Is(lerr, errNotRunning) {
		err = lerr
	}
	return err
}

// CaptureVia saves the machine's current definition if it differs from the newest version.
// user is who made the change through VirtoMir ("" when unknown); via how it was noticed.
func (s *Store) CaptureVia(uuid, user, via string) error {
	doc, err := s.src.DomainXML(uuid, true)
	if err != nil {
		return err
	}
	source := "outside"
	if via == ViaAPI {
		source = "virtomir"
	}
	return s.save(uuid, doc, user, source, via)
}

var errNotRunning = errors.New("machine is not running")

// CaptureLive saves the running configuration of a running machine if it differs (after
// leaving out what libvirt changes by itself, see NormalizeLive) from the newest live version.
func (s *Store) CaptureLive(uuid, via string) error {
	as, ok := s.src.(ActiveSource)
	if !ok {
		return errNotRunning
	}
	if on, err := as.DomainActive(uuid); err != nil {
		return err
	} else if !on {
		return errNotRunning
	}
	doc, err := s.src.DomainXML(uuid, false)
	if err != nil {
		return err
	}
	doc = NormalizeLive(doc)
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, err := s.machineDir(uuid)
	if err != nil {
		return err
	}
	key := strings.ToLower(uuid)
	prev, known := s.lastLive[key]
	if !known {
		if ids := s.liveIDs(dir); len(ids) > 0 {
			b, _ := os.ReadFile(filepath.Join(dir, ids[0]+".xml"))
			prev = string(b)
		}
	}
	if prev == doc {
		s.lastLive[key] = doc
		delete(s.restarted, key)
		return nil
	}
	source, user := "outside", ""
	switch {
	case prev == "":
		source = "first seen"
	case s.restarted[key]:
		source = "started" // the definition as the machine came up, not a live change
	case via == ViaAPI:
		source = "virtomir"
	}
	delete(s.restarted, key)
	if a, ok := s.byAPI[key]; ok && source != "first seen" && source != "started" && s.now().Sub(a.at) < apiWindow {
		source, user = "virtomir", a.user
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	id := "live-" + strconv.FormatInt(s.now().UnixNano(), 10)
	if err := s.write(dir, id, doc, Version{ID: id, Time: s.now().UTC(), User: user, Source: source, Via: via, Live: true, Size: len(doc)}); err != nil {
		return err
	}
	s.lastLive[key] = doc
	s.pruneIDs(dir, s.liveIDs(dir))
	return nil
}

func (s *Store) write(dir, id, doc string, v Version) error {
	if err := atomicfile.Write(filepath.Join(dir, id+".xml"), []byte(doc), 0o640); err != nil {
		return err
	}
	meta, _ := json.Marshal(v)
	return atomicfile.Write(filepath.Join(dir, id+".json"), meta, 0o640)
}

func (s *Store) save(uuid, doc, user, source, via string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, err := s.machineDir(uuid)
	if err != nil {
		return err
	}
	prev, known := s.last[uuid]
	if !known {
		prev, _ = s.newestLocked(dir)
	}
	if prev == doc {
		s.last[uuid] = doc
		return nil
	}
	if prev == "" {
		source = "first seen"
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	id := strconv.FormatInt(s.now().UnixNano(), 10)
	if err := s.write(dir, id, doc, Version{ID: id, Time: s.now().UTC(), User: user, Source: source, Via: via, Size: len(doc)}); err != nil {
		return err
	}
	s.last[uuid] = doc
	s.pruneLocked(dir)
	return nil
}

func (s *Store) ids(dir string) []string { return listIDs(dir, idRE, "") }

func (s *Store) liveIDs(dir string) []string { return listIDs(dir, liveIDRE, "live-") }

// listIDs returns the version IDs in dir matching re, newest first.
func listIDs(dir string, re *regexp.Regexp, prefix string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var ids []string
	for _, e := range entries {
		if id, ok := strings.CutSuffix(e.Name(), ".xml"); ok && re.MatchString(id) {
			ids = append(ids, id)
		}
	}
	slices.SortFunc(ids, func(a, b string) int { // numeric, newest first
		a, b = strings.TrimPrefix(a, prefix), strings.TrimPrefix(b, prefix)
		if len(a) != len(b) {
			return len(b) - len(a)
		}
		return strings.Compare(b, a)
	})
	return ids
}

func (s *Store) newestLocked(dir string) (string, error) {
	ids := s.ids(dir)
	if len(ids) == 0 {
		return "", fs.ErrNotExist
	}
	b, err := os.ReadFile(filepath.Join(dir, ids[0]+".xml"))
	return string(b), err
}

func (s *Store) pruneLocked(dir string) { s.pruneIDs(dir, s.ids(dir)) }

func (s *Store) pruneIDs(dir string, ids []string) {
	for _, id := range ids[min(len(ids), Keep):] {
		_ = os.Remove(filepath.Join(dir, id+".xml"))
		_ = os.Remove(filepath.Join(dir, id+".json"))
	}
}

// List returns a machine's saved definitions, newest first.
func (s *Store) List(uuid string) ([]Version, error) {
	dir, err := s.machineDir(uuid)
	if err != nil {
		return nil, err
	}
	return s.versions(dir, s.ids(dir), false), nil
}

// ListLive returns a machine's live versions (running configurations), newest first.
func (s *Store) ListLive(uuid string) ([]Version, error) {
	dir, err := s.machineDir(uuid)
	if err != nil {
		return nil, err
	}
	return s.versions(dir, s.liveIDs(dir), true), nil
}

func (s *Store) versions(dir string, ids []string, live bool) []Version {
	out := []Version{}
	for _, id := range ids {
		v := Version{ID: id, Source: "outside", Live: live}
		if b, err := os.ReadFile(filepath.Join(dir, id+".json")); err == nil {
			_ = json.Unmarshal(b, &v)
		}
		out = append(out, v)
	}
	return out
}

// ErrNoVersion is returned for unknown version IDs.
var ErrNoVersion = errors.New("no such version")

// Get returns one version's XML.
func (s *Store) Get(uuid, id string) (string, error) {
	dir, err := s.machineDir(uuid)
	if err != nil || (!idRE.MatchString(id) && !liveIDRE.MatchString(id)) {
		return "", ErrNoVersion
	}
	b, err := os.ReadFile(filepath.Join(dir, id+".xml"))
	if err != nil {
		return "", ErrNoVersion
	}
	return string(b), nil
}

// Sweep captures every machine whose definition or live configuration changed since the
// last look: changes made outside VirtoMir that no event reported (e.g. while the
// connection to libvirt was down), and a first version of machines VirtoMir has not seen.
func (s *Store) Sweep() {
	uuids, err := s.src.DomainUUIDs()
	if err != nil {
		return
	}
	for _, u := range uuids {
		if err := s.CaptureVia(u, "", ViaScan); err != nil {
			s.logFailure("config history", u, err)
		}
		if err := s.CaptureLive(u, ViaScan); err != nil && !errors.Is(err, errNotRunning) {
			s.logFailure("config history (live)", u, err)
		}
	}
}

// Changed handles a libvirt event for a machine: its definition (persistent) and/or its
// running configuration (live) may have changed.
func (s *Store) Changed(uuid string, persistent, live bool) {
	if persistent {
		if err := s.CaptureVia(uuid, "", ViaEvent); err != nil {
			s.logFailure("config history", uuid, err)
		}
	}
	if live {
		if err := s.CaptureLive(uuid, ViaEvent); err != nil && !errors.Is(err, errNotRunning) {
			s.logFailure("config history (live)", uuid, err)
		}
	}
}

// logFailure reports a failed capture at Warn the first time an error shows up and at
// Debug while the same one repeats, so a lasting problem is visible without flooding the log.
func (s *Store) logFailure(what, uuid string, err error) {
	s.mu.Lock()
	repeat := s.lastErr == err.Error()
	s.lastErr = err.Error()
	s.mu.Unlock()
	if repeat {
		s.log.Debug(what, "domain", uuid, "err", err)
		return
	}
	s.log.Warn(what, "domain", uuid, "err", err)
}

// Forget drops the cached live state of a machine that stopped: its next start is a new
// baseline, not a change.
func (s *Store) Forget(uuid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.lastLive, strings.ToLower(uuid))
	s.restarted[strings.ToLower(uuid)] = true
}
