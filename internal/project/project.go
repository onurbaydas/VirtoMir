// Package project groups machines, storage pools and networks into projects with
// members and limits (ADR-0060): multi-tenancy for VirtoMir. A project owns the
// resources created inside it; members reach it by direct membership or through
// one of their Linux groups, and the highest role wins.
package project

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Member gives one user (Kind "user") or everyone in a group (Kind "group") a
// Role in a project: "viewer", "operator" or "admin".
type Member struct {
	Kind string `json:"kind"` // user or group
	Name string `json:"name"`
	Role string `json:"role"` // viewer, operator or admin
}

// Limits caps a project's machines together; zero means unlimited.
type Limits struct {
	Machines  int   `json:"machines"`
	VCPUs     int   `json:"vcpus"`
	MemoryMiB int64 `json:"memoryMiB"`
	DiskGiB   int64 `json:"diskGiB"`
	// GPUs caps GPUs per GPU pool ID (ADR-0071); a pool missing or at 0 is not limited.
	GPUs map[string]int `json:"gpus,omitempty"`
}

// Usage is what a project's machines take now.
type Usage struct {
	Machines  int   `json:"machines"`
	VCPUs     int   `json:"vcpus"`
	MemoryMiB int64 `json:"memoryMiB"`
	DiskGiB   int64 `json:"diskGiB"`
	// GPUs per GPU pool ID.
	GPUs map[string]int `json:"gpus,omitempty"`
}

// Project is one tenant: its machines, storage pools and networks, who may use
// them and up to how much.
type Project struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Description  string    `json:"description,omitempty"`
	Members      []Member  `json:"members"`
	Limits       Limits    `json:"limits"`
	StoragePools []string  `json:"storagePools"`
	Networks     []string  `json:"networks"`
	Created      time.Time `json:"created"`
}

