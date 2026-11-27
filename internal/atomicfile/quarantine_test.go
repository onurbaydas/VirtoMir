package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestQuarantineMovesAsideWithFreeName(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.json")
	cause := errors.New("bad json")
	now := time.Unix(1700000000, 0)

	os.WriteFile(p, []byte("first"), 0o600)
	err := quarantineAt(p, cause, now)
	var ce *CorruptError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want a *CorruptError", err)
	}
	want := p + ".corrupt-" + strconv.FormatInt(now.Unix(), 10)
	if ce.Path != p || ce.Backup != want {
		t.Errorf("CorruptError = %+v, want backup %s", ce, want)
	}
	if !errors.Is(err, cause) {
		t.Error("errors.Is does not reach the cause through Unwrap")
	}
	if !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "bad json") {
		t.Errorf("message %q names neither the backup nor the cause", err)
	}
	if raw, _ := os.ReadFile(want); string(raw) != "first" {
		t.Errorf("backup holds %q", raw)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("the unreadable file is still in place")
	}

	// The same second again: the first backup is kept, the second gets a suffix.
	os.WriteFile(p, []byte("second"), 0o600)
	err = quarantineAt(p, cause, now)
	if !errors.As(err, &ce) || ce.Backup != want+"-2" {
		t.Fatalf("second quarantine = %v", err)
	}
	if raw, _ := os.ReadFile(want); string(raw) != "first" {
		t.Errorf("first backup overwritten: %q", raw)
	}
}

func TestQuarantineCannotMove(t *testing.T) {
	// A file that is gone by the time it is moved cannot be renamed, as root too.
	p := filepath.Join(t.TempDir(), "x.json")
	cause := errors.New("bad json")
	err := Quarantine(p, cause)
	var ce *CorruptError
	if !errors.As(err, &ce) || ce.Backup != "" {
		t.Fatalf("err = %v, want a CorruptError without backup", err)
	}
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "could not be moved aside") {
		t.Errorf("message %q", err)
	}
}
