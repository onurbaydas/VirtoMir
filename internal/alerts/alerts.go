// Package alerts watches the live snapshots for trouble (host overloaded, a machine crashed,
// a pool filling up, libvirt gone) and notifies people. A condition must hold for a while
// before it fires, so short spikes stay quiet; when it clears, a "resolved" follows.
package alerts

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/onurbaydas/virtomir/internal/atomicfile"
)

type Severity string

const (
	Warning  Severity = "warning"
	Critical Severity = "critical"
)

// Kind is what a rule watches.
type Kind string

const (
	HostCPU     Kind = "host.cpu"     // host CPU % above Threshold
	HostMem     Kind = "host.mem"     // host memory % above Threshold
	VMCPU       Kind = "vm.cpu"       // a machine's CPU % above Threshold (Target: one UUID or all)
	VMCrashed   Kind = "vm.crashed"   // a machine is in the crashed state
	VMDown      Kind = "vm.down"      // a machine set to autostart is not running
	PoolFree    Kind = "pool.free"    // free space of a pool below Threshold %
	LibvirtDown Kind = "libvirt.down" // virtomird cannot reach libvirt
	VMAnomaly   Kind = "vm.anomaly"   // a machine's metric far from its usual (robust z-score, ADR-0066)
	CephHealth  Kind = "ceph.health"  // the Ceph cluster is not HEALTH_OK (critical on HEALTH_ERR, ADR-0073)
	// BackupFailed: a scheduled backup failed (Target: one machine UUID or all). Not from
	// observations: ScheduledBackup raises it, and the schedule's next success resolves it.
	BackupFailed Kind = "backup.failed"
)

var kinds = []Kind{HostCPU, HostMem, VMCPU, VMCrashed, VMDown, PoolFree, LibvirtDown, VMAnomaly, CephHealth, BackupFailed}

// eventKind: alerts of these kinds are raised and resolved by events, not by Observe.
func eventKind(k Kind) bool { return k == BackupFailed }

// hiddenKinds are rules the Alerts page cannot show; Config leaves them out and SetConfig
// keeps the stored ones when they are not sent back. Empty while the page knows every kind.
var hiddenKinds []Kind

type Rule struct {
	ID        string   `json:"id"`
	Kind      Kind     `json:"kind"`
	Enabled   bool     `json:"enabled"`
	Threshold float64  `json:"threshold,omitempty"` // percent, for the threshold kinds
	ForSec    int      `json:"forSec"`              // the condition must hold this long
	Target    string   `json:"target,omitempty"`    // machine UUID or pool name; "" = all
	Severity  Severity `json:"severity"`
}

// Alert is one occurrence: a rule that fired for one subject (the host, a machine, a pool).
type Alert struct {
	ID        string     `json:"id"`
	RuleID    string     `json:"ruleId"`
	Kind      Kind       `json:"kind"`
	Severity  Severity   `json:"severity"`
	Subject   string     `json:"subject"`             // display name
	SubjectID string     `json:"subjectId,omitempty"` // machine UUID or pool name
	Message   string     `json:"message"`
	Value     float64    `json:"value,omitempty"`
	Started   time.Time  `json:"started"`
	Resolved  *time.Time `json:"resolved,omitempty"`
	AckedBy   string     `json:"ackedBy,omitempty"`
	AckedAt   *time.Time `json:"ackedAt,omitempty"`
}

// Config is everything an admin edits on the Alerts page.
type Config struct {
	Rules    []Rule    `json:"rules"`
	Channels []Channel `json:"channels"`
	SMTP     SMTP      `json:"smtp"`
}

// DefaultRules are on out of the box: sensible thresholds, nothing noisy.
func DefaultRules() []Rule {
	return []Rule{
		{ID: "host-cpu", Kind: HostCPU, Enabled: true, Threshold: 90, ForSec: 300, Severity: Warning},
		{ID: "host-mem", Kind: HostMem, Enabled: true, Threshold: 90, ForSec: 300, Severity: Warning},
		{ID: "vm-crashed", Kind: VMCrashed, Enabled: true, Severity: Critical},
		{ID: "vm-down", Kind: VMDown, Enabled: true, ForSec: 120, Severity: Warning},
		{ID: "pool-free", Kind: PoolFree, Enabled: true, Threshold: 10, ForSec: 0, Severity: Warning},
		{ID: "libvirt-down", Kind: LibvirtDown, Enabled: true, ForSec: 60, Severity: Critical},
		{ID: "vm-cpu", Kind: VMCPU, Enabled: false, Threshold: 95, ForSec: 600, Severity: Warning},
		{ID: "vm-anomaly", Kind: VMAnomaly, Enabled: false, Severity: Warning},
		{ID: "ceph-health", Kind: CephHealth, Enabled: true, ForSec: 60, Severity: Warning},
		{ID: "backup-failed", Kind: BackupFailed, Enabled: true, Severity: Warning},
	}
}

