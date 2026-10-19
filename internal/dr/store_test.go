package dr

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func validGroup() ProtectionGroup {
	return ProtectionGroup{
		Name:                "web stack",
		SourceHost:          "host-a",
		TargetHost:          "host-b",
		TargetPool:          "tank/dr",
		VMUUIDs:             []string{"e5a1a2f1-8c37-4a0e-9d52-3d0c8a1d93f0"},
		RPOMinutes:          15,
		ReplicationInterval: 5,
		Enabled:             true,
	}
}

func validPlan(groupID string) RecoveryPlan {
	return RecoveryPlan{
		Name:              "fail over web stack",
		ProtectionGroupID: groupID,
		BootGroups: []BootGroup{
			{ID: "bg1", Name: "database", Order: 1, VMs: []PlanVM{{VMUUID: "e5a1a2f1-8c37-4a0e-9d52-3d0c8a1d93f0", SourceName: "web1"}}},
		},
	}
}

func TestStorePersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "dr.json")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open new store: %v", err)
	}
	g, err := s.CreateProtectionGroup(validGroup())
	if err != nil {
		t.Fatalf("create protection group: %v", err)
	}
	p, err := s.CreatePlan(validPlan(g.ID))
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	run := ExecutionRun{ID: "run1", PlanID: p.ID, Mode: ModeTestFailover, State: StateSucceeded, Started: time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)}
	if err := s.RecordRun(run); err != nil {
		t.Fatalf("record run: %v", err)
	}

	s2, err := Open(path) // reopen from disk
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, ok := s2.GetProtectionGroup(g.ID)
	if !ok {
		t.Fatal("protection group lost after reopen")
	}
	if got.Name != g.Name || got.TargetHost != g.TargetHost || got.Status != StatusProtected {
		t.Errorf("group mismatch: %+v", got)
	}
	if got.Created.IsZero() {
		t.Error("created time lost")
	}
	gotPlan, ok := s2.GetPlan(p.ID)
	if !ok {
		t.Fatal("plan lost after reopen")
	}
	if gotPlan.ProtectionGroupID != g.ID || len(gotPlan.BootGroups) != 1 {
		t.Errorf("plan mismatch: %+v", gotPlan)
	}
	gotRun, ok := s2.GetRun("run1")
	if !ok {
		t.Fatal("run lost after reopen")
	}
	if gotRun.PlanID != p.ID || gotRun.State != StateSucceeded {
		t.Errorf("run mismatch: %+v", gotRun)
	}
}

func TestGenerateID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dr.json")
	s, _ := Open(path)
	g, err := s.CreateProtectionGroup(validGroup()) // no ID given
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(g.ID) != 16 {
		t.Errorf("generated ID %q is not 16 characters", g.ID)
	}
	_, err = s.CreateProtectionGroup(func() ProtectionGroup { gg := g; gg.ID = g.ID; gg.Name = "other"; return gg }())
	if !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate ID conflict: %v", err)
	}
}

func TestProtectionGroupValidation(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "dr.json"))

	if _, err := s.CreateProtectionGroup(func() ProtectionGroup { g := validGroup(); g.Name = ""; return g }()); !errors.Is(err, ErrInvalid) {
		t.Errorf("missing name: %v", err)
	}
	if _, err := s.CreateProtectionGroup(func() ProtectionGroup { g := validGroup(); g.VMUUIDs = nil; return g }()); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty VM list: %v", err)
	}
	if _, err := s.CreateProtectionGroup(func() ProtectionGroup { g := validGroup(); g.RPOMinutes = -1; return g }()); !errors.Is(err, ErrInvalid) {
		t.Errorf("invalid RPO minutes: %v", err)
	}
	if _, err := s.CreateProtectionGroup(func() ProtectionGroup { g := validGroup(); g.TargetHost = ""; return g }()); !errors.Is(err, ErrInvalid) {
		t.Errorf("missing target host: %v", err)
	}
	if _, err := s.CreateProtectionGroup(func() ProtectionGroup { g := validGroup(); g.TargetPool = ""; return g }()); !errors.Is(err, ErrInvalid) {
		t.Errorf("missing target pool: %v", err)
	}

	// Zero values get the documented defaults.
	g, err := s.CreateProtectionGroup(func() ProtectionGroup { g := validGroup(); g.RPOMinutes = 0; g.ReplicationInterval = 0; return g }())
	if err != nil {
		t.Fatalf("create with defaults: %v", err)
	}
	if g.RPOMinutes != 60 || g.ReplicationInterval != 15 {
		t.Errorf("defaults not applied: rpo %d, interval %d", g.RPOMinutes, g.ReplicationInterval)
	}
}

