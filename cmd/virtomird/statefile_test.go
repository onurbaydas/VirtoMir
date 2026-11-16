package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/onurbaydas/virtomir/internal/atomicfile"
	"github.com/onurbaydas/virtomir/internal/ha"
	"github.com/onurbaydas/virtomir/internal/lcm"
	"github.com/onurbaydas/virtomir/internal/offsite"
	"github.com/onurbaydas/virtomir/internal/placement"
	"github.com/onurbaydas/virtomir/internal/respool"
	"github.com/onurbaydas/virtomir/internal/vmware"
	"github.com/onurbaydas/virtomir/internal/zfsrep"
)

// A state file that cannot be read (torn by a power loss, emptied by a full disk) is moved
// aside and its store starts empty: the feature stays usable and says what happened, and
// the next save does not write over what may still be recovered (passwords among it).
func TestUnreadableStateFilesAreKeptAndTheStoreStarts(t *testing.T) {
	stores := map[string]func(path string) (any, error){
		"ha.json":              func(p string) (any, error) { s, err := ha.Open(p); return nilIfNil(s), err },
		"respools.json":        func(p string) (any, error) { s, err := respool.Open(p); return nilIfNil(s), err },
		"placement.json":       func(p string) (any, error) { s, err := placement.Open(p); return nilIfNil(s), err },
		"lcm.json":             func(p string) (any, error) { s, err := lcm.Open(p); return nilIfNil(s), err },
		"zfs-replication.json": func(p string) (any, error) { s, err := zfsrep.Open(p, zfsrep.Exec{}, nil); return nilIfNil(s), err },
		"vmware.json":          func(p string) (any, error) { s, err := vmware.Open(p); return nilIfNil(s), err },
		"offsite.json": func(p string) (any, error) {
			s, err := offsite.Open(p, filepath.Join(filepath.Dir(p), "cache"), "host")
			return nilIfNil(s), err
		},
	}
	for name, open := range stores {
		for what, content := range map[string]string{"empty": "", "torn": `{"enabled": tr`, "not json": "\x00\x00\x00"} {
			t.Run(name+"/"+what, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), name)
				if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
				s, err := open(path)
				var ce *atomicfile.CorruptError
				if s == nil || !errors.As(err, &ce) {
					t.Fatalf("store = %v, err = %v", s, err)
				}
				if b, rerr := os.ReadFile(ce.Backup); rerr != nil || string(b) != content {
					t.Errorf("the unreadable file was not kept: %q %v", b, rerr)
				}
				if _, serr := os.Stat(path); !os.IsNotExist(serr) {
					t.Errorf("the unreadable file is still in place, to be read again or written over: %v", serr)
				}
			})
		}
	}
}

// nilIfNil turns a nil *Store into a nil interface.
func nilIfNil[T any](s *T) any {
	if s == nil {
		return nil
	}
	return s
}
