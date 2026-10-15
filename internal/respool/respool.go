// Package respool implements Hierarchical Resource Pools and Storage Disk Quotas (Roadmap Phase C.12).
package respool

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ResourcePool defines a logical grouping of compute, memory, and storage limits.
type ResourcePool struct {
	ID                   string            `json:"id"`
	Name                 string            `json:"name"`
	ParentID             string            `json:"parentId,omitempty"` // empty for root pools
	Description          string            `json:"description,omitempty"`
	CPUShares            int               `json:"cpuShares"`            // relative weight (e.g. 1000 = normal)
	CPULimit             int               `json:"cpuLimit"`             // max vCPUs (0 = unlimited)
	CPUReservation       int               `json:"cpuReservation"`       // guaranteed vCPUs
	MemoryLimitMiB       uint64            `json:"memoryLimitMiB"`       // max RAM in MiB (0 = unlimited)
	MemoryReservationMiB uint64            `json:"memoryReservationMiB"` // guaranteed RAM in MiB
	StoragePoolQuotas    map[string]uint64 `json:"storagePoolQuotas"`    // storagePoolName -> maxBytes (0 = unlimited)
	IOPSLimit            int               `json:"iopsLimit"`            // total IOPS limit (0 = unlimited)
	AssignedVMs          []string          `json:"assignedVms"`          // list of VM UUIDs
	Owners               []string          `json:"owners"`               // usernames or group names
	CreatedAt            time.Time         `json:"createdAt"`
}

// PoolUsage summarizes current consumed resources by VMs inside this pool.
type PoolUsage struct {
	PoolID            string            `json:"poolId"`
	VMCount           int               `json:"vmCount"`
	UsedVCPUs         int               `json:"usedVcpus"`
	UsedMemoryMiB     uint64            `json:"usedMemoryMiB"`
	UsedStorageBytes  map[string]uint64 `json:"usedStorageBytes"`
}

type Store struct {
	path  string
	mu    sync.Mutex
	pools map[string]*ResourcePool
}

func Open(path string) (*Store, error) {
	s := &Store{
		path:  path,
		pools: make(map[string]*ResourcePool),
	}

	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return s, s.save()
	case err != nil:
		return nil, err
	}

	var list []ResourcePool
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for i := range list {
		p := list[i]
		if p.StoragePoolQuotas == nil {
			p.StoragePoolQuotas = make(map[string]uint64)
		}
		if p.AssignedVMs == nil {
			p.AssignedVMs = []string{}
		}
		if p.Owners == nil {
			p.Owners = []string{}
		}
		s.pools[p.ID] = &p
	}
	return s, nil
}

