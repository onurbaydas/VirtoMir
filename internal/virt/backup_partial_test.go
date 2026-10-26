package virt

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReserveBackupDirNeverShares(t *testing.T) {
	m := filepath.Join(t.TempDir(), "web-7c1f3e2a")
	if err := os.MkdirAll(m, 0o750); err != nil {
		t.Fatal(err)
	}
	stamp := "20261001-030000"
	p1, f1, err := reserveBackupDir(m, stamp)
	if err != nil || f1 != filepath.Join(m, stamp) || p1 != f1+partialSuffix {
		t.Fatalf("first: %s %s %v", p1, f1, err)
	}
	// Same second, first one still running: its partial name is taken.
	p2, f2, err := reserveBackupDir(m, stamp)
	if err != nil || f2 != filepath.Join(m, stamp+"-2") || p2 != f2+partialSuffix {
		t.Fatalf("second: %s %s %v", p2, f2, err)
	}
	if err := finishBackupDir(p1, f1); err != nil {
		t.Fatal(err)
	}
	// First one finished: its final name is taken, so the next gets -3.
	_, f3, err := reserveBackupDir(m, stamp)
	if err != nil || f3 != filepath.Join(m, stamp+"-3") {
		t.Fatalf("third: %s %v", f3, err)
	}
	// A failed second backup removes only its own folder.
	if err := os.RemoveAll(p2); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f1); err != nil {
		t.Fatalf("the finished backup went: %v", err)
	}
}

func TestPartialBackupsAreHiddenAndSwept(t *testing.T) {
	dir := t.TempDir()
	c := NewClient("", slog.New(slog.NewTextHandler(io.Discard, nil)))
	writeBackup(t, dir, "web-7c1f3e2a", "20261001-030000", Backup{Domain: "web", DomainUUID: "u1"})
	writeBackup(t, dir, "web-7c1f3e2a", "20261002-030000"+partialSuffix, Backup{Domain: "web", DomainUUID: "u1"})
	writeBackup(t, dir, "web-7c1f3e2a", "20261003-030000"+partialSuffix, Backup{Domain: "web", DomainUUID: "u1"})

	all, err := c.ListBackups(BackupDir(dir), "")
	if err != nil || len(all) != 1 || all[0].ID != "web-7c1f3e2a/20261001-030000" {
		t.Fatalf("list = %+v %v", all, err)
	}
	if _, err := backupPath(BackupDir(dir), "web-7c1f3e2a/20261002-030000"+partialSuffix); err == nil {
		t.Fatal("a partial backup can be addressed")
	}

	// One is stale, the other was written to just now.
	old := time.Now().Add(-2 * time.Hour)
	stale := filepath.Join(dir, "web-7c1f3e2a", "20261002-030000"+partialSuffix)
	_ = os.Chtimes(filepath.Join(stale, manifestName), old, old)
	_ = os.Chtimes(stale, old, old)
	n, err := c.SweepPartialBackups(BackupDir(dir), time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("swept %d, %v", n, err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("the stale partial backup is still there")
	}
	if _, err := os.Stat(filepath.Join(dir, "web-7c1f3e2a", "20261003-030000"+partialSuffix)); err != nil {
		t.Fatal("the active partial backup went")
	}
	if _, err := os.Stat(filepath.Join(dir, "web-7c1f3e2a", "20261001-030000")); err != nil {
		t.Fatal("the finished backup went")
	}
}
