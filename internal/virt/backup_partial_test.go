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

// A backup gets its final name only once every file of it could be flushed to disk: a file
// that cannot be opened keeps it partial, so it never counts as the newest backup.
func TestFinishBackupDirFlushesFirst(t *testing.T) {
	m := t.TempDir()
	partial, final := filepath.Join(m, "20261001-030000"+partialSuffix), filepath.Join(m, "20261001-030000")
	if err := os.MkdirAll(filepath.Join(partial, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"vda.qcow2", "domain.xml", manifestName, "sub/extra"} {
		if err := os.WriteFile(filepath.Join(partial, f), []byte("x"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	// A link to nowhere stands for a file that cannot be read back.
	if err := os.Symlink(filepath.Join(m, "nowhere"), filepath.Join(partial, "dangling")); err != nil {
		t.Fatal(err)
	}
	if err := syncTree(partial); err != nil {
		t.Fatalf("links are not files to flush: %v", err)
	}
	if err := syncTree(filepath.Join(m, "missing")); err == nil {
		t.Error("a folder that is not there was flushed")
	}
	if err := finishBackupDir(filepath.Join(m, "missing"+partialSuffix), filepath.Join(m, "missing")); err == nil {
		t.Error("a backup that is not there was finished")
	}
	if err := finishBackupDir(partial, final); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(final, "sub", "extra")); err != nil {
		t.Errorf("the finished backup: %v", err)
	}
}

// The manifest is replaced whole: no temporary file stays, and a reader never sees half.
func TestWriteManifestIsAtomic(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"web", "web-renamed"} {
		if err := writeManifest(dir, Backup{Domain: name}); err != nil {
			t.Fatal(err)
		}
		if b, err := readManifest(filepath.Join(dir, manifestName)); err != nil || b.Domain != name {
			t.Errorf("manifest: %+v %v", b, err)
		}
	}
	if es, _ := os.ReadDir(dir); len(es) != 1 {
		t.Errorf("left in the folder: %v", es)
	}
}