func (r Rule) validate() error {
	if !slices.Contains(kinds, r.Kind) {
		return fmt.Errorf("unknown rule kind %q", r.Kind)
	}
	if r.Severity != Warning && r.Severity != Critical {
		return fmt.Errorf("severity must be warning or critical")
	}
	if r.ForSec < 0 || r.ForSec > 7*24*3600 {
		return fmt.Errorf("duration must be between 0 and 7 days")
	}
	switch r.Kind {
	case HostCPU, HostMem, VMCPU, PoolFree:
		if r.Threshold <= 0 || r.Threshold > 100 {
			return fmt.Errorf("threshold must be between 0 and 100 %%")
		}
	}
	return nil
}

// Observation is what the engine looks at once per sample.
type Observation struct {
	Time       time.Time
	Connected  bool
	HostCPU    float64 // %
	HostMem    float64 // %
	Machines   []Machine
	Pools      []Pool // may be nil when not refreshed this round
	PoolsFresh bool
	// Ceph is the health of the Ceph cluster this host manages; nil = not read this round
	// (the last reading stays), or no cluster.
	Ceph *Ceph
}

// Ceph is Ceph's verdict (HEALTH_OK, HEALTH_WARN, HEALTH_ERR) and why.
type Ceph struct {
	Status  string
	Message string
}

type Machine struct {
	UUID, Name string
	State      string
	Autostart  bool
	CPU        float64
	// Anomaly describes a recent metric far from the machine's usual; "" = none.
	Anomaly string
}

type Pool struct {
	Name       string
	Active     bool
	FreePct    float64
	FreeBytes  uint64
	TotalBytes uint64
}

// Keep is how many alerts (active and resolved) are remembered.
const Keep = 500

type pendingKey struct{ rule, subject string }

type Engine struct {
	path   string
	log    *slog.Logger
	notify func(Event) // nil: no notifications (tests)

	mu      sync.Mutex
	cfg     Config
	alerts  []Alert // oldest first
	pending map[pendingKey]time.Time
	active  map[pendingKey]string // → alert ID
	lastObs Observation
}

// Open loads the config (rules, channels) from path, falling back to the defaults.
func Open(path string, log *slog.Logger) (*Engine, error) {
	e := &Engine{path: path, log: log, cfg: Config{Rules: DefaultRules(), Channels: []Channel{}},
		pending: map[pendingKey]time.Time{}, active: map[pendingKey]string{}}
	e.notify = e.send
	if path == "" {
		return e, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return e, nil
	}
	if err != nil {
		return e, err
	}
	var file struct {
		Config
		Alerts []Alert `json:"alerts"`
	}
	if err := json.Unmarshal(b, &file); err != nil {
		// Keep the unreadable file: the next save would otherwise replace it with the defaults.
		return e, atomicfile.Quarantine(path, err)
	}
	e.cfg = file.Config
	if e.cfg.Channels == nil {
		e.cfg.Channels = []Channel{}
	}
	// New built-in rules appear for existing installs too.
	for _, def := range DefaultRules() {
		if !slices.ContainsFunc(e.cfg.Rules, func(r Rule) bool { return r.ID == def.ID }) {
			e.cfg.Rules = append(e.cfg.Rules, def)
		}
	}
	e.alerts = file.Alerts
	// Alerts that were active at shutdown get resolved by the first observations if fine.
	for _, a := range e.alerts {
		if a.Resolved == nil {
			e.active[pendingKey{a.RuleID, a.SubjectID}] = a.ID
		}
	}
	return e, nil
}

func (e *Engine) saveLocked() {
	if e.path == "" {
		return
	}
	file := struct {
		Config
		Alerts []Alert `json:"alerts"`
	}{e.cfg, e.alerts}
	b, err := json.MarshalIndent(file, "", "  ")
	if err == nil {
		err = os.MkdirAll(filepath.Dir(e.path), 0o750)
	}
	if err == nil {
		err = atomicfile.Write(e.path, b, 0o600)
	}
	if err != nil && e.log != nil {
		e.log.Warn("save alerts", "err", err)
	}
}

