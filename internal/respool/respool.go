// Package respool groups machines into resource pools with hard limits (ADR-0030, made
// binding in ADR-0032): vCPUs, memory and disk space per storage pool. Pools nest; a
// machine in a child pool counts against every ancestor, so a parent caps its whole tree.
// Limits are checked whenever a machine is created, grows, gets a disk or is moved into a
// pool, for everyone (admins included): that is what a limit is for.
package respool

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

// ResourcePool is a group of machines with limits (0 means unlimited).
type ResourcePool struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ParentID    string `json:"parentId,omitempty"`
	Description string `json:"description,omitempty"`
	// CPULimit is the most vCPUs the pool's machines may have together.
	CPULimit int `json:"cpuLimit"`
	// MemoryLimitMiB is the most memory (maximum memory of each machine) together.
	MemoryLimitMiB uint64 `json:"memoryLimitMiB"`
	// StorageLimits caps disk capacity per libvirt storage pool name, in bytes.
	StorageLimits map[string]uint64 `json:"storageLimits"`
	Machines      []string          `json:"machines"` // machine UUIDs (lower case)
	CreatedAt     time.Time         `json:"createdAt"`
}

// Usage is what a pool's machines (its children's included) use.
type Usage struct {
	PoolID       string            `json:"poolId"`
	Machines     int               `json:"machines"`
	VCPUs        int               `json:"vcpus"`
	MemoryMiB    uint64            `json:"memoryMiB"`
	StorageBytes map[string]uint64 `json:"storageBytes"`
}

// MachineUse is one machine's resources as the limits count them.
type MachineUse struct {
	VCPUs        int
	MemoryMiB    uint64
	StorageBytes map[string]uint64 // per storage pool
}

// Demand is what an operation adds.
type Demand struct {
	VCPUs        int
	MemoryMiB    uint64
	StoragePool  string
	StorageBytes uint64
}

type Store struct {
	path  string
	mu    sync.Mutex
	pools map[string]*ResourcePool
}

func Open(path string) (*Store, error) {
	s := &Store{path: path, pools: map[string]*ResourcePool{}}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, err
	}
	var list []ResourcePool
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for i := range list {
		p := list[i]
		p.normalize()
		s.pools[p.ID] = &p
	}
	return s, nil
}

func (p *ResourcePool) normalize() {
	if p.StorageLimits == nil {
		p.StorageLimits = map[string]uint64{}
	}
	if p.Machines == nil {
		p.Machines = []string{}
	}
	for i, m := range p.Machines {
		p.Machines[i] = strings.ToLower(m)
	}
}

func (p ResourcePool) clone() ResourcePool {
	q := p
	q.StorageLimits = make(map[string]uint64, len(p.StorageLimits))
	for k, v := range p.StorageLimits {
		q.StorageLimits[k] = v
	}
	q.Machines = append([]string{}, p.Machines...)
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
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) listLocked() []ResourcePool {
	list := make([]ResourcePool, 0, len(s.pools))
	for _, p := range s.pools {
		list = append(list, p.clone())
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list
}

// List returns every pool, by name.
func (s *Store) List() []ResourcePool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listLocked()
}

func (s *Store) Get(id string) (ResourcePool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pools[id]
	if !ok {
		return ResourcePool{}, false
	}
	return p.clone(), true
}

var nonID = regexp.MustCompile(`[^a-z0-9]+`)

// Create adds a pool. Its limits must fit inside its parent's.
func (s *Store) Create(p ResourcePool) (ResourcePool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return ResourcePool{}, errors.New("the pool needs a name")
	}
	for _, q := range s.pools {
		if strings.EqualFold(q.Name, p.Name) {
			return ResourcePool{}, fmt.Errorf("a pool named %q exists already", p.Name)
		}
	}
	base := strings.Trim(nonID.ReplaceAllString(strings.ToLower(p.Name), "-"), "-")
	if base == "" {
		base = "pool"
	}
	p.ID = base
	for i := 2; s.pools[p.ID] != nil; i++ {
		p.ID = fmt.Sprintf("%s-%d", base, i)
	}
	p.Machines = nil
	p.normalize()
	if err := s.validateLocked(p); err != nil {
		return ResourcePool{}, err
	}
	p.CreatedAt = time.Now().UTC()
	s.pools[p.ID] = &p
	if err := s.save(); err != nil {
		delete(s.pools, p.ID)
		return ResourcePool{}, err
	}
	return p.clone(), nil
}