func TestPlanValidation(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "dr.json"))
	g, err := s.CreateProtectionGroup(validGroup())
	if err != nil {
		t.Fatalf("create group: %v", err)
	}

	dupOrders := validPlan(g.ID)
	dupOrders.BootGroups = append(dupOrders.BootGroups, BootGroup{ID: "bg2", Name: "app", Order: 1, VMs: []PlanVM{{VMUUID: g.VMUUIDs[0], SourceName: "web2"}}})
	if _, err := s.CreatePlan(dupOrders); !errors.Is(err, ErrInvalid) {
		t.Errorf("duplicate boot group orders: %v", err)
	}

	noVMs := validPlan(g.ID)
	noVMs.BootGroups[0].VMs = nil
	if _, err := s.CreatePlan(noVMs); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty boot group: %v", err)
	}

	if _, err := s.CreatePlan(validPlan("missing")); !errors.Is(err, ErrInvalid) {
		t.Errorf("unknown protection group: %v", err)
	}

	if _, err := s.CreatePlan(func() RecoveryPlan { p := validPlan(g.ID); p.Name = ""; return p }()); !errors.Is(err, ErrInvalid) {
		t.Errorf("missing name: %v", err)
	}
	if _, err := s.CreatePlan(func() RecoveryPlan { p := validPlan(g.ID); p.BootGroups = nil; return p }()); !errors.Is(err, ErrInvalid) {
		t.Errorf("no boot groups: %v", err)
	}
}

func TestCreatePlanConflict(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "dr.json"))
	g, err := s.CreateProtectionGroup(validGroup())
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	p, err := s.CreatePlan(validPlan(g.ID))
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	dup := validPlan(g.ID)
	dup.ID = p.ID
	dup.Name = "another"
	if _, err := s.CreatePlan(dup); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate plan ID: %v", err)
	}
	if got, _ := s.GetPlan(p.ID); got.Name != p.Name {
		t.Errorf("existing plan was overwritten: %+v", got)
	}
}

func TestUpdateProtectionGroupKeepsStatus(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "dr.json"))
	g, err := s.CreateProtectionGroup(validGroup())
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := s.setStatus(g.ID, StatusRPOBreached); err != nil {
		t.Fatalf("set status: %v", err)
	}
	upd := g
	upd.Name = "renamed"
	upd.Status = "" // a caller must not be able to clear the RPO status
	if err := s.UpdateProtectionGroup(upd); err != nil {
		t.Fatalf("update group: %v", err)
	}
	got, _ := s.GetProtectionGroup(g.ID)
	if got.Status != StatusRPOBreached {
		t.Errorf("update reset status to %q", got.Status)
	}
	if got.Name != "renamed" {
		t.Errorf("update did not apply the name: %q", got.Name)
	}
	if !got.Created.Equal(g.Created) {
		t.Errorf("created time changed: %v -> %v", g.Created, got.Created)
	}
}

func TestRecordRunRollbackOnSaveFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dr.json")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Occupy the store path with a directory so the atomic rename fails.
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("occupy path: %v", err)
	}
	run := ExecutionRun{ID: "run1", PlanID: "p", Mode: ModeTestFailover, State: StateRunning, Started: time.Now().UTC()}
	if err := s.RecordRun(run); err == nil {
		t.Fatal("expected the save to fail")
	}
	if got := s.ListRuns(""); len(got) != 0 {
		t.Errorf("failed save left the run in memory: %+v", got)
	}
}

func TestDeleteGroupReferencedByPlanIsBusy(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "dr.json"))
	g, err := s.CreateProtectionGroup(validGroup())
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	if _, err := s.CreatePlan(validPlan(g.ID)); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if err := s.DeleteProtectionGroup(g.ID); !errors.Is(err, ErrBusy) {
		t.Errorf("delete referenced group: %v", err)
	}
	// After deleting the plan, the group can go.
	plans := s.ListPlans(g.ID)
	if err := s.DeletePlan(plans[0].ID); err != nil {
		t.Fatalf("delete plan: %v", err)
	}
	if err := s.DeleteProtectionGroup(g.ID); err != nil {
		t.Errorf("delete group after plan removal: %v", err)
	}
	if err := s.DeleteProtectionGroup(g.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete missing group: %v", err)
	}
}

