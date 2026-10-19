package lcm

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCluster is three (or more) hosts in memory: machines, free memory, versions and jobs
// that finish after a few polls.
type fakeCluster struct {
	mu       sync.Mutex
	hosts    []Member
	vms      map[string][]VM // host -> machines
	free     map[string]int64
	jobs     map[string]*fakeJob
	running  map[string]int // background jobs per host
	failMove string         // a machine whose move fails
	stuck    string         // a host that never comes back upgraded
	ha       bool
	votes    int
	witness  bool
	calls    []string
	gate     chan struct{} // when set, jobs wait for it
	nextID   int
}

type fakeJob struct {
	host, kind string
	polls      int
	fail       bool
	onDone     func()
}

func newFake(n int) *fakeCluster {
	f := &fakeCluster{vms: map[string][]VM{}, free: map[string]int64{}, jobs: map[string]*fakeJob{}, running: map[string]int{}, votes: n}
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("h%d", i)
		f.hosts = append(f.hosts, Member{ID: id, Name: "host" + fmt.Sprint(i), Version: "0.9.0"})
		f.free[id] = 32 << 20
		f.vms[id] = []VM{{UUID: id + "-vm", Name: id + "-vm", MemKiB: 4 << 20, Running: true}}
	}
	return f
}

func (f *fakeCluster) host(id string) *Member {
	for i := range f.hosts {
		if f.hosts[i].ID == id {
			return &f.hosts[i]
		}
	}
	return nil
}

func (f *fakeCluster) log(s string) {
	f.calls = append(f.calls, s)
}

func (f *fakeCluster) Members(context.Context) ([]Member, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.hosts), nil
}
func (f *fakeCluster) Hello(_ context.Context, id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.host(id).Version, nil
}
func (f *fakeCluster) RunningVMs(_ context.Context, id string) ([]VM, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.vms[id]), nil
}
func (f *fakeCluster) FreeMemory(_ context.Context, id string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.free[id], nil
}
func (f *fakeCluster) RunningJobs(_ context.Context, id string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running[id], nil
}
func (f *fakeCluster) CheckPlacement(context.Context, string, string) ([]string, []string, error) {
	return nil, nil, nil
}
func (f *fakeCluster) Quorum(context.Context) (bool, int, bool) { return f.ha, f.votes, f.witness }

func (f *fakeCluster) newJob(j *fakeJob) string {
	f.nextID++
	id := fmt.Sprintf("j%d", f.nextID)
	f.jobs[id] = j
	return id
}

func (f *fakeCluster) MoveVM(_ context.Context, from, uuid, to string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log("move " + uuid + " " + from + "->" + to)
	return f.newJob(&fakeJob{host: from, kind: "move", fail: uuid == f.failMove, onDone: func() {
		i := slices.IndexFunc(f.vms[from], func(v VM) bool { return v.UUID == uuid })
		vm := f.vms[from][i]
		f.vms[from] = slices.Delete(f.vms[from], i, i+1)
		f.vms[to] = append(f.vms[to], vm)
		f.free[from] += int64(vm.MemKiB)
		f.free[to] -= int64(vm.MemKiB)
	}}), nil
}
func (f *fakeCluster) InstallVirtomir(_ context.Context, id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log("install " + id)
	return f.newJob(&fakeJob{host: id, kind: "install", onDone: func() {
		if id != f.stuck {
			f.host(id).Version = "0.10.0"
		}
	}}), nil
}
func (f *fakeCluster) UpgradeOS(_ context.Context, id string, reboot bool) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log(fmt.Sprintf("os %s reboot=%v", id, reboot))
	return f.newJob(&fakeJob{host: id, kind: "os"}), nil
}
func (f *fakeCluster) JobState(_ context.Context, _ string, id string) (string, string, error) {
	if f.gate != nil {
		<-f.gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	j := f.jobs[id]
	if j.polls++; j.polls < 2 {
		return "running", "", nil
	}
	if j.fail {
		return "failed", "migration refused", nil
	}
	if j.onDone != nil {
		j.onDone()
		j.onDone = nil
	}
	return "succeeded", "", nil
}

func newOrch(t *testing.T, f *fakeCluster) (*Orchestrator, *Store) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "lcm.json"))
	if err != nil {
		t.Fatal(err)
	}
	return New(Options{Driver: f, Store: s, SelfID: "h3", PollInterval: time.Millisecond, RejoinTimeout: 50 * time.Millisecond}), s
}

