package ha

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/onurbaydas/virtomir/internal/virt"
)

// fenceRetry is how long the coordinator waits before trying a failed fence again.
const fenceRetry = 30 * time.Second

// watchdogMargin is added to a dead member's watchdog timeout before it counts as reset.
const watchdogMargin = 15 * time.Second

// recoverDead runs on the coordinator: for every member silent longer than the failover
// timeout that ran protected machines, make sure it is off, then restart its machines.
func (m *Manager) recoverDead(ctx context.Context, cfg Config, members []Node, self string) {
	timeout := time.Duration(cfg.FailoverTimeoutSec) * time.Second
	now := m.now()
	for _, n := range members {
		if n.ID == self {
			continue
		}
		m.mu.Lock()
		last, seenOnce := m.seen[n.ID]
		if !seenOnce {
			// Not heard from since this daemon started: count the timeout from our start
			// rather than fencing a member that only missed the first heartbeat.
			last = m.started
		}
		dead := now.Sub(last) > timeout
		victims := m.victimsLocked(cfg, n.ID)
		rc := m.recoveries[n.ID]
		busy := rc != nil && (rc.state == "restarting" || rc.state == "done")
		m.mu.Unlock()
		if !dead || len(victims) == 0 || busy {
			continue
		}
		if rc == nil {
			rc = &recovery{since: now}
			m.mu.Lock()
			m.recoveries[n.ID] = rc
			m.event("error", n.Name, "", "Host stopped answering %s ago; %d protected machine(s) ran there", now.Sub(last).Round(time.Second), len(victims))
			m.mu.Unlock()
		}
		if !cfg.AutoFailover {
			m.note(rc, n.Name, "waiting-auto", "warn", "Automatic failover is off: the machines of %s are not restarted elsewhere", n.Name)
			continue
		}
		if !m.fenced(ctx, cfg, n, rc, last) {
			continue
		}
		m.mu.Lock()
		rc.state = "restarting"
		m.mu.Unlock()
		go m.restart(context.WithoutCancel(ctx), cfg, n, victims, rc)
	}
}

// note sets a waiting state and records it once.
func (m *Manager) note(rc *recovery, node, state, level, format string, args ...any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rc.state = state
	if rc.noted != state {
		rc.noted = state
		m.event(level, node, "", format, args...)
	}
}

