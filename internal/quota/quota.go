// Package quota limits what operators may create themselves (ADR-0022): a rule per Linux
// group or user caps the number of machines, vCPUs, memory and disk space (ADR-0044) of the
// machines they own.
package quota

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Rule limits the machines owned by one user (Kind "user") or by each member of a group
// (Kind "group"). Zero means "none of this" — a rule always caps every dimension.
type Rule struct {
	Kind      string `json:"kind"` // user or group
	Name      string `json:"name"`
	Machines  int    `json:"machines"`
	VCPUs     int    `json:"vcpus"`
	MemoryMiB uint64 `json:"memoryMiB"`
	// DiskGiB caps the size of the owned machines' own disks (their full virtual size, as
	// a thin disk can fill up). nil: no disk limit — rules from before ADR-0044 stay valid.
	DiskGiB *uint64 `json:"diskGiB,omitempty"`
}

// Usage is what a user's machines take now.
type Usage struct {
	Machines  int    `json:"machines"`
	VCPUs     int    `json:"vcpus"`
	MemoryMiB uint64 `json:"memoryMiB"`
	DiskGiB   uint64 `json:"diskGiB"`
}

type Store struct {
	path string
	mu   sync.Mutex
	r    []Rule
}

func Open(path string) (*Store, error) {
	s := &Store{path: path, r: []Rule{}}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, err
	}
	if err := json.Unmarshal(b, &s.r); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

func (s *Store) Rules() []Rule {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Rule{}, s.r...)
}

// Set replaces all rules (validated).
func (s *Store) Set(rules []Rule) error {
	seen := map[string]bool{}
	for _, r := range rules {
		if r.Kind != "user" && r.Kind != "group" {
			return fmt.Errorf("a rule is for a user or a group, not %q", r.Kind)
		}
		if strings.TrimSpace(r.Name) == "" {
			return errors.New("every rule needs the user or group it is for")
		}
		if r.Machines < 0 || r.VCPUs < 0 {
			return errors.New("limits cannot be negative")
		}
		k := r.Kind + ":" + r.Name
		if seen[k] {
			return fmt.Errorf("there are two rules for %s %s", r.Kind, r.Name)
		}
		seen[k] = true
	}
	b, err := json.MarshalIndent(rules, "", "  ")
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
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	s.mu.Lock()
	s.r = append([]Rule{}, rules...)
	s.mu.Unlock()
	return nil
}

// For returns the rule that applies to user: their own rule, else the most generous rule
// of any of their groups. ok is false when none applies (they cannot create machines).
func (s *Store) For(user string, groups []string) (Rule, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best Rule
	found := false
	for _, r := range s.r {
		if r.Kind == "user" && r.Name == user {
			return r, true
		}
	}
	for _, r := range s.r {
		if r.Kind != "group" {
			continue
		}
		for _, g := range groups {
			if g == r.Name && (!found || r.Machines > best.Machines || (r.Machines == best.Machines && r.MemoryMiB > best.MemoryMiB)) {
				best, found = r, true
			}
		}
	}
	return best, found
}

// Check says whether adding (machines, vcpus, memory, disk) to used stays within r; the
// error names what would be exceeded.
func Check(r Rule, used Usage, machines, vcpus int, memoryMiB, diskGiB uint64) error {
	var over []string
	if used.Machines+machines > r.Machines {
		over = append(over, fmt.Sprintf("machines (%d of %d used)", used.Machines, r.Machines))
	}
	if used.VCPUs+vcpus > r.VCPUs {
		over = append(over, fmt.Sprintf("vCPUs (%d of %d used, this needs %d)", used.VCPUs, r.VCPUs, vcpus))
	}
	if used.MemoryMiB+memoryMiB > r.MemoryMiB {
		over = append(over, fmt.Sprintf("memory (%d of %d MiB used, this needs %d)", used.MemoryMiB, r.MemoryMiB, memoryMiB))
	}
	if r.DiskGiB != nil && used.DiskGiB+diskGiB > *r.DiskGiB {
		over = append(over, fmt.Sprintf("disk space (%d of %d GiB used, this needs %d)", used.DiskGiB, *r.DiskGiB, diskGiB))
	}
	if len(over) > 0 {
		return fmt.Errorf("this would exceed your quota for %s; ask an admin for more", strings.Join(over, ", "))
	}
	return nil
}