func startPlan(t *testing.T, r *Orchestrator, s *Store, p Plan) Plan {
	t.Helper()
	if p.ID == "" {
		p.ID = "p1"
	}
	if p.HostOrder == nil {
		p.HostOrder = []string{"h1", "h2", "h3"}
	}
	p.TargetVersion, p.State = "0.10.0", PlanPlanned
	if err := s.SavePlan(p); err != nil {
		t.Fatal(err)
	}
	if err := r.Start(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	r.Wait()
	got, _ := s.Plan(p.ID)
	return got
}

func kinds(p Plan, host string) []StepKind {
	var out []StepKind
	for _, st := range p.Steps {
		if st.HostID == host && st.State != StateRunning {
			out = append(out, st.Kind)
		}
	}
	return out
}

func TestRollingUpgradeHappyPath(t *testing.T) {
	f := newFake(3)
	r, s := newOrch(t, f)
	p := startPlan(t, r, s, Plan{})
	if p.State != PlanSucceeded {
		t.Fatalf("plan %s: %+v", p.State, p.Steps)
	}
	want := []StepKind{StepPrecheck, StepEnterMaintenance, StepEvacuate, StepUpdateVirtomir, StepWaitRejoin, StepExitMaintenance}
	for _, h := range []string{"h1", "h2", "h3"} {
		if got := kinds(p, h); !slices.Equal(got, want) {
			t.Errorf("%s steps %v", h, got)
		}
		if f.host(h).Version != "0.10.0" {
			t.Errorf("%s not upgraded", h)
		}
	}
	if len(p.Maintenance) != 0 {
		t.Errorf("hosts left in maintenance: %v", p.Maintenance)
	}
	if i, j := slices.Index(f.calls, "install h1"), slices.Index(f.calls, "install h3"); i < 0 || j < i {
		t.Errorf("order %v (this host last)", f.calls)
	}
}

func TestPrecheckFailures(t *testing.T) {
	for name, setup := range map[string]func(*fakeCluster){
		"two nodes with HA and no witness": func(f *fakeCluster) { f.hosts = f.hosts[:2]; f.ha, f.votes = true, 2 },
		"running backup":                   func(f *fakeCluster) { f.running["h1"] = 1 },
		"another major version":            func(f *fakeCluster) { f.hosts[1].Version = "1.0.0" },
		"no room for the machines":         func(f *fakeCluster) { f.free["h2"], f.free["h3"] = 0, 0 },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(3)
			setup(f)
			r, s := newOrch(t, f)
			p := startPlan(t, r, s, Plan{HostOrder: []string{"h1", "h2"}})
			if p.State != PlanFailed || !slices.Equal(kinds(p, "h1"), []StepKind{StepPrecheck}) || p.Steps[len(p.Steps)-1].State != StateFailed {
				t.Fatalf("plan %s steps %+v", p.State, p.Steps)
			}
			if slices.ContainsFunc(f.calls, func(c string) bool { return strings.HasPrefix(c, "install") || strings.HasPrefix(c, "move") }) {
				t.Errorf("acted despite a failed check: %v", f.calls)
			}
		})
	}
}

func TestEvacuationFailureStopsThePlan(t *testing.T) {
	f := newFake(3)
	f.failMove = "h2-vm"
	r, s := newOrch(t, f)
	p := startPlan(t, r, s, Plan{})
	if p.State != PlanFailed || f.host("h1").Version != "0.10.0" || f.host("h2").Version != "0.9.0" || f.host("h3").Version != "0.9.0" {
		t.Fatalf("plan %s, versions %v", p.State, f.hosts)
	}
	last := p.Steps[len(p.Steps)-1]
	if last.HostID != "h2" || last.Kind != StepEvacuate || !strings.Contains(last.Message, "migration refused") {
		t.Errorf("last step %+v", last)
	}
}

func TestRejoinTimeout(t *testing.T) {
	f := newFake(3)
	f.stuck = "h1"
	r, s := newOrch(t, f)
	p := startPlan(t, r, s, Plan{})
	last := p.Steps[len(p.Steps)-1]
	if p.State != PlanFailed || last.Kind != StepWaitRejoin || !strings.Contains(last.Message, "still runs 0.9.0") {
		t.Fatalf("plan %s last %+v", p.State, last)
	}
}