var slug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$`)

// rank orders roles from lowest to highest.
func rank(role string) int {
	switch role {
	case "viewer":
		return 1
	case "operator":
		return 2
	case "admin":
		return 3
	}
	return 0
}

// normalizeLocked trims the whitespace around a project's ID, name and member
// names so that lookups and dedup never see padded copies of the same string.
func normalizeLocked(p *Project) {
	p.ID = strings.TrimSpace(p.ID)
	p.Name = strings.TrimSpace(p.Name)
	for i := range p.Members {
		p.Members[i].Name = strings.TrimSpace(p.Members[i].Name)
	}
}

func validMember(m Member) bool {
	if m.Kind != "user" && m.Kind != "group" {
		return false
	}
	return rank(m.Role) > 0 && strings.TrimSpace(m.Name) != ""
}

// validateLocked checks a whole project, as Create and Update receive it.
func validateLocked(p Project, existing map[string]*Project, selfID string) error {
	if !slug.MatchString(p.ID) {
		return fmt.Errorf("%q is not a valid project ID (lower-case letters, digits and dashes)", p.ID)
	}
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("the project needs a name")
	}
	for _, q := range existing {
		if q.ID != selfID && strings.EqualFold(q.Name, p.Name) {
			return fmt.Errorf("a project named %q exists already", p.Name)
		}
	}
	seen := map[string]bool{}
	for _, m := range p.Members {
		if !validMember(m) {
			return fmt.Errorf("member %q is a user or a group with role viewer, operator or admin", m.Name)
		}
		k := m.Kind + ":" + m.Name
		if seen[k] {
			return fmt.Errorf("%s %s is a member twice", m.Kind, m.Name)
		}
		seen[k] = true
	}
	if p.Limits.Machines < 0 || p.Limits.VCPUs < 0 || p.Limits.MemoryMiB < 0 || p.Limits.DiskGiB < 0 {
		return errors.New("limits cannot be negative")
	}
	for pool, n := range p.Limits.GPUs {
		if n < 0 {
			return fmt.Errorf("the GPU quota for pool %s cannot be negative", pool)
		}
	}
	return nil
}

type Store struct {
	path string
	mu   sync.RWMutex
	p    map[string]*Project
}

func Open(path string) (*Store, error) {
	s := &Store{path: path, p: map[string]*Project{}}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, err
	}
	var list []Project
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	parsed := map[string]*Project{}
	for i := range list {
		p := list[i]
		normalizeLocked(&p)
		if _, dup := parsed[p.ID]; dup {
			return nil, fmt.Errorf("%s: project %q appears twice", path, p.ID)
		}
		if err := validateLocked(p, parsed, p.ID); err != nil {
			return nil, fmt.Errorf("%s: project %q: %w", path, p.ID, err)
		}
		if p.Members == nil {
			p.Members = []Member{}
		}
		if p.StoragePools == nil {
			p.StoragePools = []string{}
		}
		if p.Networks == nil {
			p.Networks = []string{}
		}
		parsed[p.ID] = &p
	}
	s.p = parsed
	return s, nil
}

func (p Project) clone() Project {
	q := p
	q.Members = append([]Member{}, p.Members...)
	q.StoragePools = append([]string{}, p.StoragePools...)
	q.Networks = append([]string{}, p.Networks...)
	return q
}

func (s *Store) save() error {
	list := s.listLocked()
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o750); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := func() error {
		f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		if _, err := f.Write(b); err != nil {
			f.Close()
			os.Remove(tmp)
			return err
		}
		if err := f.Sync(); err != nil {
			f.Close()
			os.Remove(tmp)
			return err
		}
		return f.Close()
	}(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func (s *Store) listLocked() []Project {
	list := make([]Project, 0, len(s.p))
	for _, p := range s.p {
		list = append(list, p.clone())
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list
}

// List returns every project, by ID.
func (s *Store) List() []Project {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.listLocked()
}

func (s *Store) Get(id string) (Project, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.p[id]
	if !ok {
		return Project{}, false
	}
	return p.clone(), true
}

// Create adds a project.
func (s *Store) Create(p Project) (Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	normalizeLocked(&p)
	if err := validateLocked(p, s.p, ""); err != nil {
		return Project{}, err
	}
	if s.p[p.ID] != nil {
		return Project{}, fmt.Errorf("a project %q exists already", p.ID)
	}
	if p.Members == nil {
		p.Members = []Member{}
	}
	if p.StoragePools == nil {
		p.StoragePools = []string{}
	}
	if p.Networks == nil {
		p.Networks = []string{}
	}
	if p.Created.IsZero() {
		p.Created = time.Now().UTC()
	}
	s.p[p.ID] = &p
	if err := s.save(); err != nil {
		delete(s.p, p.ID)
		return Project{}, err
	}
	return p.clone(), nil
}

// Update changes a project's name, description, members, limits, pools and networks.
func (s *Store) Update(p Project) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.p[p.ID]
	if !ok {
		return fmt.Errorf("no project %q", p.ID)
	}
	next := cur.clone()
	next.Name, next.Description = strings.TrimSpace(p.Name), p.Description
	next.Members = append([]Member{}, p.Members...)
	next.Limits = p.Limits
	next.StoragePools = append([]string{}, p.StoragePools...)
	next.Networks = append([]string{}, p.Networks...)
	normalizeLocked(&next)
	if err := validateLocked(next, s.p, p.ID); err != nil {
		return err
	}
	prev := *cur
	*cur = next
	if err := s.save(); err != nil {
		*cur = prev
		return err
	}
	return nil
}

// Delete removes a project.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.p[id] == nil {
		return fmt.Errorf("no project %q", id)
	}
	prev := *s.p[id]
	delete(s.p, id)
	if err := s.save(); err != nil {
		s.p[id] = &prev
		return err
	}
	return nil
}

// RoleFor is the highest role the user has in a project, directly or through any
// of their groups; ok is false when they are not a member.
func (s *Store) RoleFor(projectID, user string, groups []string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.roleForLocked(projectID, user, groups)
}

func (s *Store) roleForLocked(projectID, user string, groups []string) (string, bool) {
	p, ok := s.p[projectID]
	if !ok {
		return "", false
	}
	best, found := "", false
	for _, m := range p.Members {
		var hit bool
		if m.Kind == "user" {
			hit = m.Name == user
		} else {
			for _, g := range groups {
				if g == m.Name {
					hit = true
					break
				}
			}
		}
		if hit && (!found || rank(m.Role) > rank(best)) {
			best, found = m.Role, true
		}
	}
	return best, found
}

// ProjectsFor is every project the user can see, directly or through any of
// their groups, by ID.
func (s *Store) ProjectsFor(user string, groups []string) []Project {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := s.listLocked()
	out := make([]Project, 0, len(list))
	for _, p := range list {
		if _, ok := s.roleForLocked(p.ID, user, groups); ok {
			out = append(out, p)
		}
	}
	return out
}

// Exceeded reports the first resource that demand would push past the limits,
// given current usage; zero limits are unlimited.
func (l Limits) Exceeded(usage, demand Usage) error {
	if l.Machines > 0 && usage.Machines+demand.Machines > l.Machines {
		return fmt.Errorf("this would exceed the project's limit of %d machines (%d used, this needs %d)", l.Machines, usage.Machines, demand.Machines)
	}
	if l.VCPUs > 0 && usage.VCPUs+demand.VCPUs > l.VCPUs {
		return fmt.Errorf("this would exceed the project's limit of %d vCPUs (%d used, this needs %d)", l.VCPUs, usage.VCPUs, demand.VCPUs)
	}
	if l.MemoryMiB > 0 && usage.MemoryMiB+demand.MemoryMiB > l.MemoryMiB {
		return fmt.Errorf("this would exceed the project's limit of %d MiB of memory (%d used, this needs %d)", l.MemoryMiB, usage.MemoryMiB, demand.MemoryMiB)
	}
	if l.DiskGiB > 0 && usage.DiskGiB+demand.DiskGiB > l.DiskGiB {
		return fmt.Errorf("this would exceed the project's limit of %d GiB of disk space (%d used, this needs %d)", l.DiskGiB, usage.DiskGiB, demand.DiskGiB)
	}
	pools := make([]string, 0, len(l.GPUs))
	for pool := range l.GPUs {
		pools = append(pools, pool)
	}
	sort.Strings(pools) // the same error every time
	for _, pool := range pools {
		limit, used, need := l.GPUs[pool], usage.GPUs[pool], demand.GPUs[pool]
		if limit > 0 && need > 0 && used+need > limit {
			return fmt.Errorf("this would exceed the project's GPU quota of %d for pool %s (%d used, this needs %d)", limit, pool, used, need)
		}
	}
	return nil
}
