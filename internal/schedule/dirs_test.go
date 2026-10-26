package schedule

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestRememberDir(t *testing.T) {
	cases := []struct {
		old        []string
		prev, next string
		want       []string
	}{
		{nil, "/a", "/b", []string{"/a"}},
		{[]string{"/a"}, "/b", "/c", []string{"/b", "/a"}},
		{[]string{"/b", "/a"}, "/c", "/a", []string{"/c", "/b"}}, // back to an old one: no longer "old"
		{[]string{"/a"}, "/a/", "/b", []string{"/a"}},             // cleaned, no duplicate
		{nil, "/a", "/a", nil},
	}
	for _, c := range cases {
		got := rememberDir(c.old, filepath.FromSlash(c.prev), filepath.FromSlash(c.next))
		var want []string
		for _, w := range c.want {
			want = append(want, filepath.Clean(filepath.FromSlash(w)))
		}
		if !slices.Equal(got, want) {
			t.Errorf("rememberDir(%v, %s, %s) = %v, want %v", c.old, c.prev, c.next, got, want)
		}
	}
}

func TestSetSettingsKeepsOldLocationsAndTestsWriting(t *testing.T) {
	root := t.TempDir()
	first, second := filepath.Join(root, "one"), filepath.Join(root, "two")
	s, _ := Open(filepath.Join(root, "s.json"), first, &fakeRunner{snapshots: map[string][]string{}}, syncStart, nil)
	// The API sends only backupDir; whatever it says about old ones is ignored.
	if err := s.SetSettings(Settings{BackupDir: second, OldBackupDirs: []string{"/bogus"}}); err != nil {
		t.Fatal(err)
	}
	st := s.Settings()
	if st.BackupDir != second || !slices.Equal(st.OldBackupDirs, []string{first}) {
		t.Fatalf("settings = %+v", st)
	}
	if fi, err := os.Stat(second); err != nil || !fi.IsDir() {
		t.Fatalf("the new location was not created: %v", err)
	}
	if entries, _ := os.ReadDir(second); len(entries) != 0 {
		t.Fatalf("the write test left %v behind", entries)
	}
	// A location under a file cannot be created: refused, settings unchanged.
	blocker := filepath.Join(root, "file")
	_ = os.WriteFile(blocker, []byte("x"), 0o600)
	if err := s.SetSettings(Settings{BackupDir: filepath.Join(blocker, "sub")}); err == nil {
		t.Fatal("an unusable location was accepted")
	}
	if s.Settings().BackupDir != second {
		t.Fatal("a refused location replaced the old one")
	}
}

func TestDeleteForDomain(t *testing.T) {
	s, _ := Open("", "/b", &fakeRunner{snapshots: map[string][]string{}}, syncStart, nil)
	for _, d := range []string{"u1", "u1", "u2"} {
		if _, err := s.Put(Schedule{Kind: Backups, Domain: d, Every: "daily", At: "03:00", Keep: 3}); err != nil {
			t.Fatal(err)
		}
	}
	if n := s.DeleteForDomain("u1"); n != 2 {
		t.Fatalf("removed %d, want 2", n)
	}
	if l := s.List(""); len(l) != 1 || l[0].Domain != "u2" {
		t.Fatalf("left %+v", l)
	}
}

func TestScheduleOfDeletedMachineSaysSo(t *testing.T) {
	r := &fakeRunner{snapshots: map[string][]string{}, gone: map[string]bool{"u1": true}}
	s, _ := Open("", "/b", r, syncStart, nil)
	sc, _ := s.Put(Schedule{Kind: Backups, Domain: "u1", Enabled: true, Every: "daily", At: "03:00", Keep: 3})
	if err := s.RunNow(sc.ID); err != nil {
		t.Fatal(err)
	}
	got := s.List("u1")[0]
	if got.LastError != ErrMachineGone.Error() || r.backups != 0 {
		t.Fatalf("lastError %q, backups %d", got.LastError, r.backups)
	}
}
