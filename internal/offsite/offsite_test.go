package offsite

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fake struct {
	calls [][]string
	envs  [][]string
	reply func(args []string, onLine func(string)) ([]byte, error)
}

func (f *fake) exec(_ context.Context, env, args []string, onLine func(string)) ([]byte, error) {
	f.calls = append(f.calls, args)
	f.envs = append(f.envs, env)
	if f.reply != nil {
		return f.reply(args, onLine)
	}
	return nil, nil
}

func newStore(t *testing.T, f *fake) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "offsite.json"), filepath.Join(dir, "cache"), "hv-01")
	if err != nil {
		t.Fatal(err)
	}
	s.exec = f.exec
	s.look = func(string) (string, error) { return "/usr/bin/restic", nil }
	return s
}

func TestConfigSecrets(t *testing.T) {
	s := newStore(t, &fake{})
	cfg := Config{Repository: "s3:https://s3.example.com/bkt/vm", Password: "pw", Env: map[string]string{"AWS_ACCESS_KEY_ID": "AK", "AWS_SECRET_ACCESS_KEY": "SK"}, KeepLast: 3}
	if err := s.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	shown := s.Config()
	if shown.Password != Secret || shown.Env["AWS_SECRET_ACCESS_KEY"] != Secret {
		t.Fatalf("secrets shown: %+v", shown)
	}
	// Sending the placeholders back keeps the secrets; a dropped key goes.
	shown.Env = map[string]string{"AWS_ACCESS_KEY_ID": Secret, "AWS_SECRET_ACCESS_KEY": "SK2"}
	shown.KeepLast = 5
	if err := s.SetConfig(shown); err != nil {
		t.Fatal(err)
	}
	c := s.current()
	if c.Password != "pw" || c.Env["AWS_ACCESS_KEY_ID"] != "AK" || c.Env["AWS_SECRET_ACCESS_KEY"] != "SK2" || c.KeepLast != 5 {
		t.Errorf("stored %+v", c)
	}
	fi, err := os.Stat(s.path)
	if err != nil || fi.Mode().Perm() != 0o600 && os.PathSeparator == '/' {
		t.Errorf("file mode %v %v", fi, err)
	}
	for _, bad := range []Config{
		{Repository: "ftp://x"},
		{Repository: "relative/path"},
		{Env: map[string]string{"PATH": "/tmp"}},
		{Env: map[string]string{"RESTIC_PASSWORD_COMMAND": "sh -c evil"}},
		{Env: map[string]string{"LD_PRELOAD": "x"}},
		{KeepLast: -1},
	} {
		if err := s.SetConfig(bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestUploadAndRetention(t *testing.T) {
	f := &fake{}
	s := newStore(t, f)
	_ = s.SetConfig(Config{Repository: "/mnt/usb/restic", Password: "pw", KeepLast: 2, KeepWeekly: 4, LimitUploadKiB: 512})
	dir := t.TempDir()
	id := "web-1a2b3c4d/20261016-101500"
	_ = os.MkdirAll(filepath.Join(dir, filepath.FromSlash(id)), 0o755)
	_ = os.WriteFile(filepath.Join(dir, filepath.FromSlash(id), "manifest.json"), []byte("{}"), 0o644)
	f.reply = func(args []string, onLine func(string)) ([]byte, error) {
		if args[0] == "backup" {
			onLine(`{"message_type":"status","percent_done":0.5}`)
			onLine(`{"message_type":"summary","snapshot_id":"0123456789abcdef"}`)
		}
		return nil, nil
	}
	var reports []string
	snap, err := s.Upload(context.Background(), dir, id, "1a2b3c4d-0000-0000-0000-000000000000", "web, prod", func(m string) { reports = append(reports, m) })
	if err != nil || snap != "01234567" {
		t.Fatalf("%q %v", snap, err)
	}
	up := strings.Join(f.calls[0], " ")
	for _, want := range []string{"backup --json --host hv-01 --tag virtomir --tag machine:1a2b3c4d-0000-0000-0000-000000000000", "--tag backup:" + id, "--tag name:web__prod", "--limit-upload 512", filepath.Join(dir, filepath.FromSlash(id))} {
		if !strings.Contains(up, want) {
			t.Errorf("upload lacks %q: %s", want, up)
		}
	}
	if got := strings.Join(f.calls[1], " "); got != "forget --tag virtomir,machine:1a2b3c4d-0000-0000-0000-000000000000 --host hv-01 --group-by host --prune --keep-last 2 --keep-weekly 4" {
		t.Errorf("forget: %s", got)
	}
	if !strings.Contains(strings.Join(f.envs[0], " "), "RESTIC_PASSWORD=pw") || !strings.Contains(strings.Join(reports, " "), "50%") {
		t.Errorf("env %v reports %v", f.envs[0], reports)
	}
	if _, err := s.Upload(context.Background(), dir, "../etc/passwd", "1a2b3c4d-0000-0000-0000-000000000000", "x", nil); err == nil {
		t.Error("escaping backup ID accepted")
	}
}

func TestSnapshotsAndRestore(t *testing.T) {
	f := &fake{}
	s := newStore(t, f)
	_ = s.SetConfig(Config{Repository: "rest:https://backup:8000/", Password: "pw"})
	dir := t.TempDir()
	snaps := []resticSnapshot{{ID: "aaaaaaaabbbbbbbb", ShortID: "aaaaaaaa", Hostname: "hv-01", Paths: []string{"/var/lib/virtomir/backups/web-1a2b3c4d/20261016-101500"},
		Tags: []string{"virtomir", "machine:1a2b3c4d-0000-0000-0000-000000000000", "backup:web-1a2b3c4d/20261016-101500", "name:web"}}}
	f.reply = func(args []string, _ func(string)) ([]byte, error) {
		switch args[0] {
		case "snapshots":
			return json.Marshal(snaps)
		case "restore":
			target := args[len(args)-1]
			_ = os.MkdirAll(target, 0o755)
			return nil, os.WriteFile(filepath.Join(target, "manifest.json"), []byte("{}"), 0o644)
		}
		return nil, nil
	}
	list, err := s.Snapshots(context.Background())
	if err != nil || len(list) != 1 || list[0].Machine != "web" || list[0].Backup != "web-1a2b3c4d/20261016-101500" {
		t.Fatalf("%+v %v", list, err)
	}
	id, err := s.Restore(context.Background(), dir, "aaaaaaaa", nil)
	if err != nil || id != "web-1a2b3c4d/20261016-101500" {
		t.Fatalf("%q %v", id, err)
	}
	if got := strings.Join(f.calls[len(f.calls)-1], " "); got != "restore aaaaaaaabbbbbbbb:/var/lib/virtomir/backups/web-1a2b3c4d/20261016-101500 --target "+filepath.Join(dir, "web-1a2b3c4d", "20261016-101500") {
		t.Errorf("restore: %s", got)
	}
	if _, err := s.Restore(context.Background(), dir, "aaaaaaaa", nil); !errors.Is(err, ErrRefused) {
		t.Errorf("restoring over an existing backup: %v", err)
	}
	// A snapshot whose tag tries to escape the backup location.
	snaps[0].Tags[2] = "backup:../../etc"
	snaps[0].ShortID, snaps[0].ID = "cccccccc", "ccccccccdddddddd"
	if _, err := s.Restore(context.Background(), dir, "cccccccc", nil); err == nil {
		t.Error("escaping backup tag accepted")
	}
}

func TestCheck(t *testing.T) {
	f := &fake{}
	s := newStore(t, f)
	if _, err := s.Check(context.Background(), false); !errors.Is(err, ErrRefused) {
		t.Errorf("unconfigured: %v", err)
	}
	_ = s.SetConfig(Config{Repository: "/srv/restic", Password: "pw"})
	f.reply = func(args []string, _ func(string)) ([]byte, error) {
		if args[0] == "cat" {
			return nil, errors.New("restic cat: Fatal: repository does not exist: unable to open config file")
		}
		return nil, nil
	}
	if _, err := s.Check(context.Background(), false); !errors.Is(err, ErrRefused) {
		t.Errorf("missing repository: %v", err)
	}
	if msg, err := s.Check(context.Background(), true); err != nil || !strings.Contains(msg, "created") {
		t.Errorf("init: %q %v", msg, err)
	}
	if f.calls[len(f.calls)-1][0] != "init" {
		t.Errorf("calls %v", f.calls)
	}
}
