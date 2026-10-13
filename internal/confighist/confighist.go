// Package confighist keeps every version of each machine's definition (ADR-0021), so any
// change — made in VirtoMir, with virsh or by hand — can be compared and undone. Versions
// live in <dir>/<uuid>/<unixnano>.xml with a small .json beside each.
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
	Size   int    `json:"size"`
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

	mu   sync.Mutex
	last map[string]string // uuid → newest saved XML (cache)
}

func Open(dir string, src Source, log *slog.Logger) (*Store, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	return &Store{dir: dir, src: src, log: log, now: time.Now, last: map[string]string{}}, nil
}

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var idRE = regexp.MustCompile(`^[0-9]{1,20}$`)

func (s *Store) machineDir(uuid string) (string, error) {
	uuid = strings.ToLower(uuid)
	if !uuidRE.MatchString(uuid) {
		return "", fmt.Errorf("bad machine id %q", uuid)
	}
	return filepath.Join(s.dir, uuid), nil
}

// Capture saves the machine's current definition if it differs from the newest version.
// user is who made the change through VirtoMir ("" when unknown).
func (s *Store) Capture(uuid, user string) error {
	doc, err := s.src.DomainXML(uuid, true)
	if err != nil {
		return err
	}
	source := "virtomir"
	if user == "" {
		source = "outside"
	}
	return s.save(uuid, doc, user, source)
}

func (s *Store) save(uuid, doc, user, source string) error {
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
	if err := os.WriteFile(filepath.Join(dir, id+".xml"), []byte(doc), 0o640); err != nil {
		return err
	}
	meta, _ := json.Marshal(Version{ID: id, Time: s.now().UTC(), User: user, Source: source, Size: len(doc)})
	if err := os.WriteFile(filepath.Join(dir, id+".json"), meta, 0o640); err != nil {
		return err
	}
	s.last[uuid] = doc
	s.pruneLocked(dir)
	return nil
}

func (s *Store) ids(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var ids []string
	for _, e := range entries {
		if id, ok := strings.CutSuffix(e.Name(), ".xml"); ok && idRE.MatchString(id) {
			ids = append(ids, id)
		}
	}
	slices.SortFunc(ids, func(a, b string) int { // numeric, newest first
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

func (s *Store) pruneLocked(dir string) {
	ids := s.ids(dir)
	for _, id := range ids[min(len(ids), Keep):] {
		_ = os.Remove(filepath.Join(dir, id+".xml"))
		_ = os.Remove(filepath.Join(dir, id+".json"))
	}
}

// List returns a machine's versions, newest first.
func (s *Store) List(uuid string) ([]Version, error) {
	dir, err := s.machineDir(uuid)
	if err != nil {
		return nil, err
	}
	out := []Version{}
	for _, id := range s.ids(dir) {
		v := Version{ID: id, Source: "outside"}
		if b, err := os.ReadFile(filepath.Join(dir, id+".json")); err == nil {
			_ = json.Unmarshal(b, &v)
		}
		out = append(out, v)
	}
	return out, nil
}

// ErrNoVersion is returned for unknown version IDs.
var ErrNoVersion = errors.New("no such version")

// Get returns one version's XML.
func (s *Store) Get(uuid, id string) (string, error) {
	dir, err := s.machineDir(uuid)
	if err != nil || !idRE.MatchString(id) {
		return "", ErrNoVersion
	}
	b, err := os.ReadFile(filepath.Join(dir, id+".xml"))
	if err != nil {
		return "", ErrNoVersion
	}
	return string(b), nil
}

// Sweep captures every machine whose definition changed since the last look: changes made
// outside VirtoMir, and a first version of machines VirtoMir has not seen yet.
func (s *Store) Sweep() {
	uuids, err := s.src.DomainUUIDs()
	if err != nil {
		return
	}
	for _, u := range uuids {
		if err := s.Capture(u, ""); err != nil {
			s.log.Debug("config history", "domain", u, "err", err)
		}
	}
}
