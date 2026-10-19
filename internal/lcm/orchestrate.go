package lcm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"
)

// Member is a cluster member with the version it runs.
type Member struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Driver is how the orchestrator reaches the members: the API implements it over the peer
// protocol (and in-process for this host), tests with fakes. Every call acts as the user
// who started the plan.
type Driver interface {
	Members(ctx context.Context) ([]Member, error)
	Hello(ctx context.Context, hostID string) (version string, err error)
	RunningVMs(ctx context.Context, hostID string) ([]VM, error)
	FreeMemory(ctx context.Context, hostID string) (int64, error)
	RunningJobs(ctx context.Context, hostID string) (int, error)
	CheckPlacement(ctx context.Context, vmUUID, targetHostID string) (hard, soft []string, err error)
	MoveVM(ctx context.Context, hostID, uuid, targetHostID string) (jobID string, err error)
	JobState(ctx context.Context, hostID, jobID string) (state, msg string, err error)
	InstallVirtomir(ctx context.Context, hostID string) (jobID string, err error)
	UpgradeOS(ctx context.Context, hostID string, reboot bool) (jobID string, err error)
	// Quorum is what high availability needs: whether it is on, its total votes and
	// whether its witness answers.
	Quorum(ctx context.Context) (haEnabled bool, totalVotes int, witnessReachable bool)
}

// Options configure an Orchestrator.
type Options struct {
	Driver Driver
	Store  *Store
	SelfID string
	Now    func() time.Time
	// PollInterval is how often jobs and rejoining hosts are polled (default 2 s).
	PollInterval time.Duration
	// RejoinTimeout is how long a host may take to come back (default 10 minutes).
	RejoinTimeout time.Duration
	Log           *slog.Logger
}

// Orchestrator runs one plan at a time, host after host.
type Orchestrator struct {
	o Options

	mu      sync.Mutex
	planID  string
	host    string
	kind    StepKind
	paused  bool
	aborted bool
	resume  chan struct{}
	done    chan struct{}
}

// New returns an orchestrator.
func New(o Options) *Orchestrator {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.PollInterval <= 0 {
		o.PollInterval = 2 * time.Second
	}
	if o.RejoinTimeout <= 0 {
		o.RejoinTimeout = 10 * time.Minute
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return &Orchestrator{o: o}
}

// Status is what the orchestrator is doing.
type Status struct {
	PlanID string   `json:"planId,omitempty"`
	Host   string   `json:"host,omitempty"`
	Step   StepKind `json:"step,omitempty"`
	Paused bool     `json:"paused"`
}

func (r *Orchestrator) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	return Status{PlanID: r.planID, Host: r.host, Step: r.kind, Paused: r.paused}
}

// ErrBusy: another plan is running.
var ErrBusy = errors.New("a rolling upgrade is already running")

var errAborted = errors.New("aborted")

// Start runs plan planID in the background. A plan that was interrupted (this host restarted
// while upgrading itself) continues after the steps it already finished.
func (r *Orchestrator) Start(ctx context.Context, planID string) error {
	p, ok := r.o.Store.Plan(planID)
	if !ok {
		return fmt.Errorf("no plan %s", planID)
	}
	if p.State.Finished() {
		return fmt.Errorf("plan %s has finished (%s)", planID, p.State)
	}
	r.mu.Lock()
	if r.planID != "" {
		r.mu.Unlock()
		return ErrBusy
	}
	r.planID, r.paused, r.aborted = planID, p.State == PlanPaused, false
	r.resume, r.done = make(chan struct{}), make(chan struct{})
	done := r.done
	r.mu.Unlock()
	if p.State != PlanPaused {
		_ = r.o.Store.SetPlanState(planID, PlanRunning, r.o.Now())
	}
	go func() {
		defer close(done)
		r.run(ctx, planID)
		r.mu.Lock()
		r.planID, r.host, r.kind = "", "", ""
		r.mu.Unlock()
	}()
	return nil
}

// Wait blocks until the running plan stops (for tests).
func (r *Orchestrator) Wait() {
	r.mu.Lock()
	done := r.done
	r.mu.Unlock()
	if done != nil {
		<-done
	}
}

func (r *Orchestrator) control(planID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.planID == "" || r.planID != planID {
		return fmt.Errorf("plan %s is not running", planID)
	}
	return nil
}

