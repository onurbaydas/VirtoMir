package ceph

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"sort"
	"sync"
	"time"
)

// ClusterConfig is what VirtoMir knows of the Ceph cluster it bootstrapped or joined.
type ClusterConfig struct {
	FSID          string   `json:"fsid"`
	BootstrapHost string   `json:"bootstrapHost"`
	MonEndpoints  []string `json:"monEndpoints"`
	ClientUser    string   `json:"clientUser"`
	// ClientKey is the cephx key libvirt pools use; the API never shows it.
	ClientKey        string    `json:"clientKey,omitempty"`
	SSHPublicKey     string    `json:"sshPublicKey"`
	MaintenanceHosts []string  `json:"maintenanceHosts"`
	Noout            bool      `json:"noout"`
	Created          time.Time `json:"created"`
	Updated          time.Time `json:"updated"`
}

// StoragePolicy names the libvirt pool new disks of that kind go to ("fast", "replica-3",
// "cold-ec"), so people pick an intent instead of a pool. Pools are often Ceph RBD pools,
// but any pool works.
type StoragePolicy struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Pool        string    `json:"pool"`
	Default     bool      `json:"default"`
	Created     time.Time `json:"created"`
	Updated     time.Time `json:"updated"`
}

var (
	policyID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,38}[a-z0-9]$`)
	poolName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

func (p StoragePolicy) validate() error {
	switch {
	case !policyID.MatchString(p.ID):
		return invalidf("the policy ID is 2 to 40 lower-case letters, digits and dashes")
	case p.Name == "" || len(p.Name) > 80:
		return invalidf("the policy needs a name of at most 80 characters")
	case !poolName.MatchString(p.Pool):
		return invalidf("the policy needs the name of a storage pool")
	}
	return nil
}

type storeData struct {
	Config          *ClusterConfig  `json:"config,omitempty"`
	PoliciesVersion int             `json:"policiesVersion"`
	Policies        []StoragePolicy `json:"policies"`
}

// Store keeps -state-dir/ceph.json: the cluster this host manages and the storage
// policies, which are the same on every member.
type Store struct {
	path string
	mu   sync.RWMutex
	d    storeData
	now  func() time.Time
}

// Open loads path; a missing file is an empty store.
func Open(path string) (*Store, error) {
	s := &Store{path: path, now: time.Now}
	if path == "" {
		return s, nil
	}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("read Ceph store: %w", err)
	}
	if err := json.Unmarshal(raw, &s.d); err != nil {
		return nil, fmt.Errorf("read Ceph store: %w", err)
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

// Config returns the cluster configuration, if this host has one.
func (s *Store) Config() (ClusterConfig, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.d.Config == nil {
		return ClusterConfig{}, false
	}
	c := *s.d.Config
	c.MonEndpoints = slices.Clone(c.MonEndpoints)
	c.MaintenanceHosts = slices.Clone(c.MaintenanceHosts)
	return c, true
}

// SaveConfig keeps c.
func (s *Store) SaveConfig(c ClusterConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c.Updated = s.now().UTC()
	if c.Created.IsZero() {
		c.Created = c.Updated
	}
	old := s.d.Config
	s.d.Config = &c
	if err := s.saveLocked(); err != nil {
		s.d.Config = old
		return err
	}
	return nil
}

// updateConfig changes the configuration with fn; without one it is ErrNotFound.
func (s *Store) updateConfig(fn func(c *ClusterConfig)) error {
	c, ok := s.Config()
	if !ok {
		return fmt.Errorf("%w: this host manages no Ceph cluster", ErrNotFound)
	}
	fn(&c)
	return s.SaveConfig(c)
}

// SetNoout records whether the cluster's noout flag is set.
func (s *Store) SetNoout(noout bool) error {
	return s.updateConfig(func(c *ClusterConfig) { c.Noout = noout })
}

// SetHostMaintenance records a host entering or leaving maintenance.
func (s *Store) SetHostMaintenance(host string, on bool) error {
	return s.updateConfig(func(c *ClusterConfig) {
		c.MaintenanceHosts = slices.DeleteFunc(c.MaintenanceHosts, func(h string) bool { return h == host })
		if on {
			c.MaintenanceHosts = append(c.MaintenanceHosts, host)
			sort.Strings(c.MaintenanceHosts)
		}
	})
}

// Policies lists the storage policies by name.
func (s *Store) Policies() []StoragePolicy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := slices.Clone(s.d.Policies)
	if out == nil {
		out = []StoragePolicy{}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Policy returns one policy.
func (s *Store) Policy(id string) (StoragePolicy, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	i := slices.IndexFunc(s.d.Policies, func(p StoragePolicy) bool { return p.ID == id })
	if i < 0 {
		return StoragePolicy{}, false
	}
	return s.d.Policies[i], true
}

// changePolicies runs fn on a copy of the policies and keeps it as a new version.
func (s *Store) changePolicies(fn func(list []StoragePolicy) ([]StoragePolicy, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := fn(slices.Clone(s.d.Policies))
	if err != nil {
		return err
	}
	old, oldVersion := s.d.Policies, s.d.PoliciesVersion
	s.d.Policies, s.d.PoliciesVersion = list, s.d.PoliciesVersion+1
	if err := s.saveLocked(); err != nil {
		s.d.Policies, s.d.PoliciesVersion = old, oldVersion
		return err
	}
	return nil
}

// onlyDefault clears Default on every policy except id.
func onlyDefault(list []StoragePolicy, id string) {
	for i := range list {
		if list[i].ID != id {
			list[i].Default = false
		}
	}
}

// CreatePolicy adds a policy.
func (s *Store) CreatePolicy(p StoragePolicy) (StoragePolicy, error) {
	if err := p.validate(); err != nil {
		return StoragePolicy{}, err
	}
	err := s.changePolicies(func(list []StoragePolicy) ([]StoragePolicy, error) {
		if slices.ContainsFunc(list, func(x StoragePolicy) bool { return x.ID == p.ID }) {
			return nil, fmt.Errorf("%w: a policy %s exists already", ErrExists, p.ID)
		}
		p.Created = s.now().UTC()
		p.Updated = p.Created
		if p.Default {
			onlyDefault(list, p.ID)
		}
		return append(list, p), nil
	})
	return p, err
}

// UpdatePolicy changes a policy's name, description, pool and default flag.
func (s *Store) UpdatePolicy(p StoragePolicy) (StoragePolicy, error) {
	if err := p.validate(); err != nil {
		return StoragePolicy{}, err
	}
	err := s.changePolicies(func(list []StoragePolicy) ([]StoragePolicy, error) {
		i := slices.IndexFunc(list, func(x StoragePolicy) bool { return x.ID == p.ID })
		if i < 0 {
			return nil, fmt.Errorf("%w: no policy %s", ErrNotFound, p.ID)
		}
		p.Created, p.Updated = list[i].Created, s.now().UTC()
		list[i] = p
		if p.Default {
			onlyDefault(list, p.ID)
		}
		return list, nil
	})
	return p, err
}

// DeletePolicy removes a policy; machines keep their disks where they are.
func (s *Store) DeletePolicy(id string) error {
	return s.changePolicies(func(list []StoragePolicy) ([]StoragePolicy, error) {
		i := slices.IndexFunc(list, func(x StoragePolicy) bool { return x.ID == id })
		if i < 0 {
			return nil, fmt.Errorf("%w: no policy %s", ErrNotFound, id)
		}
		return slices.Delete(list, i, i+1), nil
	})
}

// PoliciesShared is the versioned policy list members exchange (newest version wins).
type PoliciesShared struct {
	Version  int             `json:"version"`
	Policies []StoragePolicy `json:"policies"`
}

// Shared returns the policies with their version.
func (s *Store) Shared() PoliciesShared {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return PoliciesShared{Version: s.d.PoliciesVersion, Policies: slices.Clone(s.d.Policies)}
}

// Adopt takes over a member's policies when they are newer; it reports whether it did.
func (s *Store) Adopt(sh PoliciesShared) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sh.Version <= s.d.PoliciesVersion {
		return false, nil
	}
	for _, p := range sh.Policies {
		if err := p.validate(); err != nil {
			return false, err
		}
	}
	old, oldVersion := s.d.Policies, s.d.PoliciesVersion
	s.d.Policies, s.d.PoliciesVersion = slices.Clone(sh.Policies), sh.Version
	if err := s.saveLocked(); err != nil {
		s.d.Policies, s.d.PoliciesVersion = old, oldVersion
		return false, err
	}
	return true, nil
}
