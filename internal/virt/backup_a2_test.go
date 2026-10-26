package virt

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestBackupsInEarlierLocationsStayListed(t *testing.T) {
	cur, old := t.TempDir(), t.TempDir()
	c := NewClient("", slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.OldBackupDirs = func() []string { return []string{old, cur, "relative", ""} }
	base := time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)
	writeBackup(t, old, "web-7c1f3e2a", "20261001-030000", Backup{Domain: "web", DomainUUID: "u1", Created: base, Automatic: true})
	writeBackup(t, old, "web-7c1f3e2a", "20261002-030000", Backup{Domain: "web", DomainUUID: "u1", Created: base.AddDate(0, 0, 1), Automatic: true})
	writeBackup(t, cur, "web-7c1f3e2a", "20261003-030000", Backup{Domain: "web", DomainUUID: "u1", Created: base.AddDate(0, 0, 2), Automatic: true})

	if got := c.backupDirs(BackupDir(cur)); len(got) != 2 || got[0] != BackupDir(filepath.Clean(cur)) {
		t.Fatalf("dirs = %v", got)
	}
	all, err := c.ListBackups(BackupDir(cur), "u1")
	if err != nil || len(all) != 3 {
		t.Fatalf("list = %+v %v", all, err)
	}
	if all[0].Location != "" || all[1].Location != old || all[2].Location != old {
		t.Fatalf("locations = %q %q %q", all[0].Location, all[1].Location, all[2].Location)
	}
	if loc, _, err := c.locateBackup(BackupDir(cur), "web-7c1f3e2a/20261001-030000"); err != nil || string(loc) != old {
		t.Fatalf("locate = %s %v", loc, err)
	}
	// Retention counts every location: keep one, the two in the old folder go.
	n, err := c.PruneBackups(BackupDir(cur), "u1", func(ts []time.Time) []bool {
		k := make([]bool, len(ts))
		k[0] = true
		return k
	})
	if err != nil || n != 2 {
		t.Fatalf("pruned %d %v", n, err)
	}
	if left, _ := c.ListBackups(BackupDir(cur), "u1"); len(left) != 1 || left[0].Location != "" {
		t.Fatalf("left %+v", left)
	}
}

func TestMergeBackupDirs(t *testing.T) {
	a, b := filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")
	got := mergeBackupDirs(a, []string{b, a + string(filepath.Separator), b, "x"})
	if !slices.Equal(got, []string{a, b}) {
		t.Fatalf("got %v", got)
	}
}

func TestInFlightPaths(t *testing.T) {
	s := inflightSet{paths: map[string]int{}}
	p := filepath.Join(t.TempDir(), "disk.qcow2")
	done1, done2 := s.add(p), s.add(p)
	if !s.has(p) || !s.has(p+string(filepath.Separator)) {
		t.Fatal("not marked")
	}
	done1()
	done1() // twice is harmless
	if !s.has(p) {
		t.Fatal("the second writer was forgotten")
	}
	done2()
	if s.has(p) || len(s.paths) != 0 {
		t.Fatalf("still marked: %v", s.paths)
	}
	s.add("")() // nothing to mark
}

func TestBackupNeed(t *testing.T) {
	if n := backupNeed([]uint64{10 << 30, 5 << 30}, false); n != 15<<30 {
		t.Fatalf("full = %d", n)
	}
	if n := backupNeed([]uint64{10 << 30}, true); n != 1<<30 {
		t.Fatalf("incremental = %d", n)
	}
}

func TestVerifyBackupFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "vda.qcow2"), []byte("disk bytes"), 0o640); err != nil {
		t.Fatal(err)
	}
	checkErr := error(nil)
	orig := qemuCheck
	qemuCheck = func(context.Context, string) error { return checkErr }
	defer func() { qemuCheck = orig }()
	ctx := context.Background()
	quiet := func(string) {}

	b := Backup{Disks: []BackupDisk{{Target: "vda", File: "vda.qcow2"}}}
	if err := verifyBackupFiles(ctx, dir, &b, true, quiet); err != nil || b.Disks[0].SHA256 == "" || b.Verified == nil || b.CheckError != "" {
		t.Fatalf("new backup: %v %+v", err, b)
	}
	if err := checksumsMatch(ctx, dir, b, quiet); err != nil {
		t.Fatalf("unchanged: %v", err)
	}
	// The file changes: verify and the restore check both notice.
	_ = os.WriteFile(filepath.Join(dir, "vda.qcow2"), []byte("tampered!!"), 0o640)
	if err := verifyBackupFiles(ctx, dir, &b, true, quiet); err == nil || b.CheckError == "" {
		t.Fatal("a changed file passed")
	}
	if err := checksumsMatch(ctx, dir, b, quiet); err == nil {
		t.Fatal("restore check passed a changed file")
	}
	// qemu-img check failing is a failed verification too.
	checkErr = errors.New("2 errors were found on the image")
	b2 := Backup{Disks: []BackupDisk{{Target: "vda", File: "vda.qcow2"}}}
	if err := verifyBackupFiles(ctx, dir, &b2, true, quiet); err == nil || b2.CheckError == "" {
		t.Fatal("a damaged image passed")
	}
}

func TestVerifyBackupStoresTheResult(t *testing.T) {
	dir := t.TempDir()
	c := NewClient("", slog.New(slog.NewTextHandler(io.Discard, nil)))
	writeBackup(t, dir, "web-7c1f3e2a", "20261001-030000", Backup{Domain: "web", DomainUUID: "u1", Disks: []BackupDisk{{Target: "vda", File: "vda.qcow2"}}})
	_ = os.WriteFile(filepath.Join(dir, "web-7c1f3e2a", "20261001-030000", "vda.qcow2"), []byte("x"), 0o640)
	orig := qemuCheck
	qemuCheck = func(context.Context, string) error { return nil }
	defer func() { qemuCheck = orig }()
	if _, err := c.VerifyBackup(context.Background(), BackupDir(dir), "web-7c1f3e2a/20261001-030000", func(string) {}); err != nil {
		t.Fatal(err)
	}
	got, _ := c.ListBackups(BackupDir(dir), "u1")
	if len(got) != 1 || got[0].Verified == nil || got[0].Disks[0].SHA256 == "" || got[0].ID != "web-7c1f3e2a/20261001-030000" {
		t.Fatalf("manifest = %+v", got)
	}
}
