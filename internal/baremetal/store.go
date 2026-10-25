package baremetal

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

const maxEvents = 100

// Store keeps the settings and the hosts in -state-dir/baremetal.json (0600: BMC passwords).
type Store struct {
	path string
	mu   sync.RWMutex
	st   storeData
	now  func() time.Time
}

type storeData struct {
	Settings Settings `json:"settings"`
	Hosts    []Host   `json:"hosts"`
}

// Open loads path; a missing file is an empty store, "" keeps it in memory.
func Open(path string) (*Store, error) {
	s := &Store{path: path, now: time.Now}
	_ = s.st.Settings.Normalize() // defaults
	if path == "" {
		return s, nil
	}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("read bare-metal hosts: %w", err)
	}
	if err := json.Unmarshal(raw, &s.st); err != nil {
		return nil, fmt.Errorf("read bare-metal hosts: %w", err)
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

// change applies fn and saves, or leaves everything as it was.
func (s *Store) change(fn func(*storeData) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, _ := json.Marshal(s.st)
	if err := fn(&s.st); err != nil {
		return err
	}
	if err := s.saveLocked(); err != nil {
		_ = json.Unmarshal(raw, &s.st)
		return err
	}
	return nil
}

// Settings are the network boot settings.
func (s *Store) Settings() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.st.Settings
}

// SetSettings replaces the settings.
func (s *Store) SetSettings(n Settings) error {
	if err := n.Normalize(); err != nil {
		return err
	}
	return s.change(func(st *storeData) error { st.Settings = n; return nil })
}

// Hosts is every host, by name.
func (s *Store) Hosts() []Host {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := slices.Clone(s.st.Hosts)
	slices.SortFunc(out, func(a, b Host) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Host is one host.
func (s *Store) Host(id string) (Host, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	i := slices.IndexFunc(s.st.Hosts, func(h Host) bool { return h.ID == id })
	if i < 0 {
		return Host{}, false
	}
	return s.st.Hosts[i], true
}

// ByMAC is the host booting from mac.
func (s *Store) ByMAC(mac string) (Host, bool) {
	mac = NormalizeMAC(mac)
	s.mu.RLock()
	defer s.mu.RUnlock()
	i := slices.IndexFunc(s.st.Hosts, func(h Host) bool { return mac != "" && h.MAC == mac })
	if i < 0 {
		return Host{}, false
	}
	return s.st.Hosts[i], true
}

// ByToken is the host being installed with token.
func (s *Store) ByToken(token string) (Host, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	i := slices.IndexFunc(s.st.Hosts, func(h Host) bool { return token != "" && h.Token == token })
	if i < 0 {
		return Host{}, false
	}
	return s.st.Hosts[i], true
}

// Add registers a new host; names and MACs are unique.
func (s *Store) Add(h Host) (Host, error) {
	if err := h.Validate(); err != nil {
		return Host{}, err
	}
	err := s.change(func(st *storeData) error {
		for _, o := range st.Hosts {
			if o.Name == h.Name || o.MAC == h.MAC {
				return fmt.Errorf("%w: %s (%s) is registered already", ErrExists, o.Name, o.MAC)
			}
		}
		h.ID, h.State, h.Created, h.Updated = randomHex(8), StateRegistered, s.now().UTC(), s.now().UTC()
		h.Events = []Event{{At: h.Created, Message: "registered"}}
		st.Hosts = append(st.Hosts, h)
		return nil
	})
	return h, err
}

// Update changes host id through fn (validated, names and MACs unique).
func (s *Store) Update(id string, fn func(*Host) error) (Host, error) {
	var out Host
	err := s.change(func(st *storeData) error {
		i := slices.IndexFunc(st.Hosts, func(h Host) bool { return h.ID == id })
		if i < 0 {
			return fmt.Errorf("%w: host %s", ErrNotFound, id)
		}
		h := st.Hosts[i]
		h.Events = slices.Clone(h.Events)
		if err := fn(&h); err != nil {
			return err
		}
		if err := h.Validate(); err != nil {
			return err
		}
		for j, o := range st.Hosts {
			if j != i && (o.Name == h.Name || o.MAC == h.MAC) {
				return fmt.Errorf("%w: %s (%s) is registered already", ErrExists, o.Name, o.MAC)
			}
		}
		if len(h.Events) > maxEvents {
			h.Events = h.Events[len(h.Events)-maxEvents:]
		}
		h.Updated = s.now().UTC()
		st.Hosts[i] = h
		out = h
		return nil
	})
	return out, err
}

// SetState moves a host to state with a log line (and an error flag for failures).
func (s *Store) SetState(id, state, msg string) (Host, error) {
	return s.Update(id, func(h *Host) error {
		h.State, h.Message = state, ""
		if state == StateFailed {
			h.Message = msg
		}
		h.Events = append(h.Events, Event{At: s.now().UTC(), Message: msg, Error: state == StateFailed})
		return nil
	})
}

// Log adds a line to a host's log.
func (s *Store) Log(id string, isErr bool, msg string) {
	_, _ = s.Update(id, func(h *Host) error {
		h.Events = append(h.Events, Event{At: s.now().UTC(), Message: msg, Error: isErr})
		return nil
	})
}

// Delete forgets a host.
func (s *Store) Delete(id string) error {
	return s.change(func(st *storeData) error {
		i := slices.IndexFunc(st.Hosts, func(h Host) bool { return h.ID == id })
		if i < 0 {
			return fmt.Errorf("%w: host %s", ErrNotFound, id)
		}
		st.Hosts = slices.Delete(st.Hosts, i, i+1)
		return nil
	})
}
