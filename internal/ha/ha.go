// Package ha keeps protected machines running when a cluster member dies (ADR-0031).
//
// Every member runs the same Manager. Members exchange heartbeats; a partition with more
// than half of the votes (members plus an optional witness) has quorum. The member with the
// lowest ID in the quorate partition coordinates. When a member stops answering for longer
// than the failover timeout and ran protected machines, the coordinator makes sure it is
// really off (fencing: its BMC over IPMI/Redfish, its own hardware watchdog, or an admin's
// confirmation) and only then starts those machines elsewhere from the definitions every
// member keeps of them. A member that loses quorum stops its protected machines itself.
//
// Restarting a machine elsewhere needs its disks on storage every member reaches (NFS,
// Ceph RBD, iSCSI); a machine on a local disk cannot be recovered and fails with libvirt's
// reason.
package ha

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Config is the cluster-wide HA configuration. Every member keeps a copy; the highest
// Version wins, so a change made on any member reaches all of them.
type Config struct {
	Version int `json:"version"`
	// AutoFailover lets the coordinator restart machines of a dead member by itself. Off,
	// HA only reports (and a member without quorum still stops its protected machines).
	AutoFailover         bool `json:"autoFailover"`
	HeartbeatIntervalSec int  `json:"heartbeatIntervalSec"`
	// FailoverTimeoutSec is how long a member may be silent before it counts as dead.
	FailoverTimeoutSec int `json:"failoverTimeoutSec"`
	// Witness is "host:port" of a third machine that only has to accept TCP connections
	// (e.g. its SSH port). It is one extra vote, so two members can keep quorum when one dies.
	Witness string `json:"witness,omitempty"`
	// Watchdog arms the hardware watchdog (/dev/watchdog) on members that run protected
	// machines: if virtomird hangs or loses quorum and cannot stop them, the host resets
	// itself. This is what makes failover safe without a BMC.
	Watchdog           bool `json:"watchdog"`
	WatchdogTimeoutSec int  `json:"watchdogTimeoutSec"`

	FenceAgents map[string]FenceAgent `json:"fenceAgents"` // by member ID
	VMPolicies  map[string]VMPolicy   `json:"vmPolicies"`  // by machine UUID
}

// FenceAgent powers a member off through its management controller.
type FenceAgent struct {
	NodeID string `json:"nodeId"`
	// Driver: "ipmi" (ipmitool lanplus), "redfish" (HTTPS API) or "manual" (an admin
	// confirms that the host is off).
	Driver   string `json:"driver"`
	Endpoint string `json:"endpoint"` // BMC address: host, host:port or https://host
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	// InsecureTLS accepts a Redfish BMC's self-signed certificate.
	InsecureTLS bool `json:"insecureTls,omitempty"`
}

