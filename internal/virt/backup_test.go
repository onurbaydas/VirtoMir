package virt

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeBackup(t *testing.T, dir, folder, stamp string, b Backup) {
	t.Helper()
	p := filepath.Join(dir, folder, stamp)
	if err := os.MkdirAll(p, 0o750); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(b)
	if err := os.WriteFile(filepath.Join(p, manifestName), raw, 0o640); err != nil {
		t.Fatal(err)
	}
}

func TestListPruneDeleteBackups(t *testing.T) {
	dir := t.TempDir()
	c := NewClient("", slog.New(slog.NewTextHandler(io.Discard, nil)))
	base := time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)
	for i := range 4 {
		stamp := base.AddDate(0, 0, i).Format("20060102-150405")
		writeBackup(t, dir, "web-7c1f3e2a", stamp, Backup{Domain: "web", DomainUUID: "u1", Created: base.AddDate(0, 0, i), Automatic: i != 0})
	}
	writeBackup(t, dir, "db-11111111", "20261001-030000", Backup{Domain: "db", DomainUUID: "u2", Created: base})

	all, err := c.ListBackups(BackupDir(dir), "")
	if err != nil || len(all) != 5 {
		t.Fatalf("list = %d %v", len(all), err)
	}
	web, _ := c.ListBackups(BackupDir(dir), "u1")
	if len(web) != 4 || !web[0].Created.After(web[3].Created) || web[0].ID != "web-7c1f3e2a/20261004-030000" {
		t.Fatalf("newest first expected: %+v", web)
	}

	// Keep 2 automatic: the oldest automatic one goes; the manual one always stays.
	n, err := c.PruneBackups(BackupDir(dir), "u1", 2)
	if err != nil || n != 1 {
		t.Fatalf("pruned %d %v", n, err)
	}
	web, _ = c.ListBackups(BackupDir(dir), "u1")
	manual := 0
	for _, b := range web {
		if !b.Automatic {
			manual++
		}
	}
	if len(web) != 3 || manual != 1 {
		t.Errorf("after prune: %+v", web)
	}

	if err := c.DeleteBackup(BackupDir(dir), "db-11111111/20261001-030000"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "db-11111111")); !os.IsNotExist(err) {
		t.Error("empty machine folder left behind")
	}
}

func TestBackupPathStaysInside(t *testing.T) {
	dir := t.TempDir()
	writeBackup(t, dir, "web-1", "s1", Backup{})
	for _, bad := range []string{"../etc", "web-1/../../x", "web-1", "/etc/passwd", "web-1/s1/../../.."} {
		if _, err := backupPath(BackupDir(dir), bad); !errors.Is(err, ErrNotFound) {
			t.Errorf("%q must be refused, got %v", bad, err)
		}
	}
	if _, err := backupPath(BackupDir(dir), "web-1/s1"); err != nil {
		t.Errorf("valid id refused: %v", err)
	}
	if _, err := (&Client{}).ListBackups("relative", ""); err == nil {
		t.Error("relative backup dir accepted")
	}
}

func TestBackupDisks(t *testing.T) {
	x := fixture(t) // vda (qcow2) + empty read-only CD-ROM
	disks := backupDisks(x)
	if len(disks) != 1 || disks[0].Target.Dev != "vda" {
		t.Errorf("disks = %+v", disks)
	}
	if got := machineFolder("web", "7c1f3e2a-55b4-4a8e-9d0c-1b2a3c4d5e6f"); got != "web-7c1f3e2a" {
		t.Errorf("folder = %s", got)
	}
	if xmlEscape(`a'b&<c>`) != "a&apos;b&amp;&lt;c&gt;" {
		t.Error("xml escape")
	}
}

func TestParseDACLabel(t *testing.T) {
	for _, tc := range []struct {
		in       string
		uid, gid int
		ok       bool
	}{
		{"+64055:+64055", 64055, 64055, true},
		{"107:992", 107, 992, true},
		{"+0:+0", 0, 0, false}, // QEMU as root needs nothing
		{"libvirt-qemu:kvm", 0, 0, false},
		{"", 0, 0, false},
	} {
		uid, gid, ok := parseDACLabel(tc.in)
		if uid != tc.uid || gid != tc.gid || ok != tc.ok {
			t.Errorf("parseDACLabel(%q) = %d, %d, %v; want %d, %d, %v", tc.in, uid, gid, ok, tc.uid, tc.gid, tc.ok)
		}
	}
}
