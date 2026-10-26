package atomicfile

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteReplacesAndLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.json")
	if err := Write(p, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Write(p, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "two" {
		t.Fatalf("content %q", b)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("left behind: %v", entries)
	}
}

func TestWriteFuncFailureKeepsOldFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.json")
	_ = Write(p, []byte("old"), 0o600)
	err := WriteFunc(p, 0o600, func(w io.Writer) error {
		_, _ = w.Write([]byte("half"))
		return errors.New("boom")
	})
	if err == nil {
		t.Fatal("expected the error")
	}
	if b, _ := os.ReadFile(p); string(b) != "old" {
		t.Fatalf("content %q", b)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temp file left behind: %v", entries)
	}
}

func TestQuarantine(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.json")
	now := time.Unix(1700000000, 0)
	for i, want := range []string{p + ".corrupt-1700000000", p + ".corrupt-1700000000-2"} {
		_ = os.WriteFile(p, []byte("{broken"), 0o600)
		err := quarantineAt(p, errors.New("bad json"), now)
		var ce *CorruptError
		if !errors.As(err, &ce) || ce.Backup != want || ce.Path != p {
			t.Fatalf("%d: %#v", i, err)
		}
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%d: original still there", i)
		}
		if b, _ := os.ReadFile(want); string(b) != "{broken" {
			t.Fatalf("%d: backup content %q", i, b)
		}
	}
}
