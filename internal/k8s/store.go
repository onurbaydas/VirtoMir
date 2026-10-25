package k8s

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

// maxEvents is how much of a cluster's log is kept.
const maxEvents = 200

// Store keeps the clusters this host runs in -state-dir/k8s.json (0600: it holds the CAs).
type Store struct {
	path     string
	mu       sync.RWMutex
	clusters map[string]Cluster
	now      func() time.Time
}

// Open loads path; a missing file is an empty store, "" keeps it in memory.
func Open(path string) (*Store, error) {
	s := &Store{path: path, clusters: map[string]Cluster{}, now: time.Now}
	if path == "" {
		return s, nil
	}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("read Kubernetes clusters: %w", err)
	}
	var list []Cluster
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("read Kubernetes clusters: %w", err)
	}
	for _, c := range list {
		s.clusters[c.ID] = c
	}
	return s, nil
}

func (s *Store) listLocked() []Cluster {
	out := make([]Cluster, 0, len(s.clusters))
	for _, c := range s.clusters {
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b Cluster) int { return strings.Compare(a.Spec.Name, b.Spec.Name) })
	return out
}

func (s *Store) saveLocked() error {
	if s.path == "" {
		return nil
	}
	raw, err := json.MarshalIndent(s.listLocked(), "", "  ")
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

// List is every cluster, with its keys.
func (s *Store) List() []Cluster {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.listLocked()
}

// Get is one cluster.
func (s *Store) Get(id string) (Cluster, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.clusters[id]
	return c, ok
}

// Create adds a new cluster; names are unique.
func (s *Store) Create(c Cluster) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.clusters[c.ID]; ok {
		return fmt.Errorf("%w: cluster %s", ErrExists, c.ID)
	}
	for _, o := range s.clusters {
		if o.Spec.Name == c.Spec.Name {
			return fmt.Errorf("%w: a cluster is already named %s", ErrExists, c.Spec.Name)
		}
	}
	c.Created, c.Updated = s.now().UTC(), s.now().UTC()
	s.clusters[c.ID] = c
	if err := s.saveLocked(); err != nil {
		delete(s.clusters, c.ID)
		return err
	}
	return nil
}

// Update changes cluster id through fn and saves it; fn's error leaves it as it was.
func (s *Store) Update(id string, fn func(*Cluster) error) (Cluster, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.clusters[id]
	if !ok {
		return Cluster{}, fmt.Errorf("%w: cluster %s", ErrNotFound, id)
	}
	old := c
	c.Nodes = slices.Clone(c.Nodes)
	c.Events = slices.Clone(c.Events)
	if err := fn(&c); err != nil {
		return old, err
	}
	c.Updated = s.now().UTC()
	s.clusters[id] = c
	if err := s.saveLocked(); err != nil {
		s.clusters[id] = old
		return old, err
	}
	return c, nil
}

// Log adds a line to a cluster's log.
func (s *Store) Log(id string, isErr bool, format string, a ...any) {
	_, _ = s.Update(id, func(c *Cluster) error {
		c.Events = append(c.Events, Event{At: s.now().UTC(), Message: fmt.Sprintf(format, a...), Error: isErr})
		if len(c.Events) > maxEvents {
			c.Events = c.Events[len(c.Events)-maxEvents:]
		}
		return nil
	})
}

// Delete removes a cluster.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.clusters[id]
	if !ok {
		return fmt.Errorf("%w: cluster %s", ErrNotFound, id)
	}
	delete(s.clusters, id)
	if err := s.saveLocked(); err != nil {
		s.clusters[id] = old
		return err
	}
	return nil
}