// Pause lets the step in progress finish and starts no new one.
func (r *Orchestrator) Pause(planID string) error {
	if err := r.control(planID); err != nil {
		return err
	}
	r.mu.Lock()
	r.paused = true
	r.mu.Unlock()
	return r.o.Store.SetPlanState(planID, PlanPaused, r.o.Now())
}

// Resume continues a paused plan.
func (r *Orchestrator) Resume(planID string) error {
	if err := r.control(planID); err != nil {
		return err
	}
	r.mu.Lock()
	if r.paused {
		r.paused = false
		close(r.resume)
		r.resume = make(chan struct{})
	}
	r.mu.Unlock()
	return r.o.Store.SetPlanState(planID, PlanRunning, r.o.Now())
}

// Abort stops the plan once the step in progress has finished.
func (r *Orchestrator) Abort(planID string) error {
	if err := r.control(planID); err != nil {
		return err
	}
	r.mu.Lock()
	r.aborted = true
	if r.paused {
		r.paused = false
		close(r.resume)
		r.resume = make(chan struct{})
	}
	r.mu.Unlock()
	return nil
}

// gate is passed before every step: it waits while paused and stops an aborted plan.
func (r *Orchestrator) gate(ctx context.Context) error {
	for {
		r.mu.Lock()
		aborted, paused, resume := r.aborted, r.paused, r.resume
		r.mu.Unlock()
		if aborted {
			return errAborted
		}
		if !paused {
			return ctx.Err()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-resume:
		}
	}
}

// step logs a step as running, runs fn and logs how it ended.
type stepRun struct {
	r      *Orchestrator
	planID string
	host   Member
}

func (s stepRun) log(kind StepKind, state, msg string) {
	_ = s.r.o.Store.AppendStep(s.planID, Step{At: s.r.o.Now().UTC(), Kind: kind, Host: s.host.Name, HostID: s.host.ID, State: state, Message: msg})
}

func (s stepRun) do(ctx context.Context, kind StepKind, fn func() (string, error)) error {
	if err := s.r.gate(ctx); err != nil {
		return err
	}
	s.r.mu.Lock()
	s.r.host, s.r.kind = s.host.Name, kind
	s.r.mu.Unlock()
	s.log(kind, StateRunning, "")
	msg, err := fn()
	if err != nil {
		s.log(kind, StateFailed, err.Error())
		return err
	}
	s.log(kind, StateDone, msg)
	return nil
}

// doneSteps are the steps already finished for host in p (an interrupted plan resumes).
func doneSteps(p Plan, hostID string) map[StepKind]bool {
	out := map[StepKind]bool{}
	for _, st := range p.Steps {
		if st.HostID == hostID && (st.State == StateDone || st.State == StateSkipped) {
			out[st.Kind] = true
		}
	}
	return out
}

func (r *Orchestrator) run(ctx context.Context, planID string) {
	p, _ := r.o.Store.Plan(planID)
	members, err := r.o.Driver.Members(ctx)
	if err != nil {
		r.finish(planID, Member{Name: "cluster"}, PlanFailed, "cannot list the members: "+err.Error())
		return
	}
	byID := map[string]Member{}
	for _, m := range members {
		byID[m.ID] = m
	}
	for _, id := range p.HostOrder {
		host, ok := byID[id]
		if !ok {
			r.finish(planID, Member{ID: id, Name: id}, PlanFailed, "this host is no longer a cluster member")
			return
		}
		cur, _ := r.o.Store.Plan(planID)
		if err := r.upgradeHost(ctx, cur, host, members); err != nil {
			if errors.Is(err, errAborted) {
				r.finish(planID, host, PlanAborted, "aborted by an administrator; the hosts done so far stay upgraded")
			} else {
				_ = r.o.Store.SetPlanState(planID, PlanFailed, r.o.Now())
			}
			return
		}
	}
	_ = r.o.Store.SetPlanState(planID, PlanSucceeded, r.o.Now())
	r.o.Log.Info("rolling upgrade finished", "plan", planID)
}