// VMPolicy says whether and how a machine is protected.
type VMPolicy struct {
	UUID    string `json:"uuid"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	// Priority orders restarts: critical, high, normal, low.
	Priority string `json:"priority"`
	// RestartDelaySec waits after the previous machine before starting this one.
	RestartDelaySec int `json:"restartDelaySec"`
	// MaxRetries is how many members are tried before giving up.
	MaxRetries int `json:"maxRetries"`
	// TargetNode is the preferred member (ID) to restart on; others are used if it is down.
	TargetNode string `json:"targetNode,omitempty"`
}

var priorityWeight = map[string]int{"critical": 1, "high": 2, "normal": 3, "low": 4}

// Redacted marks a password that is set but not shown.
const Redacted = "********"

const (
	defaultHeartbeat = 2
	defaultTimeout   = 30
	defaultWatchdog  = 60
)

// Store persists the configuration in -state-dir/ha.json.
type Store struct {
	path string
	mu   sync.Mutex
	cfg  Config
}

func Open(path string) (*Store, error) {
	s := &Store{path: path}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		s.cfg.normalize()
		return s, s.save()
	case err != nil:
		return nil, err
	}
	if err := json.Unmarshal(raw, &s.cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	s.cfg.normalize()
	return s, nil
}

func (c *Config) normalize() {
	if c.HeartbeatIntervalSec <= 0 {
		c.HeartbeatIntervalSec = defaultHeartbeat
	}
	if c.FailoverTimeoutSec <= 0 {
		c.FailoverTimeoutSec = defaultTimeout
	}
	if c.WatchdogTimeoutSec <= 0 {
		c.WatchdogTimeoutSec = defaultWatchdog
	}
	if c.FenceAgents == nil {
		c.FenceAgents = map[string]FenceAgent{}
	}
	if c.VMPolicies == nil {
		c.VMPolicies = map[string]VMPolicy{}
	}
}

func (s *Store) save() error {
	b, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o750); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil { // holds BMC passwords
		return err
	}
	return os.Rename(tmp, s.path)
}

// Config returns a full copy (passwords included: for fencing and for peers).
func (s *Store) Config() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.clone()
}

func (c Config) clone() Config {
	out := c
	out.FenceAgents = make(map[string]FenceAgent, len(c.FenceAgents))
	for k, v := range c.FenceAgents {
		out.FenceAgents[k] = v
	}
	out.VMPolicies = make(map[string]VMPolicy, len(c.VMPolicies))
	for k, v := range c.VMPolicies {
		out.VMPolicies[k] = v
	}
	return out
}

// Public is the configuration as shown to people: passwords replaced by Redacted.
func (s *Store) Public() Config {
	c := s.Config()
	for k, a := range c.FenceAgents {
		if a.Password != "" {
			a.Password = Redacted
		}
		c.FenceAgents[k] = a
	}
	return c
}

// Agent is the fence agent configured for a member.
func (s *Store) Agent(nodeID string) (FenceAgent, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.cfg.FenceAgents[nodeID]
	return a, ok && a.Driver != ""
}

// Policy is a machine's HA policy (ok=false: not protected).
func (s *Store) Policy(uuid string) (VMPolicy, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.cfg.VMPolicies[strings.ToLower(uuid)]
	return p, ok && p.Enabled
}

// Settings is what an admin changes on the HA page (policies have their own calls).
type Settings struct {
	AutoFailover         bool                  `json:"autoFailover"`
	HeartbeatIntervalSec int                   `json:"heartbeatIntervalSec"`
	FailoverTimeoutSec   int                   `json:"failoverTimeoutSec"`
	Witness              string                `json:"witness"`
	Watchdog             bool                  `json:"watchdog"`
	WatchdogTimeoutSec   int                   `json:"watchdogTimeoutSec"`
	FenceAgents          map[string]FenceAgent `json:"fenceAgents"`
}

// Validate checks settings before they are stored.
func (st Settings) Validate() error {
	if st.HeartbeatIntervalSec != 0 && (st.HeartbeatIntervalSec < 1 || st.HeartbeatIntervalSec > 30) {
		return errors.New("the heartbeat interval must be 1–30 seconds")
	}
	if st.FailoverTimeoutSec != 0 && st.FailoverTimeoutSec < 10 {
		return errors.New("the failover timeout must be at least 10 seconds (shorter ones fail over on a busy network)")
	}
	if st.HeartbeatIntervalSec > 0 && st.FailoverTimeoutSec > 0 && st.FailoverTimeoutSec < 3*st.HeartbeatIntervalSec {
		return errors.New("the failover timeout must be at least three heartbeats")
	}
	if st.WatchdogTimeoutSec != 0 && (st.WatchdogTimeoutSec < 10 || st.WatchdogTimeoutSec > 600) {
		return errors.New("the watchdog timeout must be 10–600 seconds")
	}
	if w := strings.TrimSpace(st.Witness); w != "" && !strings.Contains(w, ":") {
		return errors.New(`the witness must be "host:port" (any TCP port it answers on, e.g. 22)`)
	}
	for id, a := range st.FenceAgents {
		switch a.Driver {
		case "", "manual":
		case "ipmi", "redfish":
			if strings.TrimSpace(a.Endpoint) == "" {
				return fmt.Errorf("the fence agent of %s needs the BMC address", id)
			}
		default:
			return fmt.Errorf("unknown fence driver %q (ipmi, redfish or manual)", a.Driver)
		}
	}
	return nil
}

// UpdateSettings stores new settings as a new version. A password sent back as Redacted or
// empty keeps the stored one.
func (s *Store) UpdateSettings(st Settings) (Config, error) {
	if err := st.Validate(); err != nil {
		return Config{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.cfg.clone()
	next.AutoFailover = st.AutoFailover
	next.HeartbeatIntervalSec = st.HeartbeatIntervalSec
	next.FailoverTimeoutSec = st.FailoverTimeoutSec
	next.Witness = strings.TrimSpace(st.Witness)
	next.Watchdog = st.Watchdog
	next.WatchdogTimeoutSec = st.WatchdogTimeoutSec
	agents := map[string]FenceAgent{}
	for id, a := range st.FenceAgents {
		if a.Driver == "" {
			continue
		}
		a.NodeID = id
		a.Endpoint = strings.TrimSpace(a.Endpoint)
		if a.Password == Redacted || a.Password == "" {
			a.Password = s.cfg.FenceAgents[id].Password
		}
		agents[id] = a
	}
	next.FenceAgents = agents
	return s.commit(next)
}

// SetPolicy protects (or changes the protection of) a machine.
func (s *Store) SetPolicy(p VMPolicy) (Config, error) {
	p.UUID = strings.ToLower(strings.TrimSpace(p.UUID))
	if p.UUID == "" {
		return Config{}, errors.New("the machine UUID is missing")
	}
	if p.Priority == "" {
		p.Priority = "normal"
	}
	if _, ok := priorityWeight[p.Priority]; !ok {
		return Config{}, fmt.Errorf("unknown priority %q (critical, high, normal or low)", p.Priority)
	}
	if p.MaxRetries <= 0 {
		p.MaxRetries = 2
	}
	if p.RestartDelaySec < 0 || p.RestartDelaySec > 3600 {
		return Config{}, errors.New("the restart delay must be 0–3600 seconds")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.cfg.clone()
	next.VMPolicies[p.UUID] = p
	return s.commit(next)
}

// DeletePolicy stops protecting a machine.
func (s *Store) DeletePolicy(uuid string) (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.cfg.clone()
	delete(next.VMPolicies, strings.ToLower(uuid))
	return s.commit(next)
}

// Adopt installs a configuration from a peer if it is newer than ours.
func (s *Store) Adopt(c Config) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.Version <= s.cfg.Version {
		return false, nil
	}
	c.normalize()
	prev := s.cfg
	s.cfg = c.clone()
	if err := s.save(); err != nil {
		s.cfg = prev
		return false, err
	}
	return true, nil
}

func (s *Store) commit(next Config) (Config, error) {
	next.Version = s.cfg.Version + 1
	next.normalize()
	prev := s.cfg
	s.cfg = next
	if err := s.save(); err != nil {
		s.cfg = prev
		return Config{}, err
	}
	return next.clone(), nil
}

// Policies lists protected machines, most important first.
func (c Config) Policies() []VMPolicy {
	out := make([]VMPolicy, 0, len(c.VMPolicies))
	for _, p := range c.VMPolicies {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if wi, wj := priorityWeight[out[i].Priority], priorityWeight[out[j].Priority]; wi != wj {
			return wi < wj
		}
		return out[i].Name < out[j].Name
	})
	return out
}
