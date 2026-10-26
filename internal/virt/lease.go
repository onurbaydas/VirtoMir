package virt

import (
	"fmt"
	"strings"
	"sync"
)

// BusyError means another long operation (clone, backup, restore, delete, …) holds the
// machine right now. The API answers it with 409 Conflict.
type BusyError struct{ Op string }

func (e *BusyError) Error() string {
	return fmt.Sprintf("Another operation (%s) is running on this machine. Try again when it has finished.", e.Op)
}

// leaseSet is an in-memory registry of the machines (by UUID) and machine names (targets
// of a clone or restore) long operations work on, so two of them never run on the same
// machine at once. The zero value is ready to use.
type leaseSet struct {
	mu   sync.Mutex
	held map[string]lease
}

type lease struct {
	op string
	// keepOff: the operation needs the machine to stay shut off (an offline copy of its
	// disks), so starting it is refused meanwhile.
	keepOff bool
}

func machineKey(uuid string) string { return "uuid:" + strings.ToLower(uuid) }
func nameKey(name string) string    { return "name:" + name }

// take leases all keys for op, or none: if any is held, it returns a *BusyError naming the
// operation holding it. release is safe to call more than once.
func (s *leaseSet) take(op string, keepOff bool, keys ...string) (release func(), err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range keys {
		if h, ok := s.held[k]; ok {
			return nil, &BusyError{Op: h.op}
		}
	}
	if s.held == nil {
		s.held = map[string]lease{}
	}
	for _, k := range keys {
		s.held[k] = lease{op: op, keepOff: keepOff}
	}
	return sync.OnceFunc(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, k := range keys {
			delete(s.held, k)
		}
	}), nil
}

// mustStayOff returns the *BusyError for starting the machine while an operation that needs
// it shut off runs, or nil.
func (s *leaseSet) mustStayOff(uuid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h, ok := s.held[machineKey(uuid)]; ok && h.keepOff {
		return &BusyError{Op: h.op}
	}
	return nil
}
