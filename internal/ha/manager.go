package ha

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/onurbaydas/virtomir/internal/virt"
)

// Node is a cluster member as HA sees it.
type Node struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Report is what a member says about itself in every heartbeat.
type Report struct {
	NodeID        string    `json:"nodeId"`
	Name          string    `json:"name"`
	Time          time.Time `json:"time"`
	Quorate       bool      `json:"quorate"`
	ConfigVersion int       `json:"configVersion"`
	// WatchdogArmed: if this member hangs or loses quorum it resets itself within
	// WatchdogTimeoutSec, so others may restart its machines after that.
	WatchdogArmed      bool   `json:"watchdogArmed"`
	WatchdogTimeoutSec int    `json:"watchdogTimeoutSec"`
	MemTotalKiB        uint64 `json:"memTotalKiB"`
	MemFreeKiB         uint64 `json:"memFreeKiB"`
}

// Replica is what every member keeps of a protected machine, so it can be started on a
// survivor when its host dies. Epoch grows with every failover; the highest wins.
type Replica struct {
	UUID      string    `json:"uuid"`
	Name      string    `json:"name"`
	Owner     string    `json:"owner"` // member ID the machine is defined on
	OwnerName string    `json:"ownerName"`
	Epoch     int       `json:"epoch"`
	Running   bool      `json:"running"` // at the last look: only running machines are restarted
	VCPUs     int       `json:"vcpus"`
	MemoryKiB uint64    `json:"memoryKiB"`
	XML       string    `json:"xml"`
	Updated   time.Time `json:"updated"`
}

// newer reports whether r should replace old.
func (r Replica) newer(old Replica) bool {
	if r.Epoch != old.Epoch {
		return r.Epoch > old.Epoch
	}
	return r.Updated.After(old.Updated)
}

// Transport reaches the other members (implemented by the API server over pinned HTTPS).
type Transport interface {
	SelfID() string
	Members() []Node // every member, this one included; empty or one: standalone
	Heartbeat(ctx context.Context, n Node, mine Report) (Report, error)
	PushReplicas(ctx context.Context, n Node, reps []Replica) error
	FetchConfig(ctx context.Context, n Node) (Config, error)
	Recover(ctx context.Context, n Node, rep Replica) error
}

// Hypervisor is the local libvirt (implemented by *virt.Client).
type Hypervisor interface {
	ListDomains() ([]virt.Domain, error)
	DomainXML(uuid string, inactive bool) (string, error)
	DomainAction(uuid string, action virt.Action) error
	SetAutostart(uuid string, on bool) error
	AdoptDomain(doc string) (string, error)
	HostStats() (virt.HostStats, error)
}

// Event is one thing HA did or refused to do, newest first in Status.
type Event struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"` // info, warn, error
	Node    string    `json:"node,omitempty"`
	Machine string    `json:"machine,omitempty"`
	Message string    `json:"message"`
}

// NodeState is a member as the UI sees it.
type NodeState struct {
	Node
	Self     bool      `json:"self"`
	Status   string    `json:"status"` // online, suspect (late), dead
	LastSeen time.Time `json:"lastSeen,omitempty"`
	Report   *Report   `json:"report,omitempty"`
	// Recovery: "", "fencing", "waiting-confirmation", "waiting-watchdog", "restarting",
	// "done" or "failed" while the member is dead.
	Recovery string       `json:"recovery,omitempty"`
	Fence    *FenceResult `json:"fence,omitempty"`
	// Machines is how many protected machines it runs (from the replicas).
	Machines int `json:"machines"`
}

// Quorum is the vote count of the partition this member sees.
type Quorum struct {
	Quorate          bool   `json:"quorate"`
	Votes            int    `json:"votes"`
	TotalVotes       int    `json:"totalVotes"`
	Required         int    `json:"required"`
	Witness          string `json:"witness,omitempty"`
	WitnessReachable bool   `json:"witnessReachable"`
	Coordinator      string `json:"coordinator,omitempty"` // member ID
}

