package lcm

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreRoundTripAndWatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lcm.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ch, stop := s.Watch()
	defer stop()
	now := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	if err := s.SavePlan(Plan{ID: "p1", Started: now, State: PlanRunning, HostOrder: []string{"a", "self"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ch:
	default:
		t.Fatal("Watch did not fire on a change")
	}
	_ = s.AppendStep("p1", Step{Kind: StepPrecheck, HostID: "a", State: StateRunning})
	_ = s.AppendStep("p1", Step{Kind: StepPrecheck, HostID: "a", State: StateDone, Message: "ok"})
	_ = s.SetMaintenance("p1", "a", true)
	_ = s.SavePlan(Plan{ID: "p2", Started: now.Add(time.Hour), State: PlanPlanned})
	_ = s.SetPlanState("p1", PlanSucceeded, now.Add(time.Minute))

	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	plans := r.Plans()
	if len(plans) != 2 || plans[0].ID != "p2" {
		t.Fatalf("plans newest first = %+v", plans)
	}
	p, _ := r.Plan("p1")
	if len(p.Steps) != 1 || p.Steps[0].State != StateDone || p.State != PlanSucceeded || p.Finished.IsZero() || p.Maintenance[0] != "a" {
		t.Errorf("reloaded p1 = %+v (running step replaced by its result)", p)
	}
	raw, _ := json.Marshal(Step{Kind: StepWaitRejoin})
	if !strings.Contains(string(raw), `"kind":"wait_rejoin"`) {
		t.Errorf("step JSON %s", raw)
	}
}
