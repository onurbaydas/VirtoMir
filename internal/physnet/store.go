package physnet

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"slices"
	"sort"
	"sync"
	"time"
)

// errUnchanged ends a change that needs no new version.
var errUnchanged = errors.New("unchanged")

// Shared is what members exchange (newest version wins): the IP pools with their
// allocations and the dynamic DNS settings.
type Shared struct {
	Version int       `json:"version"`
	Pools   []IPPool  `json:"pools"`
	DNS     DNSConfig `json:"dns"`
}

type state struct {
	Shared
	// BGP is this host's own.
	BGP BGPConfig `json:"bgp"`
}

// Store keeps -state-dir/physnet.json.
type Store struct {
	path string
	mu   sync.RWMutex
	st   state
	now  func() time.Time
}

// Open loads path; a missing file is an empty store.
func Open(path string) (*Store, error) {
	s := &Store{path: path, now: time.Now, st: state{Shared: Shared{Pools: []IPPool{}}, BGP: BGPConfig{Neighbors: []BGPNeighbor{}, Announce: []string{}}}}
	if path == "" {
		return s, nil
	}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("read physical network store: %w", err)
	}
	if err := json.Unmarshal(raw, &s.st); err != nil {
		return nil, fmt.Errorf("read physical network store: %w", err)
	}
	if s.st.Pools == nil {
		s.st.Pools = []IPPool{}
	}
	return s, nil
}

func (s *Store) saveLocked() error {
	if s.path == "" {
		return nil
	}
	raw, err := json.MarshalIndent(s.st, "", "  ")
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

func clonePools(ps []IPPool) []IPPool {
	out := make([]IPPool, len(ps))
	for i, p := range ps {
		p.DNSServers, p.Reserved, p.Allocations = slices.Clone(p.DNSServers), slices.Clone(p.Reserved), slices.Clone(p.Allocations)
		if p.Allocations == nil {
			p.Allocations = []Allocation{}
		}
		out[i] = p
	}
	return out
}

// change runs fn on a copy of the shared state and keeps it as a new version.
func (s *Store) change(fn func(st *state) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := s.st
	cp.Pools = clonePools(s.st.Pools)
	cp.DNS.ReverseZones = slices.Clone(s.st.DNS.ReverseZones)
	if err := fn(&cp); err != nil {
		if errors.Is(err, errUnchanged) {
			return nil
		}
		return err
	}
	cp.Version++
	old := s.st
	s.st = cp
	if err := s.saveLocked(); err != nil {
		s.st = old
		return err
	}
	return nil
}

// Pools lists the IP pools by name.
func (s *Store) Pools() []IPPool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := clonePools(s.st.Pools)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Pool returns one pool.
func (s *Store) Pool(id string) (IPPool, bool) {
	for _, p := range s.Pools() {
		if p.ID == id {
			return p, true
		}
	}
	return IPPool{}, false
}

// CreatePool adds a pool; it must not overlap another pool's range.
func (s *Store) CreatePool(p IPPool) (IPPool, error) {
	p.Allocations = []Allocation{}
	if err := p.validate(); err != nil {
		return IPPool{}, err
	}
	err := s.change(func(st *state) error {
		for _, o := range st.Pools {
			if o.ID == p.ID {
				return fmt.Errorf("%w: an IP pool %s exists already", ErrExists, p.ID)
			}
			if rangesOverlap(o, p) {
				return fmt.Errorf("%w: the range overlaps pool %s (%s – %s)", ErrConflict, o.Name, o.RangeStart, o.RangeEnd)
			}
		}
		p.Created = s.now().UTC()
		p.Updated = p.Created
		st.Pools = append(st.Pools, p)
		return nil
	})
	return p, err
}

// UpdatePool changes a pool; addresses machines hold must stay inside it.
func (s *Store) UpdatePool(p IPPool) (IPPool, error) {
	if err := p.validate(); err != nil {
		return IPPool{}, err
	}
	err := s.change(func(st *state) error {
		i := slices.IndexFunc(st.Pools, func(o IPPool) bool { return o.ID == p.ID })
		if i < 0 {
			return fmt.Errorf("%w: no IP pool %s", ErrNotFound, p.ID)
		}
		for j, o := range st.Pools {
			if j != i && rangesOverlap(o, p) {
				return fmt.Errorf("%w: the range overlaps pool %s (%s – %s)", ErrConflict, o.Name, o.RangeStart, o.RangeEnd)
			}
		}
		p.Allocations, p.Created, p.Updated = st.Pools[i].Allocations, st.Pools[i].Created, s.now().UTC()
		for _, a := range p.Allocations {
			if ip := mustAddr(a.IP); !p.Prefix().Contains(ip) || ip.Less(mustAddr(p.RangeStart)) || mustAddr(p.RangeEnd).Less(ip) {
				return fmt.Errorf("%w: %s (held by %s) would fall outside the pool", ErrBusy, a.IP, a.VMName)
			}
		}
		st.Pools[i] = p
		return nil
	})
	return p, err
}

// DeletePool removes a pool nobody holds addresses of.
func (s *Store) DeletePool(id string) error {
	return s.change(func(st *state) error {
		i := slices.IndexFunc(st.Pools, func(o IPPool) bool { return o.ID == id })
		if i < 0 {
			return fmt.Errorf("%w: no IP pool %s", ErrNotFound, id)
		}
		if n := len(st.Pools[i].Allocations); n > 0 {
			return fmt.Errorf("%w: %d machines hold addresses of %s", ErrBusy, n, st.Pools[i].Name)
		}
		st.Pools = slices.Delete(st.Pools, i, i+1)
		return nil
	})
}

// DNS returns the dynamic DNS settings.
func (s *Store) DNS() DNSConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c := s.st.DNS
	c.ReverseZones = slices.Clone(c.ReverseZones)
	return c
}