func (s *Store) save() error {
	var list []ResourcePool
	for _, p := range s.pools {
		list = append(list, *p)
	}
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

func (s *Store) List() []ResourcePool {
	s.mu.Lock()
	defer s.mu.Unlock()

	var list []ResourcePool
	for _, p := range s.pools {
		list = append(list, *p)
	}
	return list
}

func (s *Store) Get(id string) (ResourcePool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	p, ok := s.pools[id]
	if !ok {
		return ResourcePool{}, false
	}
	return *p, true
}

func (s *Store) Create(p ResourcePool) (ResourcePool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if strings.TrimSpace(p.Name) == "" {
		return ResourcePool{}, errors.New("resource pool name is required")
	}
	if p.ID == "" {
		p.ID = "pool-" + strings.ToLower(strings.ReplaceAll(p.Name, " ", "-"))
	}
	if _, exists := s.pools[p.ID]; exists {
		return ResourcePool{}, fmt.Errorf("resource pool %q already exists", p.ID)
	}
	if p.ParentID != "" {
		if _, exists := s.pools[p.ParentID]; !exists {
			return ResourcePool{}, fmt.Errorf("parent resource pool %q not found", p.ParentID)
		}
	}
	if p.CPUShares <= 0 {
		p.CPUShares = 1000
	}
	if p.StoragePoolQuotas == nil {
		p.StoragePoolQuotas = make(map[string]uint64)
	}
	if p.AssignedVMs == nil {
		p.AssignedVMs = []string{}
	}
	if p.Owners == nil {
		p.Owners = []string{}
	}
	p.CreatedAt = time.Now()

	s.pools[p.ID] = &p
	return p, s.save()
}

func (s *Store) Update(p ResourcePool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	existing, ok := s.pools[p.ID]
	if !ok {
		return fmt.Errorf("resource pool %q not found", p.ID)
	}
	if p.ParentID == p.ID {
		return errors.New("resource pool cannot be its own parent")
	}
	if p.ParentID != "" {
		if _, exists := s.pools[p.ParentID]; !exists {
			return fmt.Errorf("parent resource pool %q not found", p.ParentID)
		}
	}

	existing.Name = p.Name
	existing.ParentID = p.ParentID
	existing.Description = p.Description
	existing.CPUShares = p.CPUShares
	existing.CPULimit = p.CPULimit
	existing.CPUReservation = p.CPUReservation
	existing.MemoryLimitMiB = p.MemoryLimitMiB
	existing.MemoryReservationMiB = p.MemoryReservationMiB
	existing.StoragePoolQuotas = p.StoragePoolQuotas
	existing.IOPSLimit = p.IOPSLimit
	existing.Owners = p.Owners

	return s.save()
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	p, ok := s.pools[id]
	if !ok {
		return fmt.Errorf("resource pool %q not found", id)
	}
	if len(p.AssignedVMs) > 0 {
		return fmt.Errorf("cannot delete pool %q: it contains %d virtual machines", p.Name, len(p.AssignedVMs))
	}
	// Check for child pools
	for _, child := range s.pools {
		if child.ParentID == id {
			return fmt.Errorf("cannot delete pool %q: child pool %q depends on it", p.Name, child.Name)
		}
	}

	delete(s.pools, id)
	return s.save()
}

// AssignVM assigns a VM to a pool (unassigning it from any previous pool).
func (s *Store) AssignVM(poolID, vmUUID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	target, ok := s.pools[poolID]
	if !ok {
		return fmt.Errorf("resource pool %q not found", poolID)
	}

	// Remove from any other pool
	for _, p := range s.pools {
		var filtered []string
		for _, u := range p.AssignedVMs {
			if u != vmUUID {
				filtered = append(filtered, u)
			}
		}
		p.AssignedVMs = filtered
	}

	target.AssignedVMs = append(target.AssignedVMs, vmUUID)
	return s.save()
}

// UnassignVM removes a VM from all pools.
func (s *Store) UnassignVM(vmUUID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, p := range s.pools {
		var filtered []string
		for _, u := range p.AssignedVMs {
			if u != vmUUID {
				filtered = append(filtered, u)
			}
		}
		p.AssignedVMs = filtered
	}
	return s.save()
}

// GetPoolForVM finds the pool containing the given VM.
func (s *Store) GetPoolForVM(vmUUID string) *ResourcePool {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, p := range s.pools {
		for _, u := range p.AssignedVMs {
			if u == vmUUID {
				copyPool := *p
				return &copyPool
			}
		}
	}
	return nil
}

// CheckQuota validates if adding the specified resources exceeds pool limits.
func (s *Store) CheckQuota(poolID string, currentUsage PoolUsage, addVCPUs int, addMemMiB uint64, storagePool string, addDiskBytes uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	p, ok := s.pools[poolID]
	if !ok {
		return nil // No pool = unlimited
	}

	if p.CPULimit > 0 && currentUsage.UsedVCPUs+addVCPUs > p.CPULimit {
		return fmt.Errorf("resource pool %q vCPU limit exceeded: %d + %d > %d", p.Name, currentUsage.UsedVCPUs, addVCPUs, p.CPULimit)
	}

	if p.MemoryLimitMiB > 0 && currentUsage.UsedMemoryMiB+addMemMiB > p.MemoryLimitMiB {
		return fmt.Errorf("resource pool %q RAM limit exceeded: %d + %d MiB > %d MiB", p.Name, currentUsage.UsedMemoryMiB, addMemMiB, p.MemoryLimitMiB)
	}

	if storagePool != "" && addDiskBytes > 0 {
		if capBytes, exists := p.StoragePoolQuotas[storagePool]; exists && capBytes > 0 {
			currentBytes := currentUsage.UsedStorageBytes[storagePool]
			if currentBytes+addDiskBytes > capBytes {
				return fmt.Errorf("resource pool %q storage quota on pool %q exceeded: %d + %d > %d bytes",
					p.Name, storagePool, currentBytes, addDiskBytes, capBytes)
			}
		}
	}

	return nil
}