// MachineState is a protected machine with where it runs now.
type MachineState struct {
	VMPolicy
	Owner     string    `json:"owner,omitempty"`
	OwnerName string    `json:"ownerName,omitempty"`
	Running   bool      `json:"running"`
	Epoch     int       `json:"epoch"`
	Updated   time.Time `json:"updated,omitempty"`
	// Known: some member has sent its definition, so it can be restarted elsewhere.
	Known bool `json:"known"`
}

// Status is everything the HA page shows.
type Status struct {
	SelfID        string         `json:"selfId"`
	Clustered     bool           `json:"clustered"`
	Quorum        Quorum         `json:"quorum"`
	Nodes         []NodeState    `json:"nodes"`
	Machines      []MachineState `json:"machines"`
	Config        Config         `json:"config"`
	WatchdogArmed bool           `json:"watchdogArmed"`
	WatchdogError string         `json:"watchdogError,omitempty"`
	Events        []Event        `json:"events"`
}

// Manager runs HA on one member.
type Manager struct {
	store     *Store
	transport Transport
	hv        Hypervisor
	fencer    *Fencer
	log       *slog.Logger
	dir       string
	now       func() time.Time
	dial      func(ctx context.Context, addr string) error
	watchdog  Watchdog
	// Notify, if set, receives warnings and errors (to send them through alert channels).
	Notify func(subject, message string)
	// FilterTargets, if set, filters and orders candidate nodes for restarting a protected
	// machine according to placement rules (ADR-0062). nil preserves legacy behaviour.
	// runningOn maps machine UUID → member ID of every protected machine believed running
	// (built from m.replicas where Running is true → Replica.Owner).
	FilterTargets func(r Replica, candidates []Node, runningOn map[string]string) []Node

	mu         sync.Mutex
	seen       map[string]time.Time // member ID → last heartbeat answer
	reports    map[string]Report
	mine       Report             // this member's last heartbeat, shown on its own row
	replicas   map[string]Replica // by machine UUID
	recoveries map[string]*recovery
	confirmed  map[string]time.Time // admin confirmed member is off
	events     []Event
	quorum     Quorum
	lostSince  time.Time // when this member lost quorum (zero: has it)
	selfFenced bool
	resumed    bool // protected machines restarted after this member booted
	// atBoot is what this member ran when it last looked before this start: machine UUID →
	// replica epoch. Taken on the first beat, before replicate rewrites the replicas.
	atBoot  map[string]int
	started time.Time
	wdErr   string
	listErr string // the last "list machines" failure logged at Warn; repeats go to Debug
	tick    int
}

type recovery struct {
	state string
	fence *FenceResult
	since time.Time
	next  time.Time // retry fencing not before
	// noted keeps "waiting" events from repeating every heartbeat.
	noted string
}

const maxEvents = 300

// NewManager loads replicas and events from dir (-state-dir/ha).
func NewManager(store *Store, t Transport, hv Hypervisor, dir string, log *slog.Logger) *Manager {
	m := &Manager{
		store: store, transport: t, hv: hv, fencer: NewFencer(), log: log, dir: dir, now: time.Now,
		dial: func(ctx context.Context, addr string) error {
			c, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
			if err == nil {
				c.Close()
			}
			return err
		},
		watchdog:   NewWatchdog(),
		seen:       map[string]time.Time{},
		reports:    map[string]Report{},
		replicas:   map[string]Replica{},
		recoveries: map[string]*recovery{},
		confirmed:  map[string]time.Time{},
	}
	m.started = m.now()
	_ = os.MkdirAll(dir, 0o700)
	m.load("replicas.json", &m.replicas)
	m.load("events.json", &m.events)
	if m.replicas == nil {
		m.replicas = map[string]Replica{}
	}
	return m
}

func (m *Manager) load(name string, v any) {
	b, err := os.ReadFile(filepath.Join(m.dir, name))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			m.log.Warn("ha state", "file", name, "err", err)
		}
		return
	}
	if err := json.Unmarshal(b, v); err != nil {
		m.log.Warn("ha state", "file", name, "err", err)
	}
}

// persist writes v atomically; it holds machine definitions, so 0600.
func (m *Manager) persist(name string, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	p := filepath.Join(m.dir, name)
	if err := os.WriteFile(p+".tmp", b, 0o600); err == nil {
		_ = os.Rename(p+".tmp", p)
	}
}