// SetDNS saves the dynamic DNS settings; an empty secret keeps the stored one.
func (s *Store) SetDNS(c DNSConfig) (DNSConfig, error) {
	err := s.change(func(st *state) error {
		if c.Secret == "" {
			c.Secret = st.DNS.Secret
		}
		if err := c.Normalize(); err != nil {
			return err
		}
		st.DNS = c
		return nil
	})
	return c, err
}

// BGP returns this host's BGP settings.
func (s *Store) BGP() BGPConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c := s.st.BGP
	c.Neighbors, c.Announce = slices.Clone(c.Neighbors), slices.Clone(c.Announce)
	return c
}

// SetBGP saves this host's BGP settings (not shared: router IDs differ).
func (s *Store) SetBGP(c BGPConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.st.BGP
	s.st.BGP = c
	if err := s.saveLocked(); err != nil {
		s.st.BGP = old
		return err
	}
	return nil
}

// Shared returns the pools and DNS settings with their version.
func (s *Store) Shared() Shared {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sh := s.st.Shared
	sh.Pools = clonePools(sh.Pools)
	return sh
}

// Adopt takes over a member's shared state when it is newer; it reports whether it did.
func (s *Store) Adopt(sh Shared) (bool, error) {
	for i := range sh.Pools {
		if err := sh.Pools[i].validate(); err != nil {
			return false, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if sh.Version <= s.st.Version {
		return false, nil
	}
	old := s.st.Shared
	s.st.Shared = Shared{Version: sh.Version, Pools: clonePools(sh.Pools), DNS: sh.DNS}
	if err := s.saveLocked(); err != nil {
		s.st.Shared = old
		return false, err
	}
	return true, nil
}

func mustAddr(s string) netip.Addr { return netip.MustParseAddr(s) }

func rangesOverlap(a, b IPPool) bool {
	as, ae, bs, be := mustAddr(a.RangeStart), mustAddr(a.RangeEnd), mustAddr(b.RangeStart), mustAddr(b.RangeEnd)
	return as.BitLen() == bs.BitLen() && !ae.Less(bs) && !be.Less(as)
}
