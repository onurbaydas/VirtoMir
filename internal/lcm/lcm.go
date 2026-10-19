// Package lcm is the lifecycle manager (ADR-0070): rolling upgrades of VirtoMir itself, and
// optionally of the hosts' OS packages, one cluster member at a time. Each member is checked,
// put into maintenance, emptied by live migration, updated, waited for and put back; the
// plan and its step log live in -state-dir/lcm.json, so a restart (the orchestrating host
// upgrades itself last) continues where it stopped.
package lcm

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"sync"
	"time"
)

// StepKind is what one step of a plan does.
type StepKind string

const (
	StepPrecheck         StepKind = "precheck"
	StepEnterMaintenance StepKind = "enter_maintenance"
	StepEvacuate         StepKind = "evacuate"
	StepUpdateVirtomir   StepKind = "update_virtomir"
	StepUpdateOS         StepKind = "update_os"
	StepReboot           StepKind = "reboot"
	StepWaitRejoin       StepKind = "wait_rejoin"
	StepExitMaintenance  StepKind = "exit_maintenance"
	StepMoveBack         StepKind = "move_back"
	StepAborted          StepKind = "aborted"
)

// Step states.
const (
	StateRunning = "running"
	StateDone    = "done"
	StateFailed  = "failed"
	StateSkipped = "skipped"
)

// Step is one line of a plan's log.
type Step struct {
	At      time.Time `json:"at"`
	Kind    StepKind  `json:"kind"`
	Host    string    `json:"host"`
	HostID  string    `json:"hostId"`
	Message string    `json:"message"`
	State   string    `json:"state"`
}

// PlanState is where a plan is.
type PlanState string

const (
	PlanPlanned   PlanState = "planned"
	PlanRunning   PlanState = "running"
	PlanPaused    PlanState = "paused"
	PlanSucceeded PlanState = "succeeded"
	PlanFailed    PlanState = "failed"
	PlanAborted   PlanState = "aborted"
)

// Finished reports whether a plan in state s will not run again.
func (s PlanState) Finished() bool {
	return s == PlanSucceeded || s == PlanFailed || s == PlanAborted
}

// HostCheck is one host's pre-check result.
type HostCheck struct {
	HostID   string   `json:"hostId"`
	HostName string   `json:"hostName"`
	OK       bool     `json:"ok"`
	Problems []string `json:"problems"`
}

// Plan is one rolling upgrade.
type Plan struct {
	ID       string    `json:"id"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished,omitzero"`
	State    PlanState `json:"state"`
	// HostOrder lists member IDs; the orchestrating host is last.
	HostOrder     []string `json:"hostOrder"`
	TargetVersion string   `json:"targetVersion"`
	OSUpdates     bool     `json:"osUpdates"`
	Reboot        bool     `json:"reboot"`
	MoveBack      bool     `json:"moveBack"`
	// StartedBy is who started the plan; the steps act on the members as this user.
	StartedBy     string `json:"startedBy"`
	StartedByRole string `json:"startedByRole"`
	// Maintenance lists the hosts in maintenance right now.
	Maintenance []string `json:"maintenance"`
	// Moved remembers which machines each host gave away, for moving them back.
	Moved map[string][]PlannedMove `json:"moved,omitempty"`
	Steps []Step                   `json:"steps"`
}

// Store keeps the plans in a JSON file.
type Store struct {
	path     string
	mu       sync.Mutex
	plans    []Plan
	watchers map[chan struct{}]bool
}

// Open loads path; a missing file is an empty store.
func Open(path string) (*Store, error) {
	s := &Store{path: path, watchers: map[chan struct{}]bool{}}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("read lcm store: %w", err)
	}
	if err := json.Unmarshal(raw, &s.plans); err != nil {
		return nil, fmt.Errorf("read lcm store: %w", err)
	}
	return s, nil
}

func (s *Store) saveLocked() error {
	defer s.notifyLocked()
	if s.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(s.plans, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) notifyLocked() {
	for ch := range s.watchers {
		select {
		case ch <- struct{}{}:
		default: // a notification is already waiting
		}
	}
}

// Watch fires after every change until stop is called.
func (s *Store) Watch() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	s.mu.Lock()
	s.watchers[ch] = true
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		delete(s.watchers, ch)
		s.mu.Unlock()
	}
}

func clonePlan(p Plan) Plan {
	p.HostOrder = slices.Clone(p.HostOrder)
	p.Maintenance = slices.Clone(p.Maintenance)
	p.Steps = slices.Clone(p.Steps)
	if p.Moved != nil {
		m := make(map[string][]PlannedMove, len(p.Moved))
		for k, v := range p.Moved {
			m[k] = slices.Clone(v)
		}
		p.Moved = m
	}
	return p
}

// Plans returns every plan, newest first.
func (s *Store) Plans() []Plan {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Plan, 0, len(s.plans))
	for i := len(s.plans) - 1; i >= 0; i-- {
		out = append(out, clonePlan(s.plans[i]))
	}
	return out
}

// Plan returns the plan with id.
func (s *Store) Plan(id string) (Plan, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := s.index(id); i >= 0 {
		return clonePlan(s.plans[i]), true
	}
	return Plan{}, false
}

// Active is the plan that is running or paused, if any.
func (s *Store) Active() (Plan, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.plans {
		if !p.State.Finished() && p.State != PlanPlanned {
			return clonePlan(p), true
		}
	}
	return Plan{}, false
}

func (s *Store) index(id string) int {
	return slices.IndexFunc(s.plans, func(p Plan) bool { return p.ID == id })
}

// SavePlan stores p, replacing the plan with its ID.
func (s *Store) SavePlan(p Plan) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p = clonePlan(p)
	if i := s.index(p.ID); i >= 0 {
		s.plans[i] = p
	} else {
		s.plans = append(s.plans, p)
	}
	return s.saveLocked()
}

// update changes plan id under the lock.
func (s *Store) update(id string, fn func(*Plan)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.index(id)
	if i < 0 {
		return fmt.Errorf("no plan %s", id)
	}
	fn(&s.plans[i])
	return s.saveLocked()
}

// AppendStep adds a line to a plan's log, or replaces its last line when that is the same
// step still running (running → done/failed).
func (s *Store) AppendStep(planID string, step Step) error {
	return s.update(planID, func(p *Plan) {
		if n := len(p.Steps); n > 0 {
			last := p.Steps[n-1]
			if last.State == StateRunning && last.Kind == step.Kind && last.HostID == step.HostID {
				p.Steps[n-1] = step
				return
			}
		}
		p.Steps = append(p.Steps, step)
	})
}

// SetPlanState changes a plan's state; a finished one gets its finish time.
func (s *Store) SetPlanState(planID string, state PlanState, now time.Time) error {
	return s.update(planID, func(p *Plan) {
		p.State = state
		if state.Finished() {
			p.Finished = now
		}
	})
}

// SetMaintenance puts a host into maintenance or takes it out.
func (s *Store) SetMaintenance(planID, hostID string, on bool) error {
	return s.update(planID, func(p *Plan) {
		p.Maintenance = slices.DeleteFunc(p.Maintenance, func(h string) bool { return h == hostID })
		if on {
			p.Maintenance = append(p.Maintenance, hostID)
		}
	})
}

// SetMoved records the machines a host gave away.
func (s *Store) SetMoved(planID, hostID string, moves []PlannedMove) error {
	return s.update(planID, func(p *Plan) {
		if p.Moved == nil {
			p.Moved = map[string][]PlannedMove{}
		}
		p.Moved[hostID] = slices.Clone(moves)
	})
}