// victimsLocked is the protected machines a member ran at its last report, most important first.
func (m *Manager) victimsLocked(cfg Config, nodeID string) []Replica {
	var out []Replica
	for _, r := range m.replicas {
		if p, ok := cfg.VMPolicies[r.UUID]; ok && p.Enabled && r.Owner == nodeID && r.Running {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		pi, pj := cfg.VMPolicies[out[i].UUID], cfg.VMPolicies[out[j].UUID]
		if wi, wj := priorityWeight[pi.Priority], priorityWeight[pj.Priority]; wi != wj {
			return wi < wj
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// fenced decides whether a dead member is certainly off. In order: its BMC switched it off,
// an admin confirmed it, or its armed watchdog has had time to reset it.
func (m *Manager) fenced(ctx context.Context, cfg Config, n Node, rc *recovery, last time.Time) bool {
	now := m.now()
	m.mu.Lock()
	_, confirmed := m.confirmed[n.ID]
	rep, hasRep := m.reports[n.ID]
	m.mu.Unlock()
	if confirmed {
		return true
	}
	if a, ok := m.store.Agent(n.ID); ok && a.Driver != "manual" {
		if now.Before(rc.next) {
			return false
		}
		m.mu.Lock()
		rc.state = "fencing"
		m.mu.Unlock()
		a.NodeID = n.ID
		c, cancel := context.WithTimeout(ctx, 2*time.Minute)
		res, err := m.fencer.Fence(c, a, FenceOff)
		cancel()
		m.mu.Lock()
		defer m.mu.Unlock()
		rc.fence = &res
		if err != nil {
			rc.state, rc.next = "failed", now.Add(fenceRetry)
			m.event("error", n.Name, "", "Fencing failed, machines are NOT restarted (retry in %s): %v", fenceRetry, err)
			return false
		}
		m.event("warn", n.Name, "", "Fenced: %s", res.Message)
		return true
	}
	if hasRep && rep.WatchdogArmed {
		wd := time.Duration(max(rep.WatchdogTimeoutSec, 1)) * time.Second
		safe := last.Add(time.Duration(cfg.FailoverTimeoutSec)*time.Second + wd + watchdogMargin)
		if now.Before(safe) {
			m.note(rc, n.Name, "waiting-watchdog", "warn", "Waiting until %s for the watchdog of %s to have reset it", safe.Format(time.TimeOnly), n.Name)
			return false
		}
		m.mu.Lock()
		m.event("warn", n.Name, "", "Its watchdog has reset the host by now (armed, %s timeout)", wd)
		m.mu.Unlock()
		return true
	}
	m.note(rc, n.Name, "waiting-confirmation", "error",
		"%s has no fence agent and no armed watchdog: make sure it is powered off, then confirm it on the HA page", n.Name)
	return false
}

// restart starts a dead member's machines on survivors, most important first.
func (m *Manager) restart(ctx context.Context, cfg Config, dead Node, victims []Replica, rc *recovery) {
	failed := 0
	for i, r := range victims {
		p := cfg.VMPolicies[r.UUID]
		if i > 0 && p.RestartDelaySec > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(p.RestartDelaySec) * time.Second):
			}
		}
		if err := m.restartOne(ctx, cfg, dead, r, p); err != nil {
			failed++
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if failed > 0 {
		rc.state = "failed"
		m.event("error", dead.Name, "", "Recovery finished: %d of %d machine(s) could not be restarted", failed, len(victims))
		return
	}
	rc.state = "done"
	m.event("info", dead.Name, "", "Recovery finished: all %d machine(s) run elsewhere", len(victims))
}

func (m *Manager) restartOne(ctx context.Context, cfg Config, dead Node, r Replica, p VMPolicy) error {
	targets := m.targets(cfg, dead.ID, r, p)
	if len(targets) == 0 {
		m.mu.Lock()
		m.event("error", dead.Name, r.Name, "No member has room for %s (%d MiB)", r.Name, r.MemoryKiB>>10)
		m.mu.Unlock()
		return errors.New("no target")
	}
	tries := min(len(targets), max(p.MaxRetries, 1))
	var lastErr error
	for _, t := range targets[:tries] {
		next := r
		next.Epoch, next.Owner, next.OwnerName, next.Updated = r.Epoch+1, t.ID, t.Name, m.now()
		c, cancel := context.WithTimeout(ctx, 3*time.Minute)
		var err error
		if t.ID == m.transport.SelfID() {
			err = m.Recover(next)
		} else {
			err = m.transport.Recover(c, t, next)
		}
		cancel()
		if err == nil {
			m.Accept([]Replica{next})
			m.mu.Lock()
			m.event("info", dead.Name, r.Name, "Restarted %s on %s", r.Name, t.Name)
			m.mu.Unlock()
			return nil
		}
		lastErr = err
		m.mu.Lock()
		m.event("error", dead.Name, r.Name, "Could not restart %s on %s: %v", r.Name, t.Name, err)
		m.mu.Unlock()
	}
	return lastErr
}

// targets orders the members a machine may restart on: its preferred member first, then
// the others by free memory; members without room for it are left out.
func (m *Manager) targets(cfg Config, deadID string, r Replica, p VMPolicy) []Node {
	timeout := time.Duration(cfg.FailoverTimeoutSec) * time.Second
	self := m.transport.SelfID()
	selfRep := m.LocalReport()
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	type cand struct {
		n    Node
		free uint64
	}
	var cs []cand
	for _, n := range m.transport.Members() {
		if n.ID == deadID {
			continue
		}
		var rep Report
		if n.ID == self {
			rep = selfRep
		} else {
			t, ok := m.seen[n.ID]
			if !ok || now.Sub(t) > timeout {
				continue
			}
			rep = m.reports[n.ID]
		}
		if rep.MemTotalKiB > 0 && rep.MemFreeKiB < r.MemoryKiB {
			continue
		}
		cs = append(cs, cand{n, rep.MemFreeKiB})
	}
	sort.SliceStable(cs, func(i, j int) bool {
		if (cs[i].n.ID == p.TargetNode) != (cs[j].n.ID == p.TargetNode) {
			return cs[i].n.ID == p.TargetNode
		}
		return cs[i].free > cs[j].free
	})
	out := make([]Node, len(cs))
	for i, c := range cs {
		out[i] = c.n
	}
	return out
}

// Recover defines (if needed) and starts a machine here on the coordinator's request.
func (m *Manager) Recover(r Replica) error {
	if strings.TrimSpace(r.XML) == "" {
		return errors.New("no definition was sent")
	}
	doms, err := m.hv.ListDomains()
	if err != nil {
		return err
	}
	var existing *virt.Domain
	for i := range doms {
		if strings.EqualFold(doms[i].UUID, r.UUID) {
			existing = &doms[i]
		}
	}
	uuid := r.UUID
	if existing == nil {
		if uuid, err = m.hv.AdoptDomain(r.XML); err != nil {
			return fmt.Errorf("define: %w", err)
		}
	} else if existing.State == virt.StateRunning || existing.State == virt.StateBlocked {
		m.Accept([]Replica{r})
		return nil // already running here
	}
	_ = m.hv.SetAutostart(uuid, false)
	if err := m.hv.DomainAction(uuid, virt.ActionStart); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	r.Running, r.Updated = true, m.now()
	m.Accept([]Replica{r})
	m.mu.Lock()
	m.event("info", "", r.Name, "Started %s here by HA (epoch %d)", r.Name, r.Epoch)
	m.mu.Unlock()
	return nil
}

// PlanStep is one machine of a simulated failover.
type PlanStep struct {
	Order    int    `json:"order"`
	UUID     string `json:"uuid"`
	Name     string `json:"name"`
	Priority string `json:"priority"`
	DelaySec int    `json:"delaySec"`
	Target   string `json:"target,omitempty"` // member name; empty: nowhere has room
	Problem  string `json:"problem,omitempty"`
}

// Simulation is what would happen if a member died now.
type Simulation struct {
	Node       Node       `json:"node"`
	Quorate    bool       `json:"quorate"` // the others keep quorum
	Fencing    string     `json:"fencing"` // how it would be fenced
	Steps      []PlanStep `json:"steps"`
	CanRecover bool       `json:"canRecover"`
}

// Simulate plans the failover of a member without doing anything.
func (m *Manager) Simulate(nodeID string) (Simulation, error) {
	cfg := m.store.Config()
	members := m.transport.Members()
	var dead Node
	for _, n := range members {
		if n.ID == nodeID {
			dead = n
		}
	}
	if dead.ID == "" {
		return Simulation{}, errors.New("no such member")
	}
	sim := Simulation{Node: dead}
	m.mu.Lock()
	votes, total := 0, max(len(members), 1)
	now := m.now()
	for _, n := range members {
		if n.ID != nodeID && (n.ID == m.transport.SelfID() || now.Sub(m.seen[n.ID]) <= time.Duration(cfg.FailoverTimeoutSec)*time.Second) {
			votes++
		}
	}
	if cfg.Witness != "" {
		total++
		if m.quorum.WitnessReachable {
			votes++
		}
	}
	sim.Quorate = votes >= total/2+1
	victims := m.victimsLocked(cfg, nodeID)
	rep, hasRep := m.reports[nodeID]
	m.mu.Unlock()

	switch a, ok := m.store.Agent(nodeID); {
	case ok && a.Driver != "manual":
		sim.Fencing = "power off through its BMC (" + a.Driver + ")"
	case hasRep && rep.WatchdogArmed:
		sim.Fencing = fmt.Sprintf("wait for its hardware watchdog (%ds)", rep.WatchdogTimeoutSec)
	default:
		sim.Fencing = "an admin must confirm it is off"
	}
	sim.CanRecover = sim.Quorate && cfg.AutoFailover
	for i, r := range victims {
		p := cfg.VMPolicies[r.UUID]
		st := PlanStep{Order: i + 1, UUID: r.UUID, Name: r.Name, Priority: p.Priority, DelaySec: p.RestartDelaySec}
		if t := m.targets(cfg, nodeID, r, p); len(t) > 0 {
			st.Target = t[0].Name
		} else {
			st.Problem = "no other member has enough free memory"
			sim.CanRecover = false
		}
		sim.Steps = append(sim.Steps, st)
	}
	return sim, nil
}