// event records something HA did (caller holds m.mu).
func (m *Manager) event(level, node, machine, format string, args ...any) {
	e := Event{Time: m.now(), Level: level, Node: node, Machine: machine, Message: fmt.Sprintf(format, args...)}
	m.events = append([]Event{e}, m.events...)
	if len(m.events) > maxEvents {
		m.events = m.events[:maxEvents]
	}
	m.persist("events.json", m.events)
	if m.Notify != nil && level != "info" {
		subject := "HA"
		if node != "" {
			subject += " · " + node
		}
		go m.Notify(subject, e.Message)
	}
	switch level {
	case "error":
		m.log.Error("ha: "+e.Message, "node", node, "machine", machine)
	case "warn", "warning":
		m.log.Warn("ha: "+e.Message, "node", node, "machine", machine)
	default:
		m.log.Info("ha: "+e.Message, "node", node, "machine", machine)
	}
}

// Run beats until ctx ends, then disarms the watchdog (a clean stop must not reset the host).
func (m *Manager) Run(ctx context.Context) {
	defer func() {
		if err := m.watchdog.Disarm(); err != nil {
			m.log.Warn("ha: disarm watchdog", "err", err)
		}
	}()
	for {
		cfg := m.store.Config()
		m.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(cfg.HeartbeatIntervalSec) * time.Second):
		}
	}
}

// Tick is one heartbeat round: exchange reports, count votes, replicate, protect, recover.
func (m *Manager) Tick(ctx context.Context) {
	cfg := m.store.Config()
	interval := time.Duration(cfg.HeartbeatIntervalSec) * time.Second
	self := m.transport.SelfID()
	members := m.transport.Members()
	clustered := len(members) > 1

	doms, domErr := m.hv.ListDomains()
	m.mu.Lock()
	m.tick++
	tick := m.tick
	if m.atBoot == nil {
		m.atBoot = map[string]int{}
		for id, r := range m.replicas {
			if r.Owner == self && r.Running {
				m.atBoot[id] = r.Epoch
			}
		}
	}
	m.mu.Unlock()

	// 1. Heartbeats, in parallel, each bounded by the interval.
	mine := m.report(cfg)
	m.mu.Lock()
	m.mine = mine
	m.mu.Unlock()
	type answer struct {
		n   Node
		rep Report
		err error
	}
	answers := make(chan answer, len(members))
	var wg sync.WaitGroup
	for _, n := range members {
		if n.ID == self {
			continue
		}
		wg.Add(1)
		go func(n Node) {
			defer wg.Done()
			c, cancel := context.WithTimeout(ctx, max(interval, time.Second))
			defer cancel()
			rep, err := m.transport.Heartbeat(c, n, mine)
			answers <- answer{n, rep, err}
		}(n)
	}
	witnessOK := false
	if cfg.Witness != "" && clustered {
		c, cancel := context.WithTimeout(ctx, max(interval, time.Second))
		witnessOK = m.dial(c, cfg.Witness) == nil
		cancel()
	}
	wg.Wait()
	close(answers)
	newer := Node{}
	newest := cfg.Version
	m.mu.Lock()
	now := m.now()
	for a := range answers {
		if a.err != nil {
			continue
		}
		m.seen[a.n.ID] = now
		m.reports[a.n.ID] = a.rep
		if a.rep.ConfigVersion > newest {
			newest, newer = a.rep.ConfigVersion, a.n
		}
	}
	m.mu.Unlock()

	// 2. A member with a newer configuration: take it.
	if newer.ID != "" {
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		if peerCfg, err := m.transport.FetchConfig(c, newer); err == nil {
			if ok, err := m.store.Adopt(peerCfg); err == nil && ok {
				cfg = m.store.Config()
			}
		}
		cancel()
	}

	// 3. Votes.
	m.mu.Lock()
	m.quorum = m.countVotes(cfg, members, self, witnessOK, now)
	quorate := m.quorum.Quorate
	coordinator := m.quorum.Coordinator == self
	switch {
	case quorate || !clustered:
		if !m.lostSince.IsZero() {
			m.event("info", "", "", "Quorum regained (%d of %d votes)", m.quorum.Votes, m.quorum.TotalVotes)
		}
		m.lostSince, m.selfFenced = time.Time{}, false
	case m.lostSince.IsZero():
		// Votes only go missing after the failover timeout, so the other side may already
		// be restarting our machines: stop them now.
		m.lostSince = now
		m.event("warn", "", "", "Quorum lost: this host sees %d of %d votes; it stops its protected machines so the others can restart them",
			m.quorum.Votes, m.quorum.TotalVotes)
	}
	m.mu.Unlock()

	switch {
	case domErr == nil:
		m.listErr = ""
	case domErr.Error() == m.listErr:
		m.log.Debug("ha: list machines", "err", domErr)
	default:
		m.listErr = domErr.Error()
		m.log.Warn("ha: list machines", "err", domErr)
	}

	// 4. Local protected machines: start them after a reboot, stop them without quorum,
	// replicate them and keep their autostart off.
	// Wait for the others' replicas first: one of them may have taken a machine over while
	// this host was away, and the replicas on disk do not know that yet.
	if domErr == nil && !m.resumed && (!clustered || quorate && tick > resumeAfterTicks) {
		m.resumeAfterBoot(cfg, doms, self)
		doms, domErr = m.hv.ListDomains()
	}
	local := m.localProtected(cfg, doms, self)
	stopped := true
	if clustered && !quorate {
		stopped = m.selfFence(local)
	}
	if domErr == nil && (tick%5 == 1 || tick == 1) {
		m.replicate(ctx, cfg, local, members, self)
	}

	// 5. Watchdog: armed while protected machines are defined here in a cluster; petted only
	// while this host has quorum (or has stopped them), so a hung daemon, or a host that lost
	// quorum and could not stop its machines, resets before the others restart them.
	m.handleWatchdog(cfg, clustered && len(local) > 0, domErr == nil && (quorate || stopped))

	// 6. Recovery of dead members (coordinator only).
	if clustered && quorate && coordinator {
		m.recoverDead(ctx, cfg, members, self)
	}
	m.mu.Lock()
	for id := range m.recoveries {
		if t, ok := m.seen[id]; ok && now.Sub(t) < interval*2 {
			if r := m.recoveries[id]; r.state != "" {
				m.event("info", m.nameOf(members, id), "", "Host is back (recovery state was %s)", r.state)
			}
			delete(m.recoveries, id)
			delete(m.confirmed, id)
		}
	}
	m.mu.Unlock()
}