// Config returns the current configuration with its secrets masked: the SMTP password, and
// everything after the host in webhook URLs (a Slack or Discord webhook URL, or an ntfy topic,
// is itself the credential, and viewers may read this configuration).
func (e *Engine) Config() Config {
	e.mu.Lock()
	defer e.mu.Unlock()
	c := e.cfg
	c.Rules = slices.DeleteFunc(slices.Clone(c.Rules), func(r Rule) bool { return slices.Contains(hiddenKinds, r.Kind) })
	c.Channels = slices.Clone(c.Channels)
	if c.SMTP.Password != "" {
		c.SMTP.Password = maskedSecret
	}
	for i := range c.Channels {
		if c.Channels[i].URL != "" {
			c.Channels[i].URL = maskURL(c.Channels[i].URL)
		}
	}
	return c
}

const maskedSecret = "••••••••"

// maskURL keeps the scheme and host of a webhook URL, so people can tell where it goes, and
// hides the rest.
func maskURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return maskedSecret
	}
	return u.Scheme + "://" + u.Host + "/" + maskedSecret
}

func isMaskedURL(s string) bool { return strings.HasSuffix(s, maskedSecret) }

// unmaskChannel puts the stored URL back into a channel sent back with its URL still masked.
// It fails when there is no stored channel to take it from (e.g. the same settings sent to
// another cluster member that does not have this channel yet).
func unmaskChannel(ch *Channel, stored []Channel) error {
	if !isMaskedURL(ch.URL) {
		return nil
	}
	for _, old := range stored {
		if old.ID != "" && old.ID == ch.ID && maskURL(old.URL) == ch.URL {
			ch.URL = old.URL
			return nil
		}
	}
	return fmt.Errorf("channel %q: enter its webhook URL again (it is hidden, and this host does not have it yet)", ch.Name)
}