func TestListPlansAndRuns(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "dr.json"))
	g, _ := s.CreateProtectionGroup(validGroup())
	g2, _ := s.CreateProtectionGroup(func() ProtectionGroup { gg := validGroup(); gg.Name = "other"; gg.TargetPool = "tank/dr2"; return gg }())
	a, _ := s.CreatePlan(func() RecoveryPlan { p := validPlan(g.ID); p.Name = "beta"; return p }())
	b, _ := s.CreatePlan(func() RecoveryPlan { p := validPlan(g.ID); p.Name = "alpha"; return p }())
	_, _ = s.CreatePlan(func() RecoveryPlan { p := validPlan(g2.ID); p.Name = "other"; return p }())

	plans := s.ListPlans(g.ID)
	if len(plans) != 2 || plans[0].Name != "alpha" || plans[1].Name != "beta" {
		t.Fatalf("plans not filtered/sorted: %+v", plans)
	}
	all := s.ListPlans("")
	if len(all) != 3 {
		t.Fatalf("expected 3 plans total, got %d", len(all))
	}

	update := a
	update.Description = "checked monthly"
	if err := s.UpdatePlan(update); err != nil {
		t.Fatalf("update plan: %v", err)
	}
	if got, _ := s.GetPlan(a.ID); got.Description != "checked monthly" {
		t.Errorf("update not applied: %+v", got)
	}
	if err := s.UpdatePlan(func() RecoveryPlan { p := a; p.ProtectionGroupID = "missing"; return p }()); !errors.Is(err, ErrInvalid) {
		t.Errorf("update to unknown group: %v", err)
	}
	if err := s.UpdatePlan(func() RecoveryPlan { p := a; p.ID = "missing"; return p }()); !errors.Is(err, ErrNotFound) {
		t.Errorf("update missing plan: %v", err)
	}

	start := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	for i, st := range []ExecutionState{StateRunning, StateSucceeded, StateFailed} {
		run := ExecutionRun{ID: string(rune('a' + i)), PlanID: b.ID, Mode: ModeRealFailover, State: st, Started: start.Add(time.Duration(i) * time.Minute)}
		if err := s.RecordRun(run); err != nil {
			t.Fatalf("record run: %v", err)
		}
	}
	runs := s.ListRuns(b.ID)
	if len(runs) != 3 || runs[0].ID != "c" || runs[2].ID != "a" {
		t.Errorf("runs not newest first: %+v", runs)
	}
	if got := s.ListRuns(a.ID); len(got) != 0 {
		t.Errorf("runs leaked into other plan: %+v", got)
	}
	if got := s.ListRuns(""); len(got) != 3 {
		t.Errorf("unfiltered runs: %d", len(got))
	}
}

func TestRunHistoryCapped(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "dr.json"))
	g, _ := s.CreateProtectionGroup(validGroup())
	p, _ := s.CreatePlan(validPlan(g.ID))
	start := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	for i := 0; i < maxRuns+10; i++ {
		run := ExecutionRun{ID: string(rune('a'+i%26)) + string(rune('a'+i/26)), PlanID: p.ID, Mode: ModeTestFailover, State: StateSucceeded, Started: start.Add(time.Duration(i) * time.Minute)}
		if err := s.RecordRun(run); err != nil {
			t.Fatalf("record run %d: %v", i, err)
		}
	}
	if got := len(s.ListRuns("")); got != maxRuns {
		t.Errorf("run history is %d, want %d", got, maxRuns)
	}
	// The oldest runs are gone.
	if _, ok := s.GetRun("aa"); ok {
		t.Error("oldest run was kept")
	}
	if _, ok := s.GetRun("bc"); !ok {
		t.Error("newest run missing")
	}
}

func TestOpenMissingFileIsEmpty(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "no-such-dir", "dr.json"))
	if err != nil {
		t.Fatalf("open missing file: %v", err)
	}
	if len(s.ListProtectionGroups()) != 0 || len(s.ListPlans("")) != 0 {
		t.Error("missing file should yield an empty store")
	}
}

func TestOpenCorruptFileFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dr.json")
	if err := os.WriteFile(path, []byte("], not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Error("corrupt file should fail to load")
	}
}