// resumeAfterTicks is how many heartbeats a member that just started waits before resuming
// its machines: more than two rounds of replicas from the others (sent every 5 beats).
const resumeAfterTicks = 12

// report is this member's heartbeat.
func (m *Manager) report(cfg Config) Report {
	r := Report{NodeID: m.transport.SelfID(), Time: m.now(), ConfigVersion: cfg.Version,
		WatchdogArmed: m.watchdog.Armed(), WatchdogTimeoutSec: cfg.WatchdogTimeoutSec}
	for _, n := range m.transport.Members() {
		if n.ID == r.NodeID {
			r.Name = n.Name
		}
	}
	m.mu.Lock()
	r.Quorate = m.quorum.Quorate
	m.mu.Unlock()
	if st, err := m.hv.HostStats(); err == nil {
		r.MemTotalKiB, r.MemFreeKiB = st.MemTotalKiB, st.MemTotalKiB-min(st.MemUsedKiB, st.MemTotalKiB)
	}
	return r
}

// countVotes: every member that answered within the timeout is a vote, so is this one and a
// reachable witness. More than half of all votes is quorum. Caller holds m.mu.
func (m *Manager) countVotes(cfg Config, members []Node, self string, witnessOK bool, now time.Time) Quorum {
	timeout := time.Duration(cfg.FailoverTimeoutSec) * time.Second
	q := Quorum{Witness: cfg.Witness, WitnessReachable: witnessOK}
	var online []string
	for _, n := range members {
		last, ok := m.seen[n.ID]
		if !ok {
			// Not heard from since this daemon started: like recoverDead, count from the
			// start, so a restart while the others are slow to answer stops nothing.
			last = m.started
		}
		if n.ID == self || now.Sub(last) <= timeout {
			online = append(online, n.ID)
		}
	}
	if len(members) == 0 {
		online = []string{self}
	}
	q.TotalVotes, q.Votes = max(len(members), 1), len(online)
	if cfg.Witness != "" && len(members) > 1 {
		q.TotalVotes++
		if witnessOK {
			q.Votes++
		}
	}
	q.Required = q.TotalVotes/2 + 1
	q.Quorate = q.Votes >= q.Required
	sort.Strings(online)
	if q.Quorate && len(online) > 0 {
		q.Coordinator = online[0]
	}
	return q
}

