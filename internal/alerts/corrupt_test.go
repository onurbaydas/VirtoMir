package alerts

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
	p := filepath.Join(dir, "alerts.json")
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(p, nil)
	if s == nil || len(s.Config().Rules) != len(DefaultRules())-len(hiddenKinds) {
		t.Fatal("not the defaults")
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