func TestPauseResumeAndAbort(t *testing.T) {
	f := newFake(3)
	f.gate = make(chan struct{})
	r, s := newOrch(t, f)
	_ = s.SavePlan(Plan{ID: "p1", HostOrder: []string{"h1", "h2", "h3"}, TargetVersion: "0.10.0", State: PlanPlanned})
	if err := r.Start(context.Background(), "p1"); err != nil {
		t.Fatal(err)
	}
	if err := r.Start(context.Background(), "p1"); !errors.Is(err, ErrBusy) {
		t.Errorf("second start: %v", err)
	}
	f.gate <- struct{}{} // the first job poll runs
	if err := r.Pause("p1"); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.Plan("p1"); p.State != PlanPaused {
		t.Errorf("state %s", p.State)
	}
	close(f.gate) // the move in progress finishes, nothing new starts
	time.Sleep(20 * time.Millisecond)
	before, _ := s.Plan("p1")
	time.Sleep(20 * time.Millisecond)
	if after, _ := s.Plan("p1"); len(after.Steps) != len(before.Steps) {
		t.Errorf("steps went on while paused: %d → %d", len(before.Steps), len(after.Steps))
	}
	if err := r.Resume("p1"); err != nil {
		t.Fatal(err)
	}
	r.Wait()
	if p, _ := s.Plan("p1"); p.State != PlanSucceeded {
		t.Fatalf("after resume: %s", p.State)
	}

	g := newFake(3)
	g.gate = make(chan struct{})
	r2, s2 := newOrch(t, g)
	_ = s2.SavePlan(Plan{ID: "p2", HostOrder: []string{"h1", "h2", "h3"}, TargetVersion: "0.10.0", State: PlanPlanned})
	_ = r2.Start(context.Background(), "p2")
	g.gate <- struct{}{}
	_ = r2.Abort("p2")
	close(g.gate)
	r2.Wait()
	p, _ := s2.Plan("p2")
	if p.State != PlanAborted || p.Steps[len(p.Steps)-1].Kind != StepAborted || g.host("h2").Version != "0.9.0" {
		t.Fatalf("abort: %s %+v", p.State, p.Steps[len(p.Steps)-1])
	}
}

func TestMoveBackAndOSUpdates(t *testing.T) {
	f := newFake(3)
	r, s := newOrch(t, f)
	p := startPlan(t, r, s, Plan{MoveBack: true, OSUpdates: true, Reboot: true})
	if p.State != PlanSucceeded {
		t.Fatalf("plan %s %+v", p.State, p.Steps)
	}
	for _, h := range []string{"h1", "h2", "h3"} {
		if len(f.vms[h]) != 1 || f.vms[h][0].UUID != h+"-vm" {
			t.Errorf("%s machines after move back: %+v", h, f.vms[h])
		}
		if !slices.Contains(kinds(p, h), StepMoveBack) || !slices.Contains(kinds(p, h), StepReboot) {
			t.Errorf("%s steps %v", h, kinds(p, h))
		}
	}
	if !slices.Contains(f.calls, "os h1 reboot=true") {
		t.Errorf("calls %v", f.calls)
	}
}

// This host upgrades itself last and restarts on the way: the plan continues after the
// steps it had finished.
func TestInterruptedPlanContinues(t *testing.T) {
	f := newFake(3)
	r, s := newOrch(t, f)
	f.host("h1").Version, f.host("h2").Version = "0.10.0", "0.10.0"
	f.vms["h3"] = nil
	now := time.Now().UTC()
	plan := Plan{ID: "p1", HostOrder: []string{"h1", "h2", "h3"}, TargetVersion: "0.10.0", State: PlanRunning}
	for _, h := range []string{"h1", "h2"} {
		for _, k := range []StepKind{StepPrecheck, StepEnterMaintenance, StepEvacuate, StepUpdateVirtomir, StepWaitRejoin, StepExitMaintenance} {
			plan.Steps = append(plan.Steps, Step{At: now, Kind: k, HostID: h, State: StateDone})
		}
	}
	for _, k := range []StepKind{StepPrecheck, StepEnterMaintenance, StepEvacuate} {
		plan.Steps = append(plan.Steps, Step{At: now, Kind: k, HostID: "h3", State: StateDone})
	}
	plan.Steps = append(plan.Steps, Step{At: now, Kind: StepUpdateVirtomir, HostID: "h3", State: StateRunning})
	f.host("h3").Version = "0.10.0" // it restarted with the new version
	_ = s.SavePlan(plan)
	if err := r.Start(context.Background(), "p1"); err != nil {
		t.Fatal(err)
	}
	r.Wait()
	p, _ := s.Plan("p1")
	if p.State != PlanSucceeded || len(f.calls) != 0 {
		t.Fatalf("plan %s, calls %v (nothing to redo)", p.State, f.calls)
	}
	if got := kinds(p, "h3"); got[len(got)-1] != StepExitMaintenance {
		t.Errorf("h3 steps %v", got)
	}
}