// localProtected is the protected machines defined on this member, minus those another
// member has taken over (a higher epoch elsewhere: this copy is stale).
func (m *Manager) localProtected(cfg Config, doms []virt.Domain, self string) []virt.Domain {
	member := map[string]bool{}
	for _, n := range m.transport.Members() {
		member[n.ID] = true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []virt.Domain
	for _, d := range doms {
		p, ok := cfg.VMPolicies[strings.ToLower(d.UUID)]
		if !ok || !p.Enabled || d.Template || !d.Persistent {
			continue
		}
		// Only a current member can have taken it over; an unknown owner is this host under
		// an earlier identity (e.g. before it had TLS) or a member that left.
		if r, ok := m.replicas[strings.ToLower(d.UUID)]; ok && r.Owner != self && member[r.Owner] {
			if d.State == virt.StateRunning || d.State == virt.StatePaused || d.State == virt.StateBlocked {
				// Our stale copy runs while another member owns the machine: split brain.
				if err := m.hv.DomainAction(d.UUID, virt.ActionForceOff); err == nil {
					m.event("error", "", d.Name, "Stopped a stale copy here: %s now runs on %s (it was restarted there while this host was away)", d.Name, r.OwnerName)
				}
			}
			continue
		}
		out = append(out, d)
	}
	return out
}

// selfFence stops this member's protected machines after quorum is lost for the timeout:
// the quorate side will restart them. It reports whether all of them are stopped.
func (m *Manager) selfFence(local []virt.Domain) bool {
	ok := true
	for _, d := range local {
		if d.State != virt.StateRunning && d.State != virt.StatePaused && d.State != virt.StateBlocked {
			continue
		}
		err := m.hv.DomainAction(d.UUID, virt.ActionForceOff)
		m.mu.Lock()
		if err != nil {
			ok = false
			m.event("error", "", d.Name, "Could not stop %s after losing quorum: %v", d.Name, err)
		} else {
			m.event("warn", "", d.Name, "Stopped %s: this host lost quorum, the other members restart it", d.Name)
		}
		m.mu.Unlock()
	}
	m.mu.Lock()
	m.selfFenced = true
	m.mu.Unlock()
	return ok
}

// replicate sends the definitions of this member's protected machines to every member and
// keeps autostart off for them (HA starts them, so a returning host does not start a
// machine that now runs elsewhere).
func (m *Manager) replicate(ctx context.Context, cfg Config, local []virt.Domain, members []Node, self string) {
	selfName := m.nameOf(members, self)
	var reps []Replica
	for _, d := range local {
		x, err := m.hv.DomainXML(d.UUID, true)
		if err != nil {
			continue
		}
		u := strings.ToLower(d.UUID)
		m.mu.Lock()
		epoch := m.replicas[u].Epoch
		m.mu.Unlock()
		r := Replica{UUID: u, Name: d.Name, Owner: self, OwnerName: selfName, Epoch: epoch,
			Running: d.State == virt.StateRunning || d.State == virt.StateBlocked || d.State == virt.StatePaused,
			VCPUs:   d.VCPUs, MemoryKiB: d.MaxMemoryKiB, XML: x, Updated: m.now()}
		reps = append(reps, r)
		if d.Autostart {
			if err := m.hv.SetAutostart(d.UUID, false); err == nil {
				m.mu.Lock()
				m.event("info", "", d.Name, "Turned off autostart of %s: HA starts protected machines", d.Name)
				m.mu.Unlock()
			}
		}
	}
	// A protected machine deleted outside VirtoMir (virsh) must not come back when this host
	// dies: its copies elsewhere learn that it no longer runs here.
	here := map[string]bool{}
	for _, r := range reps {
		here[r.UUID] = true
	}
	m.mu.Lock()
	for u, r := range m.replicas {
		if r.Owner == self && r.Running && !here[u] {
			r.Running, r.Updated = false, m.now()
			reps = append(reps, r)
		}
	}
	m.mu.Unlock()
	m.Accept(reps)
	if len(reps) == 0 {
		return
	}
	for _, n := range members {
		if n.ID == self {
			continue
		}
		go func(n Node) {
			c, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if err := m.transport.PushReplicas(c, n, reps); err != nil {
				m.log.Debug("ha: push replicas", "host", n.Name, "err", err)
			}
		}(n)
	}
}

// Accept stores replicas from a member (or this one), keeping the newest of each machine.
func (m *Manager) Accept(reps []Replica) {
	m.mu.Lock()
	defer m.mu.Unlock()
	changed := false
	for _, r := range reps {
		r.UUID = strings.ToLower(r.UUID)
		if old, ok := m.replicas[r.UUID]; !ok || r.newer(old) {
			m.replicas[r.UUID] = r
			changed = true
		}
	}
	if changed {
		m.persist("replicas.json", m.replicas)
	}
}

// resumeAfterBoot starts, once per daemon start, the protected machines this member owns
// that were running at the last look (they no longer autostart, see replicate).
func (m *Manager) resumeAfterBoot(cfg Config, doms []virt.Domain, self string) {
	m.mu.Lock()
	m.resumed = true
	m.mu.Unlock()
	for _, d := range doms {
		if _, ok := cfg.VMPolicies[strings.ToLower(d.UUID)]; !ok || d.State != virt.StateShutoff {
			continue
		}
		if p := cfg.VMPolicies[strings.ToLower(d.UUID)]; !p.Enabled {
			continue
		}
		id := strings.ToLower(d.UUID)
		m.mu.Lock()
		r, ok := m.replicas[id]
		epoch, ran := m.atBoot[id]
		m.mu.Unlock()
		// Still ours with the epoch it had: nobody restarted it elsewhere meanwhile.
		if !ok || !ran || r.Owner != self || r.Epoch != epoch {
			continue
		}
		err := m.hv.DomainAction(d.UUID, virt.ActionStart)
		m.mu.Lock()
		if err != nil {
			m.event("error", "", d.Name, "Could not start %s after this host came up: %v", d.Name, err)
		} else {
			m.event("info", "", d.Name, "Started %s: it was running before this host went down", d.Name)
		}
		m.mu.Unlock()
	}
}

func (m *Manager) handleWatchdog(cfg Config, needed, healthy bool) {
	want := cfg.Watchdog && needed
	switch {
	case want && !m.watchdog.Armed():
		if err := m.watchdog.Arm(cfg.WatchdogTimeoutSec); err != nil {
			m.mu.Lock()
			if m.wdErr != err.Error() {
				m.event("error", "", "", "Cannot arm the hardware watchdog: %v. Without it, failover of this host needs its BMC or an admin's confirmation.", err)
			}
			m.wdErr = err.Error()
			m.mu.Unlock()
			return
		}
		m.mu.Lock()
		m.wdErr = ""
		m.event("info", "", "", "Hardware watchdog armed (%ds): this host resets itself if it hangs or cannot stop its protected machines", cfg.WatchdogTimeoutSec)
		m.mu.Unlock()
	case !want && m.watchdog.Armed():
		if err := m.watchdog.Disarm(); err == nil {
			m.mu.Lock()
			m.event("info", "", "", "Hardware watchdog disarmed")
			m.mu.Unlock()
		}
		return
	}
	if m.watchdog.Armed() && healthy {
		if err := m.watchdog.Pet(); err != nil {
			m.log.Warn("ha: pet watchdog", "err", err)
		}
	}
}

func (m *Manager) nameOf(members []Node, id string) string {
	for _, n := range members {
		if n.ID == id {
			return n.Name
		}
	}
	return id
}

// ConfirmOff records an admin's word that a member is powered off (manual fencing).
func (m *Manager) ConfirmOff(nodeID, user string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if nodeID == m.transport.SelfID() {
		return errors.New("this is the host you are talking to: it is obviously on")
	}
	cfg := m.store.Config()
	if t, ok := m.seen[nodeID]; ok && m.now().Sub(t) < time.Duration(cfg.FailoverTimeoutSec)*time.Second {
		return errors.New("this host still answers heartbeats; it is not off")
	}
	m.confirmed[nodeID] = m.now()
	m.event("warn", m.nameOf(m.transport.Members(), nodeID), "", "%s confirmed that the host is powered off", user)
	return nil
}

// Fence runs a fence agent by hand (status, off, reboot) from the HA page.
func (m *Manager) Fence(ctx context.Context, nodeID string, action FenceAction, user string) (FenceResult, error) {
	a, ok := m.store.Agent(nodeID)
	if !ok {
		return FenceResult{NodeID: nodeID, Action: action}, errors.New("no fence agent is configured for this host")
	}
	a.NodeID = nodeID
	if action != FenceStatus && nodeID == m.transport.SelfID() {
		return FenceResult{}, errors.New("a host cannot fence itself; do it from another member")
	}
	res, err := m.fencer.Fence(ctx, a, action)
	if action != FenceStatus {
		m.mu.Lock()
		lvl := "warn"
		if err != nil {
			lvl = "error"
		}
		m.event(lvl, m.nameOf(m.transport.Members(), nodeID), "", "%s: fence %s by hand: %s", user, action, res.Message)
		m.mu.Unlock()
	}
	return res, err
}

// Status is the HA page.
func (m *Manager) Status() Status {
	cfg := m.store.Public()
	members := m.transport.Members()
	self := m.transport.SelfID()
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	timeout := time.Duration(cfg.FailoverTimeoutSec) * time.Second
	interval := time.Duration(cfg.HeartbeatIntervalSec) * time.Second
	st := Status{SelfID: self, Clustered: len(members) > 1, Quorum: m.quorum, Config: cfg,
		WatchdogArmed: m.watchdog.Armed(), WatchdogError: m.wdErr, Events: append([]Event{}, m.events...)}
	count := map[string]int{}
	for _, r := range m.replicas {
		if p, ok := cfg.VMPolicies[r.UUID]; ok && p.Enabled && r.Running {
			count[r.Owner]++
		}
	}
	for _, n := range members {
		ns := NodeState{Node: n, Self: n.ID == self, Machines: count[n.ID]}
		if ns.Self {
			ns.Status, ns.LastSeen = "online", now
			if m.mine.NodeID != "" {
				r := m.mine
				ns.Report = &r
			}
		} else {
			t, ok := m.seen[n.ID]
			ns.LastSeen = t
			switch {
			case ok && now.Sub(t) <= interval*3:
				ns.Status = "online"
			case ok && now.Sub(t) <= timeout:
				ns.Status = "suspect"
			default:
				ns.Status = "dead"
			}
			if r, ok := m.reports[n.ID]; ok {
				ns.Report = &r
			}
			if rc, ok := m.recoveries[n.ID]; ok {
				ns.Recovery, ns.Fence = rc.state, rc.fence
			}
		}
		st.Nodes = append(st.Nodes, ns)
	}
	for _, p := range cfg.Policies() {
		ms := MachineState{VMPolicy: p}
		if r, ok := m.replicas[p.UUID]; ok {
			ms.Owner, ms.OwnerName, ms.Running, ms.Epoch, ms.Updated, ms.Known = r.Owner, r.OwnerName, r.Running, r.Epoch, r.Updated, true
		}
		st.Machines = append(st.Machines, ms)
	}
	return st
}

// LocalReport answers a member's heartbeat.
func (m *Manager) LocalReport() Report { return m.report(m.store.Config()) }

// Seen records a heartbeat a member sent us (heartbeats count both ways).
func (m *Manager) Seen(r Report) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seen[r.NodeID] = m.now()
	m.reports[r.NodeID] = r
}