// SetConfig replaces the configuration after validating it. A masked SMTP password or webhook
// URL keeps the stored one.
func (e *Engine) SetConfig(c Config) error {
	e.mu.Lock()
	stored := slices.Clone(e.cfg.Channels)
	e.mu.Unlock()
	for i := range c.Channels {
		if err := unmaskChannel(&c.Channels[i], stored); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for i := range c.Rules {
		r := &c.Rules[i]
		if r.ID == "" {
			r.ID = newID()
		}
		if seen[r.ID] {
			return fmt.Errorf("duplicate rule %s", r.ID)
		}
		seen[r.ID] = true
		if err := r.validate(); err != nil {
			return fmt.Errorf("rule %s: %w", r.ID, err)
		}
	}
	for i := range c.Channels {
		ch := &c.Channels[i]
		if ch.ID == "" {
			ch.ID = newID()
		}
		if err := ch.validate(); err != nil {
			return fmt.Errorf("channel %q: %w", ch.Name, err)
		}
	}
	if c.Channels == nil {
		c.Channels = []Channel{}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if c.SMTP.Password == maskedSecret {
		c.SMTP.Password = e.cfg.SMTP.Password
	}
	for _, r := range e.cfg.Rules { // rules Config did not show come back as they were
		if slices.Contains(hiddenKinds, r.Kind) && !slices.ContainsFunc(c.Rules, func(x Rule) bool { return x.ID == r.ID }) {
			c.Rules = append(c.Rules, r)
		}
	}
	e.cfg = c
	// Forget pending conditions of removed or disabled rules; their alerts resolve next round.
	for k := range e.pending {
		if !e.ruleEnabled(k.rule) {
			delete(e.pending, k)
		}
	}
	e.saveLocked()
	return nil
}

func (e *Engine) ruleEnabled(id string) bool {
	for _, r := range e.cfg.Rules {
		if r.ID == id {
			return r.Enabled
		}
	}
	return false
}

// List returns alerts newest first: active ones, then resolved ones.
func (e *Engine) List() []Alert {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Alert, 0, len(e.alerts))
	for i := len(e.alerts) - 1; i >= 0; i-- {
		if e.alerts[i].Resolved == nil {
			out = append(out, e.alerts[i])
		}
	}
	for i := len(e.alerts) - 1; i >= 0; i-- {
		if e.alerts[i].Resolved != nil {
			out = append(out, e.alerts[i])
		}
	}
	return out
}

// Ack marks an active alert as seen by user (it stays active until the condition clears).
func (e *Engine) Ack(id, user string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range e.alerts {
		if e.alerts[i].ID == id {
			now := time.Now().UTC()
			e.alerts[i].AckedBy, e.alerts[i].AckedAt = user, &now
			e.saveLocked()
			return nil
		}
	}
	return fmt.Errorf("no alert %s", id)
}

// condition is one rule evaluated for one subject in one observation.
type condition struct {
	key       pendingKey
	subject   string
	subjectID string
	message   string
	value     float64
	severity  Severity // "" = the rule's
}

// Observe evaluates every enabled rule. Call once per sample.
func (e *Engine) Observe(o Observation) {
	e.mu.Lock()
	if !o.PoolsFresh {
		o.Pools, o.PoolsFresh = e.lastObs.Pools, e.lastObs.PoolsFresh
	}
	if o.Ceph == nil {
		o.Ceph = e.lastObs.Ceph
	}
	e.lastObs = o
	var events []Event
	firingNow := map[pendingKey]bool{}

	for _, r := range e.cfg.Rules {
		if !r.Enabled {
			continue
		}
		for _, c := range evaluate(r, o) {
			firingNow[c.key] = true
			since, ok := e.pending[c.key]
			if !ok {
				since = o.Time
				e.pending[c.key] = since
			}
			if _, active := e.active[c.key]; active || o.Time.Sub(since) < time.Duration(r.ForSec)*time.Second {
				continue
			}
			sev := r.Severity
			if c.severity != "" {
				sev = c.severity
			}
			a := Alert{
				ID: newID(), RuleID: r.ID, Kind: r.Kind, Severity: sev,
				Subject: c.subject, SubjectID: c.subjectID, Message: c.message, Value: c.value, Started: o.Time.UTC(),
			}
			e.alerts = append(e.alerts, a)
			e.active[c.key] = a.ID
			events = append(events, Event{Type: Firing, Alert: a})
		}
	}
	for k, id := range e.active {
		// Without libvirt, machines and pools are unknown: keep their alerts as they are.
		if firingNow[k] || eventKind(e.kindOf(k.rule)) || (!o.Connected && e.kindOf(k.rule) != LibvirtDown) {
			continue
		}
		{
			delete(e.active, k)
			if i := slices.IndexFunc(e.alerts, func(a Alert) bool { return a.ID == id }); i >= 0 {
				now := o.Time.UTC()
				e.alerts[i].Resolved = &now
				events = append(events, Event{Type: ResolvedEvent, Alert: e.alerts[i]})
			}
		}
	}
	for k := range e.pending {
		if !firingNow[k] {
			delete(e.pending, k)
		}
	}
	e.publishLocked(events)
}

// publishLocked stores new and resolved alerts and sends their events. It is called with
// e.mu held and releases it.
func (e *Engine) publishLocked(events []Event) {
	if len(events) > 0 {
		for len(e.alerts) > Keep {
			i := slices.IndexFunc(e.alerts, func(a Alert) bool { return a.Resolved != nil })
			if i < 0 {
				break
			}
			e.alerts = slices.Delete(e.alerts, i, i+1)
		}
		e.saveLocked()
	}
	channels, smtp := slices.Clone(e.cfg.Channels), e.cfg.SMTP
	notify := e.notify
	e.mu.Unlock()

	if notify == nil {
		return
	}
	for _, ev := range events {
		ev.channels, ev.smtp = channels, smtp
		notify(ev)
	}
}

// ScheduledBackup records how a scheduled backup run of a machine ended: a failure raises
// the backup-failed alert for it (once, until resolved), a success resolves it. machine is
// the display name.
func (e *Engine) ScheduledBackup(machineUUID, machine string, runErr error) {
	if e == nil {
		return
	}
	now := time.Now().UTC()
	e.mu.Lock()
	var events []Event
	for _, r := range e.cfg.Rules {
		if r.Kind != BackupFailed {
			continue
		}
		k := pendingKey{r.ID, machineUUID}
		id, active := e.active[k]
		fails := runErr != nil && r.Enabled && (r.Target == "" || strings.EqualFold(r.Target, machineUUID))
		switch {
		case fails && !active:
			a := Alert{
				ID: newID(), RuleID: r.ID, Kind: r.Kind, Severity: r.Severity,
				Subject: machine, SubjectID: machineUUID, Message: "Scheduled backup of " + machine + " failed: " + runErr.Error(), Started: now,
			}
			e.alerts = append(e.alerts, a)
			e.active[k] = a.ID
			events = append(events, Event{Type: Firing, Alert: a})
		case !fails && active:
			delete(e.active, k)
			if i := slices.IndexFunc(e.alerts, func(a Alert) bool { return a.ID == id }); i >= 0 {
				e.alerts[i].Resolved = &now
				events = append(events, Event{Type: ResolvedEvent, Alert: e.alerts[i]})
			}
		}
	}
	e.publishLocked(events)
}

func (e *Engine) kindOf(ruleID string) Kind {
	for _, r := range e.cfg.Rules {
		if r.ID == ruleID {
			return r.Kind
		}
	}
	return ""
}

// evaluate returns the subjects for which rule r's condition holds right now.
func evaluate(r Rule, o Observation) []condition {
	key := func(subject string) pendingKey { return pendingKey{r.ID, subject} }
	var out []condition
	switch r.Kind {
	case LibvirtDown:
		if !o.Connected {
			out = append(out, condition{key: key("libvirt"), subject: "libvirt", message: "VirtoMir cannot reach libvirt on this host"})
		}
	case HostCPU:
		if o.Connected && o.HostCPU > r.Threshold {
			out = append(out, condition{key: key("host"), subject: "Host", value: o.HostCPU,
				message: fmt.Sprintf("Host CPU at %.0f %% (threshold %.0f %%)", o.HostCPU, r.Threshold)})
		}
	case HostMem:
		if o.Connected && o.HostMem > r.Threshold {
			out = append(out, condition{key: key("host"), subject: "Host", value: o.HostMem,
				message: fmt.Sprintf("Host memory at %.0f %% (threshold %.0f %%)", o.HostMem, r.Threshold)})
		}
	case VMCPU, VMCrashed, VMDown, VMAnomaly:
		for _, m := range o.Machines {
			if r.Target != "" && r.Target != m.UUID {
				continue
			}
			switch {
			case r.Kind == VMCPU && (m.State == "running" || m.State == "blocked") && m.CPU > r.Threshold:
				out = append(out, condition{key: key(m.UUID), subject: m.Name, subjectID: m.UUID, value: m.CPU,
					message: fmt.Sprintf("%s CPU at %.0f %% (threshold %.0f %%)", m.Name, m.CPU, r.Threshold)})
			case r.Kind == VMCrashed && m.State == "crashed":
				out = append(out, condition{key: key(m.UUID), subject: m.Name, subjectID: m.UUID, message: m.Name + " crashed"})
			case r.Kind == VMDown && m.Autostart && m.State != "running" && m.State != "blocked" && m.State != "paused":
				out = append(out, condition{key: key(m.UUID), subject: m.Name, subjectID: m.UUID,
					message: m.Name + " should start with the host but is " + m.State})
			case r.Kind == VMAnomaly && m.Anomaly != "":
				out = append(out, condition{key: key(m.UUID), subject: m.Name, subjectID: m.UUID, message: m.Anomaly})
			}
		}
	case PoolFree:
		for _, p := range o.Pools {
			if !p.Active || p.TotalBytes == 0 || (r.Target != "" && r.Target != p.Name) {
				continue
			}
			if p.FreePct < r.Threshold {
				out = append(out, condition{key: key(p.Name), subject: "Pool " + p.Name, subjectID: p.Name, value: p.FreePct,
					message: fmt.Sprintf("Pool %s has %.0f %% free (%s left; threshold %.0f %%)", p.Name, p.FreePct, gib(p.FreeBytes), r.Threshold)})
			}
		}
	case CephHealth:
		if o.Ceph != nil && o.Ceph.Status != "" && o.Ceph.Status != "HEALTH_OK" {
			// HEALTH_ERR is its own (critical) alert, so a warning that gets worse notifies again.
			c := condition{key: key("ceph"), subject: "Ceph cluster", subjectID: "ceph",
				message: "Ceph health is " + o.Ceph.Status}
			if o.Ceph.Message != "" {
				c.message += ": " + o.Ceph.Message
			}
			if o.Ceph.Status == "HEALTH_ERR" {
				c.key, c.severity = key("ceph-err"), Critical
			}
			out = append(out, c)
		}
	}
	return out
}

func gib(b uint64) string { return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30)) }

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
