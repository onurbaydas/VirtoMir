package alerts

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestScheduledBackupFiresAndResolves(t *testing.T) {
	e, s := newEngine(t)
	const vm = "7c1f3e2a-55b4-4a8e-9d0c-1b2a3c4d5e6f"
	e.ScheduledBackup(vm, "web", errors.New("disk full"))
	if len(s.events) != 1 || s.events[0].Type != Firing || s.events[0].Alert.Kind != BackupFailed ||
		s.events[0].Alert.Message != "Scheduled backup of web failed: disk full" {
		t.Fatalf("events = %+v", s.events)
	}
	e.ScheduledBackup(vm, "web", errors.New("disk full")) // still failing: no duplicate
	// Observations never resolve it.
	e.Observe(Observation{Time: time.Now(), Connected: true})
	if len(s.events) != 1 {
		t.Fatalf("events = %+v", s.events)
	}
	e.ScheduledBackup("other", "db", nil) // another machine's success changes nothing
	e.ScheduledBackup(vm, "web", nil)
	if len(s.events) != 2 || s.events[1].Type != ResolvedEvent {
		t.Fatalf("events = %+v", s.events)
	}
	if l := e.List(); len(l) != 1 || l[0].Resolved == nil {
		t.Fatalf("list = %+v", l)
	}
}

func TestBackupRuleIsDefaultEverywhere(t *testing.T) {
	// An alerts.json written before the rule existed gets it, enabled.
	p := filepath.Join(t.TempDir(), "alerts.json")
	old := `{"rules":[{"id":"host-cpu","kind":"host.cpu","enabled":false,"threshold":80,"forSec":60,"severity":"warning"}],"channels":[]}`
	if err := os.WriteFile(p, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	e, err := Open(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(e.cfg.Rules, func(r Rule) bool { return r.Kind == BackupFailed })
	if i < 0 || !e.cfg.Rules[i].Enabled {
		t.Fatalf("rules = %+v", e.cfg.Rules)
	}
	// Shown on the Alerts page, and kept when the page saves it back.
	cfg := e.Config()
	if !slices.ContainsFunc(cfg.Rules, func(r Rule) bool { return r.Kind == BackupFailed }) {
		t.Fatal("Config hides the backup rule")
	}
	if err := e.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(e.cfg.Rules, func(r Rule) bool { return r.Kind == BackupFailed && r.Enabled }) {
		t.Fatalf("SetConfig dropped the rule: %+v", e.cfg.Rules)
	}
}
