package schedule

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onurbaydas/virtomir/internal/atomicfile"
)

// An unreadable state file is moved aside, not overwritten by the next save.
func TestCorruptFileIsKept(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "schedules.json")
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(p, "/b", nil, nil, nil)
	if len(s.List("")) != 0 {
		t.Fatal("not empty")
	}
	var ce *atomicfile.CorruptError
	if !errors.As(err, &ce) || !strings.HasPrefix(ce.Backup, p+".corrupt-") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(ce.Backup); string(b) != "{not json" {
		t.Fatalf("backup holds %q", b)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("the corrupt file is still in place")
	}
}