func (r *Orchestrator) finish(planID string, host Member, state PlanState, msg string) {
	kind, st := StepPrecheck, StateFailed
	if state == PlanAborted {
		kind, st = StepAborted, StateDone
	}
	stepRun{r, planID, host}.log(kind, st, msg)
	_ = r.o.Store.SetPlanState(planID, state, r.o.Now())
}

func (r *Orchestrator) upgradeHost(ctx context.Context, p Plan, host Member, members []Member) error {
	done := doneSteps(p, host.ID)
	s := stepRun{r, p.ID, host}
	d := r.o.Driver
	run := func(kind StepKind, fn func() (string, error)) error {
		if done[kind] {
			return nil
		}
		return s.do(ctx, kind, fn)
	}

	var moves []PlannedMove
	if err := run(StepPrecheck, func() (string, error) {
		check := r.Precheck(ctx, p, host, members)
		if !check.OK {
			return "", errors.New(joinProblems(check.Problems))
		}
		return "all checks passed", nil
	}); err != nil {
		return err
	}
	if err := run(StepEnterMaintenance, func() (string, error) {
		return "no new machines are placed here", r.o.Store.SetMaintenance(p.ID, host.ID, true)
	}); err != nil {
		return err
	}
	if err := run(StepEvacuate, func() (string, error) {
		cur, _ := r.o.Store.Plan(p.ID)
		planned, problems, err := r.planEvacuation(ctx, cur, host, members)
		if err != nil {
			return "", err
		}
		if len(problems) > 0 {
			return "", errors.New(joinProblems(problems))
		}
		for _, m := range planned {
			if err := r.gate(ctx); err != nil {
				return "", err
			}
			jobID, err := d.MoveVM(ctx, host.ID, m.VMUUID, m.TargetHostID)
			if err == nil {
				err = r.waitJob(ctx, host.ID, jobID)
			}
			if err != nil {
				_ = r.o.Store.SetMoved(p.ID, host.ID, moves)
				return "", fmt.Errorf("moving %s to %s: %w", m.VMName, m.TargetHostName, err)
			}
			moves = append(moves, m)
		}
		_ = r.o.Store.SetMoved(p.ID, host.ID, moves)
		return fmt.Sprintf("%d machines moved away", len(moves)), nil
	}); err != nil {
		return err
	}
	if err := run(StepUpdateVirtomir, func() (string, error) {
		if have, err := d.Hello(ctx, host.ID); err == nil && p.TargetVersion != "" && AtLeast(have, p.TargetVersion) {
			return "already runs " + have, nil
		}
		jobID, err := d.InstallVirtomir(ctx, host.ID)
		if err != nil {
			return "", err
		}
		// The host restarts VirtoMir while installing: a lost connection is expected, and
		// the next step waits for it to come back.
		if err := r.waitJob(ctx, host.ID, jobID); err != nil && !errors.Is(err, errJobLost) {
			return "", err
		}
		return "update installed", nil
	}); err != nil {
		return err
	}
	if p.OSUpdates {
		if err := run(StepUpdateOS, func() (string, error) {
			jobID, err := d.UpgradeOS(ctx, host.ID, p.Reboot)
			if err != nil {
				return "", err
			}
			if err := r.waitJob(ctx, host.ID, jobID); err != nil && !errors.Is(err, errJobLost) {
				return "", err
			}
			return "OS packages upgraded", nil
		}); err != nil {
			return err
		}
		if p.Reboot {
			if err := run(StepReboot, func() (string, error) { return "restarting with the new packages", nil }); err != nil {
				return err
			}
		}
	}
	if err := run(StepWaitRejoin, func() (string, error) {
		return r.waitRejoin(ctx, host.ID, p.TargetVersion)
	}); err != nil {
		return err
	}
	if err := run(StepExitMaintenance, func() (string, error) {
		return "machines may be placed here again", r.o.Store.SetMaintenance(p.ID, host.ID, false)
	}); err != nil {
		return err
	}
	if p.MoveBack {
		if err := run(StepMoveBack, func() (string, error) {
			cur, _ := r.o.Store.Plan(p.ID)
			back := 0
			for _, m := range cur.Moved[host.ID] {
				if err := r.gate(ctx); err != nil {
					return "", err
				}
				jobID, err := d.MoveVM(ctx, m.TargetHostID, m.VMUUID, host.ID)
				if err == nil {
					err = r.waitJob(ctx, m.TargetHostID, jobID)
				}
				if err != nil {
					return "", fmt.Errorf("moving %s back from %s: %w", m.VMName, m.TargetHostName, err)
				}
				back++
			}
			return fmt.Sprintf("%d machines moved back", back), nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func joinProblems(p []string) string {
	out := ""
	for i, s := range p {
		if i > 0 {
			out += "; "
		}
		out += s
	}
	return out
}

// Precheck checks one host right now.
func (r *Orchestrator) Precheck(ctx context.Context, p Plan, host Member, members []Member) HostCheck {
	d := r.o.Driver
	c := HostCheck{HostID: host.ID, HostName: host.Name, Problems: []string{}}
	add := func(ok bool, msg string) {
		if !ok {
			c.Problems = append(c.Problems, msg)
		}
	}
	var versions, up []string
	for _, m := range members {
		v, err := d.Hello(ctx, m.ID)
		if err != nil {
			c.Problems = append(c.Problems, m.Name+" does not answer: "+err.Error())
			continue
		}
		versions = append(versions, v)
		if !slices.Contains(p.Maintenance, m.ID) {
			up = append(up, m.ID)
		}
	}
	if p.TargetVersion != "" {
		versions = append(versions, p.TargetVersion)
	}
	add(SameMajor(versions), "the members and the target version differ in their major version; upgrade one major version at a time")
	haOn, votes, witness := d.Quorum(ctx)
	add(QuorumSafeWithout(up, host.ID, haOn, votes, witness))
	if n, err := d.RunningJobs(ctx, host.ID); err != nil {
		add(false, "cannot list the host's jobs: "+err.Error())
	} else {
		add(RunningJobsSafe(n))
	}
	_, problems, err := r.planEvacuation(ctx, p, host, members)
	if err != nil {
		add(false, err.Error())
	}
	c.Problems = append(c.Problems, problems...)
	c.OK = len(c.Problems) == 0
	return c
}

func (r *Orchestrator) planEvacuation(ctx context.Context, p Plan, host Member, members []Member) ([]PlannedMove, []string, error) {
	d := r.o.Driver
	vms, err := d.RunningVMs(ctx, host.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot list the host's machines: %w", err)
	}
	var targets []Target
	for _, m := range members {
		if m.ID == host.ID || slices.Contains(p.Maintenance, m.ID) {
			continue
		}
		free, err := d.FreeMemory(ctx, m.ID)
		if err != nil {
			continue // a member that does not answer takes nothing
		}
		targets = append(targets, Target{HostID: m.ID, Name: m.Name, FreeKiB: free})
	}
	moves, problems := EvacuationFeasible(vms, targets, func(vm, target string) ([]string, []string) {
		hard, soft, err := d.CheckPlacement(ctx, vm, target)
		if err != nil {
			return []string{err.Error()}, nil
		}
		return hard, soft
	})
	return moves, problems, nil
}

// errJobLost: the host stopped answering about the job (it is restarting).
var errJobLost = errors.New("the host stopped answering while the job ran")

func (r *Orchestrator) waitJob(ctx context.Context, hostID, jobID string) error {
	misses := 0
	for {
		state, msg, err := r.o.Driver.JobState(ctx, hostID, jobID)
		switch {
		case err != nil:
			if misses++; misses >= 3 {
				return fmt.Errorf("%w: %v", errJobLost, err)
			}
		case state == "succeeded":
			return nil
		case state == "failed":
			return errors.New(msg)
		default:
			misses = 0
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(r.o.PollInterval):
		}
	}
}

func (r *Orchestrator) waitRejoin(ctx context.Context, hostID, target string) (string, error) {
	deadline := r.o.Now().Add(r.o.RejoinTimeout)
	last := "it does not answer"
	for {
		v, err := r.o.Driver.Hello(ctx, hostID)
		if err == nil {
			if AtLeast(v, target) {
				return "back with " + v, nil
			}
			last = "it still runs " + v
		}
		if r.o.Now().After(deadline) {
			return "", fmt.Errorf("the host did not come back with %s within %s: %s", orDash(target), r.o.RejoinTimeout, last)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(r.o.PollInterval):
		}
	}
}

func orDash(s string) string {
	if s == "" {
		return "the new version"
	}
	return s
}
