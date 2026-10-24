package storageeff

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"regexp"
	"slices"
	"sync"
)

// Tiers are labels for pools by speed; disks move to a tier by moving to its pool.
var Tiers = []string{"nvme", "ssd", "hdd"}

var poolNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// TiersShared is the versioned pool → tier map members exchange (newest version wins):
// pools of the same name on different hosts are the same kind of storage.
type TiersShared struct {
	Version int               `json:"version"`
	Tiers   map[string]string `json:"tiers"`
}

type storeData struct {
	TiersShared
	// NVMe are the NVMe/TCP subsystems this host connected through VirtoMir; they are
	// connected again when virtomird starts.
	NVMe []Target `json:"nvme"`
}

// Store keeps -state-dir/storageeff.json.
type Store struct {
	path string
	mu   sync.RWMutex
	d    storeData
}

// Open loads path; a missing file is an empty store.
func Open(path string) (*Store, error) {
	s := &Store{path: path, d: storeData{TiersShared: TiersShared{Tiers: map[string]string{}}}}
	if path == "" {
		return s, nil
	}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("read storage efficiency store: %w", err)
	}
	if err := json.Unmarshal(raw, &s.d); err != nil {
		return nil, fmt.Errorf("read storage efficiency store: %w", err)
	}
	if s.d.Tiers == nil {
		s.d.Tiers = map[string]string{}
	}
	return s, nil
}

func (s *Store) saveLocked() error {
	if s.path == "" {
		return nil
	}
	raw, err := json.MarshalIndent(s.d, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(raw); err == nil {
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

// Tiers returns pool → tier.
func (s *Store) Tiers() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return maps.Clone(s.d.Tiers)
}

// SetTier labels a pool; "" removes its label.
func (s *Store) SetTier(pool, tier string) error {
	if !poolNameRE.MatchString(pool) {
		return invalidf("%q is not a pool name", pool)
	}
	if tier != "" && !slices.Contains(Tiers, tier) {
		return invalidf("the tier is nvme, ssd or hdd")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old, oldVersion := maps.Clone(s.d.Tiers), s.d.Version
	if tier == "" {
		delete(s.d.Tiers, pool)
	} else {
		s.d.Tiers[pool] = tier
	}
	s.d.Version++
	if err := s.saveLocked(); err != nil {
		s.d.Tiers, s.d.Version = old, oldVersion
		return err
	}
	return nil
}

// Shared returns the tiers with their version.
func (s *Store) Shared() TiersShared {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return TiersShared{Version: s.d.Version, Tiers: maps.Clone(s.d.Tiers)}
}

// Adopt takes over a member's tiers when they are newer; it reports whether it did.
func (s *Store) Adopt(sh TiersShared) (bool, error) {
	for pool, tier := range sh.Tiers {
		if !poolNameRE.MatchString(pool) || !slices.Contains(Tiers, tier) {
			return false, invalidf("tier %q of pool %q", tier, pool)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if sh.Version <= s.d.Version {
		return false, nil
	}
	old := s.d.TiersShared
	s.d.TiersShared = TiersShared{Version: sh.Version, Tiers: maps.Clone(sh.Tiers)}
	if s.d.Tiers == nil {
		s.d.Tiers = map[string]string{}
	}
	if err := s.saveLocked(); err != nil {
		s.d.TiersShared = old
		return false, err
	}
	return true, nil
}

// NVMeTargets are the subsystems to connect at start.
func (s *Store) NVMeTargets() []Target {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.d.NVMe)
}

func (s *Store) setNVMe(fn func([]Target) []Target) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.d.NVMe
	s.d.NVMe = fn(slices.Clone(s.d.NVMe))
	if err := s.saveLocked(); err != nil {
		s.d.NVMe = old
		return err
	}
	return nil
}