// Update changes name, parent, description and limits (not the machines).
func (s *Store) Update(p ResourcePool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.pools[p.ID]
	if !ok {
		return fmt.Errorf("no resource pool %q", p.ID)
	}
	next := cur.clone()
	next.Name = strings.TrimSpace(p.Name)
	if next.Name == "" {
		return errors.New("the pool needs a name")
	}
	next.ParentID, next.Description = p.ParentID, p.Description
	next.CPULimit, next.MemoryLimitMiB = p.CPULimit, p.MemoryLimitMiB
	next.StorageLimits = p.StorageLimits
	next.normalize()
	if err := s.validateLocked(next); err != nil {
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

// validateLocked: an existing parent, no cycle, no negative limits, and no limit above the
// parent's (a child cannot promise what its parent does not have).
func (s *Store) validateLocked(p ResourcePool) error {
	if p.CPULimit < 0 {
		return errors.New("limits cannot be negative")
	}
	if p.ParentID == "" {
		return nil
	}
	parent, ok := s.pools[p.ParentID]
	if !ok {
		return fmt.Errorf("no parent pool %q", p.ParentID)
	}
	for id := p.ParentID; id != ""; id = s.pools[id].ParentID {
		if id == p.ID {
			return errors.New("a pool cannot be inside itself")
		}
		if s.pools[id] == nil {
			break
		}
	}
	if parent.CPULimit > 0 && (p.CPULimit == 0 || p.CPULimit > parent.CPULimit) {
		return fmt.Errorf("the vCPU limit must be set and at most the parent's %d", parent.CPULimit)
	}
	if parent.MemoryLimitMiB > 0 && (p.MemoryLimitMiB == 0 || p.MemoryLimitMiB > parent.MemoryLimitMiB) {
		return fmt.Errorf("the memory limit must be set and at most the parent's %d MiB", parent.MemoryLimitMiB)
	}
	for sp, max := range parent.StorageLimits {
		if max > 0 && (p.StorageLimits[sp] == 0 || p.StorageLimits[sp] > max) {
			return fmt.Errorf("the limit on storage pool %s must be set and at most the parent's", sp)
		}
	}
	return nil
}

// Delete removes an empty pool without children.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pools[id]
	if !ok {
		return fmt.Errorf("no resource pool %q", id)
	}
	if len(p.Machines) > 0 {
		return fmt.Errorf("%s still has %d machine(s); move them out first", p.Name, len(p.Machines))
	}
	for _, c := range s.pools {
		if c.ParentID == id {
			return fmt.Errorf("%s contains the pool %s; delete or move it first", p.Name, c.Name)
		}
	}
	delete(s.pools, id)
	return s.save()
}

// Assign moves a machine into a pool (out of any other); poolID "" takes it out of all.
// fits is called first with the destination chain, so the caller can check the limits
// (counting usage without this machine, which may already be somewhere in the tree).
func (s *Store) Assign(poolID, uuid string, fits func(chain []ResourcePool) error) error {
	uuid = strings.ToLower(uuid)
	if poolID != "" {
		chain := s.Chain(poolID)
		if len(chain) == 0 {
			return fmt.Errorf("no resource pool %q", poolID)
		}
		if fits != nil {
			if err := fits(chain); err != nil {
				return err
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if poolID != "" && s.pools[poolID] == nil {
		return fmt.Errorf("no resource pool %q", poolID)
	}
	for _, p := range s.pools {
		p.Machines = without(p.Machines, uuid)
	}
	if poolID != "" {
		s.pools[poolID].Machines = append(s.pools[poolID].Machines, uuid)
	}
	return s.save()
}

// Forget drops a deleted machine from its pool.
func (s *Store) Forget(uuid string) {
	_ = s.Assign("", uuid, nil)
}

func without(xs []string, x string) []string {
	out := xs[:0:0]
	for _, v := range xs {
		if v != x {
			out = append(out, v)
		}
	}
	return out
}

// PoolOf is the pool a machine is in ("" for none).
func (s *Store) PoolOf(uuid string) string {
	uuid = strings.ToLower(uuid)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.pools {
		for _, m := range p.Machines {
			if m == uuid {
				return p.ID
			}
		}
	}
	return ""
}

// Chain is a pool and its ancestors, the pool first.
func (s *Store) Chain(id string) []ResourcePool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.chainLocked(id)
}

func (s *Store) chainLocked(id string) []ResourcePool {
	var out []ResourcePool
	seen := map[string]bool{}
	for id != "" && !seen[id] {
		p, ok := s.pools[id]
		if !ok {
			break
		}
		seen[id] = true
		out = append(out, p.clone())
		id = p.ParentID
	}
	return out
}

// Usages adds up every pool's machines, children's included. use gives a machine's
// resources (ok=false: it no longer exists here and counts nothing).
func (s *Store) Usages(use func(uuid string) (MachineUse, bool)) map[string]Usage {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]Usage{}
	for id := range s.pools {
		out[id] = Usage{PoolID: id, StorageBytes: map[string]uint64{}}
	}
	for _, p := range s.pools {
		for _, m := range p.Machines {
			mu, ok := use(m)
			if !ok {
				continue
			}
			for _, anc := range s.chainLocked(p.ID) {
				u := out[anc.ID]
				u.Machines++
				u.VCPUs += mu.VCPUs
				u.MemoryMiB += mu.MemoryMiB
				for sp, b := range mu.StorageBytes {
					u.StorageBytes[sp] += b
				}
				out[anc.ID] = u
			}
		}
	}
	return out
}

// Check reports the first limit in chain that d would exceed, given current usage.
func Check(chain []ResourcePool, usage map[string]Usage, d Demand) error {
	for _, p := range chain {
		u := usage[p.ID]
		if p.CPULimit > 0 && d.VCPUs > 0 && u.VCPUs+d.VCPUs > p.CPULimit {
			return fmt.Errorf("resource pool %s allows %d vCPUs; its machines have %d, this needs %d more", p.Name, p.CPULimit, u.VCPUs, d.VCPUs)
		}
		if p.MemoryLimitMiB > 0 && d.MemoryMiB > 0 && u.MemoryMiB+d.MemoryMiB > p.MemoryLimitMiB {
			return fmt.Errorf("resource pool %s allows %d MiB of memory; its machines have %d MiB, this needs %d MiB more", p.Name, p.MemoryLimitMiB, u.MemoryMiB, d.MemoryMiB)
		}
		if d.StorageBytes > 0 && d.StoragePool != "" {
			if max := p.StorageLimits[d.StoragePool]; max > 0 && u.StorageBytes[d.StoragePool]+d.StorageBytes > max {
				return fmt.Errorf("resource pool %s allows %s on storage pool %s; its disks there have %s, this needs %s more",
					p.Name, gib(max), d.StoragePool, gib(u.StorageBytes[d.StoragePool]), gib(d.StorageBytes))
			}
		}
	}
	return nil
}

func gib(b uint64) string { return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30)) }
