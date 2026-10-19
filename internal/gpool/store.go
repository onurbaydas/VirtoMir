// Package gpool groups the cluster's GPUs into pools (D14, ADR-0071): a pool is one kind of
// GPU as machines get it (a whole card by PCI passthrough, a vGPU slice of one mdev type, or
// one NVIDIA MIG profile), so a machine asks for "2 of l40s" instead of naming a card on a
// host. Pools are cluster-wide settings, kept in -state-dir/gpools.json on every member.
package gpool

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// Kind is how a pool's GPUs reach a machine.
const (
	KindWhole = "whole" // PCI passthrough of a whole card
	KindMDev  = "mdev"  // a vGPU slice (mediated device) of type Profile
	KindMIG   = "mig"   // an NVIDIA MIG instance of profile Profile
)

// Pool is one kind of GPU.
type Pool struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Model matches the card's name (nvidia-smi) or PCI product, case-insensitively, as a
	// substring: "L40S", "A100". Empty matches every card that offers Profile.
	Model string `json:"model"`
	Kind  string `json:"kind"`
	// Profile is the mdev type (nvidia-1155) or MIG profile (1g.10gb) for those kinds.
	Profile string `json:"profile,omitempty"`
	// PerHost caps how many a machine may take on one host (0 = no cap).
	PerHost int       `json:"perHost,omitempty"`
	Created time.Time `json:"created"`
}

var slug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$`)

// Errors for the API.
var (
	ErrInvalid  = errors.New("invalid")
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("exists")
)

func invalidf(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, a...)...)
}

// Validate checks a pool.
func (p Pool) Validate() error {
	switch {
	case !slug.MatchString(p.ID):
		return invalidf("a pool ID is 3 to 40 lower-case letters, digits and dashes")
	case strings.TrimSpace(p.Name) == "":
		return invalidf("a pool needs a name")
	case p.Kind != KindWhole && p.Kind != KindMDev && p.Kind != KindMIG:
		return invalidf("the kind is whole, mdev or mig")
	case p.Kind != KindWhole && strings.TrimSpace(p.Profile) == "":
		return invalidf("a %s pool needs a profile (the vGPU type or MIG profile)", p.Kind)
	case p.Kind == KindWhole && strings.TrimSpace(p.Model) == "":
		return invalidf("a whole-GPU pool needs a model, e.g. L40S")
	case p.PerHost < 0:
		return invalidf("per host is 0 (no cap) or more")
	}
	return nil
}

// Store keeps the pools.
type Store struct {
	path    string
	mu      sync.RWMutex
	version int
	pools   map[string]Pool
}

type storeData struct {
	Version int    `json:"version"`
	Pools   []Pool `json:"pools"`
}

// Open loads path; a missing file is an empty store.
func Open(path string) (*Store, error) {
	s := &Store{path: path, pools: map[string]Pool{}}
	if path == "" {
		return s, nil
	}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("read GPU pools: %w", err)
	}
	var d storeData
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("read GPU pools: %w", err)
	}
	s.version = d.Version
	for _, p := range d.Pools {
		s.pools[p.ID] = p
	}
	return s, nil
}

func (s *Store) listLocked() []Pool {
	out := make([]Pool, 0, len(s.pools))
	for _, p := range s.pools {
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b Pool) int { return strings.Compare(a.Name, b.Name) })
	return out
}

func (s *Store) saveLocked() error {
	if s.path == "" {
		return nil
	}
	raw, err := json.MarshalIndent(storeData{Version: s.version, Pools: s.listLocked()}, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(raw); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// commitLocked saves a change as a new version, undoing it if the file cannot be written.
func (s *Store) commitLocked(undo func()) error {
	s.version++
	if err := s.saveLocked(); err != nil {
		s.version--
		undo()
		return err
	}
	return nil
}

// List returns the pools by name.
func (s *Store) List() []Pool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.listLocked()
}

// Get returns one pool.
func (s *Store) Get(id string) (Pool, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.pools[id]
	return p, ok
}

// Create adds a pool.
func (s *Store) Create(p Pool) (Pool, error) {
	if err := p.Validate(); err != nil {
		return Pool{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.pools[p.ID]; ok {
		return Pool{}, fmt.Errorf("%w: a GPU pool %s exists already", ErrExists, p.ID)
	}
	if p.Created.IsZero() {
		p.Created = time.Now().UTC()
	}
	s.pools[p.ID] = p
	return p, s.commitLocked(func() { delete(s.pools, p.ID) })
}

// Update replaces a pool (its creation time stays).
func (s *Store) Update(p Pool) (Pool, error) {
	if err := p.Validate(); err != nil {
		return Pool{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.pools[p.ID]
	if !ok {
		return Pool{}, fmt.Errorf("%w: no GPU pool %s", ErrNotFound, p.ID)
	}
	p.Created = old.Created
	s.pools[p.ID] = p
	return p, s.commitLocked(func() { s.pools[p.ID] = old })
}

// Delete removes a pool.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.pools[id]
	if !ok {
		return fmt.Errorf("%w: no GPU pool %s", ErrNotFound, id)
	}
	delete(s.pools, id)
	return s.commitLocked(func() { s.pools[id] = old })
}

// Shared is the versioned pool list members exchange (newest version wins).
type Shared struct {
	Version int    `json:"version"`
	Pools   []Pool `json:"pools"`
}

// Shared returns the pools with their version.
func (s *Store) Shared() Shared {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Shared{Version: s.version, Pools: s.listLocked()}
}

// Adopt takes another member's pools if they are newer.
func (s *Store) Adopt(sh Shared) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sh.Version <= s.version {
		return false, nil
	}
	prevV, prev := s.version, s.pools
	s.version, s.pools = sh.Version, map[string]Pool{}
	for _, p := range sh.Pools {
		s.pools[p.ID] = p
	}
	if err := s.saveLocked(); err != nil {
		s.version, s.pools = prevV, prev
		return false, err
	}
	return true, nil
}
