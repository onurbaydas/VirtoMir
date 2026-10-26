package virt

import (
	"path/filepath"
	"sync"
)

// Files that a running operation is writing right now (a restore, an upload or URL
// download, a clone's copy). They belong to no machine yet, so without this they would look
// unused: the volume list marks them, and deleting one is refused until it is finished.
var inFlight = inflightSet{paths: map[string]int{}}

type inflightSet struct {
	mu    sync.Mutex
	paths map[string]int
}

// add registers path as being written; the returned func ends that (call it once).
func (s *inflightSet) add(path string) func() {
	if path == "" {
		return func() {}
	}
	path = filepath.Clean(path)
	s.mu.Lock()
	s.paths[path]++
	s.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.paths[path]--; s.paths[path] <= 0 {
				delete(s.paths, path)
			}
		})
	}
}

// has reports whether path is being written.
func (s *inflightSet) has(path string) bool {
	if path == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.paths[filepath.Clean(path)] > 0
}
