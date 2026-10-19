package virt

import (
	"encoding/xml"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPickParent(t *testing.T) {
	disks := []BackupDisk{{Target: "vda"}}
	full := Backup{ID: "web-1/3", Checkpoint: "virtomir-3", Disks: disks}
	inc := Backup{ID: "web-1/4", Checkpoint: "virtomir-4", Parent: "web-1/3", Incremental: true, Disks: disks}
	offline := Backup{ID: "web-1/5", Disks: disks} // stopped machine: no checkpoint
	all := []Backup{offline, inc, full}            // newest first
	exists := func(string) bool { return true }

	if p, ok := pickParent(all, []string{"virtomir-4"}, disks, 7, exists); !ok || p.ID != "web-1/4" {
		t.Errorf("parent = %v %v, want the newest backup whose checkpoint still exists", p.ID, ok)
	}
	if _, ok := pickParent(all, []string{"virtomir-4"}, disks, 2, exists); ok {
		t.Error("full every 2: web-1/4 is already the second of its chain, the next must be full")
	}
	if _, ok := pickParent(all, []string{"virtomir-4"}, disks, 0, exists); ok {
		t.Error("full every 0 means always full")
	}
	if _, ok := pickParent(all, []string{"other"}, disks, 7, exists); ok {
		t.Error("checkpoint gone from the machine: full")
	}
	if _, ok := pickParent(all, []string{"virtomir-4"}, []BackupDisk{{Target: "vda"}, {Target: "vdb"}}, 7, exists); ok {
		t.Error("a disk was added since: full")
	}
	if _, ok := pickParent(all, []string{"virtomir-4"}, disks, 7, func(id string) bool { return id != "web-1/3" }); ok {
		t.Error("an ancestor's files are gone: full")
	}
}

func TestIncrementalBackupXML(t *testing.T) {
	raw := backupXMLFrom("/b/web", []BackupDisk{{Target: "vda", File: "vda.qcow2"}}, "virtomir-3")
	var x struct {
		Incremental string `xml:"incremental"`
		Disks       []struct {
			Name string `xml:"name,attr"`
		} `xml:"disks>disk"`
	}
	if err := xml.Unmarshal([]byte(raw), &x); err != nil || x.Incremental != "virtomir-3" || len(x.Disks) != 1 {
		t.Errorf("parsed %+v %v from\n%s", x, err, raw)
	}
	var cp struct {
		Name  string `xml:"name"`
		Disks []struct {
			Name       string `xml:"name,attr"`
			Checkpoint string `xml:"checkpoint,attr"`
		} `xml:"disks>disk"`
	}
	if err := xml.Unmarshal([]byte(checkpointXML("virtomir-4", []BackupDisk{{Target: "vda"}})), &cp); err != nil ||
		cp.Name != "virtomir-4" || len(cp.Disks) != 1 || cp.Disks[0].Checkpoint != "bitmap" {
		t.Errorf("checkpoint = %+v %v", cp, err)
	}
}

func TestBackupChainsSurviveDeleteAndPrune(t *testing.T) {
	dir := t.TempDir()
	c := NewClient("", slog.New(slog.NewTextHandler(io.Discard, nil)))
	base := time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)
	stamp := func(i int) string { return base.AddDate(0, 0, i).Format("20060102-150405") }
	id := func(i int) string { return "web-1/" + stamp(i) }
	// 0 full, 1 and 2 incremental on it, 3 full, 4 incremental on 3 (all automatic).
	parents := map[int]int{1: 0, 2: 1, 4: 3}
	for i := range 5 {
		b := Backup{Domain: "web", DomainUUID: "u1", Created: base.AddDate(0, 0, i), Automatic: true}
		if p, ok := parents[i]; ok {
			b.Incremental, b.Parent = true, id(p)
		}
		writeBackup(t, dir, "web-1", stamp(i), b)
	}
	if err := c.DeleteBackup(BackupDir(dir), id(1)); err == nil {
		t.Fatal("deleting a backup an incremental one depends on must be refused")
	}
	// Keep only the newest: its whole chain (3) stays, the old chain (0-2) goes.
	keepOne := func(ts []time.Time) []bool { k := make([]bool, len(ts)); k[0] = true; return k }
	n, err := c.PruneBackups(BackupDir(dir), "u1", keepOne)
	if err != nil || n != 3 {
		t.Fatalf("pruned %d %v", n, err)
	}
	for i, want := range []bool{false, false, false, true, true} {
		_, err := os.Stat(filepath.Join(dir, "web-1", stamp(i), manifestName))
		if got := err == nil; got != want {
			t.Errorf("backup %d present = %v, want %v", i, got, want)
		}
	}
	if err := c.DeleteBackup(BackupDir(dir), id(4)); err != nil {
		t.Errorf("the newest of a chain can go: %v", err)
	}
}

func TestChainInside(t *testing.T) {
	folder := filepath.Join("/b", "web-1")
	ok := []imageInfo{{Filename: filepath.Join(folder, "s2", "vda.qcow2")}, {Filename: filepath.Join(folder, "s1", "vda.qcow2")}}
	if err := chainInside(folder, ok); err != nil {
		t.Errorf("chain inside the machine folder refused: %v", err)
	}
	bad := append(ok, imageInfo{Filename: "/etc/shadow"})
	var refused *OperationError
	if err := chainInside(folder, bad); !errors.As(err, &refused) {
		t.Errorf("chain leaving the folder = %v", err)
	}
}
