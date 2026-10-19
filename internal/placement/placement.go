// Package placement provides cluster-wide placement constraints and host labels
// for VirtoMir. It acts as the single source of truth for VM-VM affinity and
// anti-affinity (must/should strength), VM-host rules against host groups, and
// fault-domain host labels.
package placement

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
)

// Kind defines the type of placement constraint.
type Kind string

const (
	// VMAffinity keeps specified VMs collocated on the same physical host.
	VMAffinity Kind = "vm_affinity"
	// VMAntiAffinity prevents specified VMs from running on the same physical host.
	VMAntiAffinity Kind = "vm_anti_affinity"
	// VMHost constrains specified VMs to or away from a group of cluster hosts.
	VMHost Kind = "vm_host"
)

// Strength defines whether a constraint is mandatory (hard) or preferred (soft).
type Strength string

const (
	// Must is a hard requirement that cannot be broken.
	Must Strength = "must"
	// Should is a soft preference that should be satisfied when possible.
	Should Strength = "should"
)

// Rule defines an affinity, anti-affinity, or VM-to-host placement constraint.
type Rule struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Kind     Kind     `json:"kind"`
	Enabled  bool     `json:"enabled"`
	Strength Strength `json:"strength"`
	Domains  []string `json:"domains"`           // VM UUIDs
	Hosts    []string `json:"hosts,omitempty"`   // for vm_host: cluster member IDs
	Exclude  bool     `json:"exclude,omitempty"` // for vm_host: must/should NOT run on Hosts
}

// View represents an immutable snapshot of placement rules and host labels.
type View struct {
	Rules  []Rule                       `json:"rules"`
	Labels map[string]map[string]string `json:"labels"`
}

// ErrInvalid and ErrNotFound classify errors for the API (422 / 404).
var (
	ErrInvalid  = errors.New("invalid")
	ErrNotFound = errors.New("not found")
)

type classed struct {
	class error
	msg   string
}

func (e classed) Error() string        { return e.msg }
func (e classed) Is(target error) bool { return target == e.class }

func invalidf(format string, a ...any) error {
	return classed{ErrInvalid, fmt.Sprintf(format, a...)}
}

func notFoundf(format string, a ...any) error {
	return classed{ErrNotFound, fmt.Sprintf(format, a...)}
}

var (
	ruleIDRE = regexp.MustCompile(`^[0-9a-f]{16}$`)
	labelRE  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)
)

type storeData struct {
	// Version counts the changes; across a cluster the newest version wins (#37).
	Version    int                          `json:"version,omitempty"`
	Rules      []Rule                       `json:"rules"`
	Labels     map[string]map[string]string `json:"labels,omitempty"`
	HostLabels map[string]map[string]string `json:"hostLabels,omitempty"`
}

// Store manages persisted placement rules and host labels.
type Store struct {
	path    string
	mu      sync.RWMutex
	version int
	rules   []Rule
	labels  map[string]map[string]string
}

// Open loads or initializes the placement store from the given JSON file path.
// If the file does not exist, it checks for a legacy drs.json in the same directory
// and imports its rules once.
func Open(path string) (*Store, error) {
	s := &Store{
		path:   path,
		rules:  []Rule{},
		labels: make(map[string]map[string]string),
	}
	if path == "" {
		return s, nil
	}

	raw, err := os.ReadFile(path)
	if err == nil {
		var data storeData
		if err := json.Unmarshal(raw, &data); err != nil {
			return nil, fmt.Errorf("read placement store: %w", err)
		}
		s.version = data.Version
		if data.Rules != nil {
			s.rules = data.Rules
		}
		if data.Labels != nil {
			s.labels = data.Labels
		} else if data.HostLabels != nil {
			s.labels = data.HostLabels
		}
		return s, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("read placement store: %w", err)
	}

	// Absent placement.json: check for legacy drs.json next to it.
	drsPath := filepath.Join(filepath.Dir(path), "drs.json")
	drsRaw, drsErr := os.ReadFile(drsPath)
	if drsErr == nil {
		var legacy struct {
			Rules []struct {
				ID         string   `json:"id"`
				Name       string   `json:"name"`
				Kind       string   `json:"kind"`
				Enabled    bool     `json:"enabled"`
				Mandatory  bool     `json:"mandatory"`
				Domains    []string `json:"domains"`
				TargetHost string   `json:"targetHost"`
			} `json:"rules"`
		}
		if err := json.Unmarshal(drsRaw, &legacy); err == nil && len(legacy.Rules) > 0 {
			for _, r := range legacy.Rules {
				var k Kind
				switch r.Kind {
				case "vm_affinity":
					k = VMAffinity
				case "vm_anti_affinity":
					k = VMAntiAffinity
				case "vm_host_affinity", "vm_host":
					k = VMHost
				default:
					continue
				}
				str := Should
				if r.Mandatory {
					str = Must
				}
				rule := Rule{
					ID:       r.ID,
					Name:     r.Name,
					Kind:     k,
					Enabled:  r.Enabled,
					Strength: str,
					Domains:  r.Domains,
				}
				if k == VMHost && r.TargetHost != "" {
					rule.Hosts = []string{r.TargetHost}
				}
				if !ruleIDRE.MatchString(rule.ID) {
					rule.ID = randomHex(8)
				}
				s.rules = append(s.rules, rule)
			}
			if err := s.saveLocked(); err != nil {
				return nil, fmt.Errorf("save imported placement rules: %w", err)
			}
		}
	}

	return s, nil
}

// New creates or loads the placement store for stateDir.
func New(stateDir string) (*Store, error) {
	return Open(filepath.Join(stateDir, "placement.json"))
}

func (s *Store) saveLocked() error {
	if s.path == "" {
		return nil
	}
	data := storeData{
		Version: s.version,
		Rules:   s.rules,
		Labels:  s.labels,
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(s.path); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Rules returns a copy of all configured placement rules.
func (s *Store) Rules() []Rule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.rules)
}

// AddRule validates and adds a placement rule. If a rule with the same 16-hex ID
// already exists, it is replaced. Returns ErrInvalid (422) if validation fails
// or if the rule introduces an unresolvable hard conflict.
func (s *Store) AddRule(rule Rule) (Rule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if strings.TrimSpace(rule.Name) == "" {
		return rule, invalidf("rule name is required")
	}
	if rule.Kind != VMAffinity && rule.Kind != VMAntiAffinity && rule.Kind != VMHost {
		return rule, invalidf("unsupported rule kind %q", rule.Kind)
	}
	if len(rule.Domains) < 1 {
		return rule, invalidf("at least one VM must be assigned to the rule")
	}
	if rule.Kind == VMAntiAffinity && len(rule.Domains) < 2 {
		return rule, invalidf("anti-affinity rule requires at least two VMs")
	}
	if rule.Kind == VMHost && len(rule.Hosts) < 1 {
		return rule, invalidf("vm-host rule requires at least one host")
	}
	if rule.Strength == "" {
		rule.Strength = Must
	}
	if rule.Strength != Must && rule.Strength != Should {
		return rule, invalidf("unsupported rule strength %q", rule.Strength)
	}
	if rule.ID != "" && !ruleIDRE.MatchString(rule.ID) {
		return rule, invalidf("a rule ID is 16 hex digits")
	}

	if rule.ID == "" {
		rule.ID = randomHex(8)
	}

	// Prepare candidate rules slice to verify Conflicts
	candidateRules := make([]Rule, 0, len(s.rules)+1)
	replaced := false
	for _, r := range s.rules {
		if r.ID == rule.ID {
			candidateRules = append(candidateRules, rule)
			replaced = true
		} else {
			candidateRules = append(candidateRules, r)
		}
	}
	if !replaced {
		candidateRules = append(candidateRules, rule)
	}

	// Reject if rule introduces a hard conflict.
	// Store does not maintain cluster host inventory, so hostCount is passed as 0
	// to avoid false-positive pigeonhole rejections when only a subset of hosts have labels.
	conflicts := Conflicts(candidateRules, 0)
	if len(conflicts) > 0 {
		return rule, invalidf("placement conflict: %s", strings.Join(conflicts, "; "))
	}

	prev := s.rules
	s.rules = candidateRules
	if err := s.commitLocked(); err != nil {
		s.rules = prev
		return rule, err
	}
	return rule, nil
}

// DeleteRule removes a placement rule by ID.
func (s *Store) DeleteRule(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !ruleIDRE.MatchString(id) {
		return invalidf("invalid rule ID %q: must be 16 hex digits", id)
	}

	idx := slices.IndexFunc(s.rules, func(r Rule) bool { return r.ID == id })
	if idx < 0 {
		return notFoundf("rule %q not found", id)
	}
	prev := s.rules
	s.rules = slices.Delete(slices.Clone(s.rules), idx, idx+1)
	if err := s.commitLocked(); err != nil {
		s.rules = prev
		return err
	}
	return nil
}

// SetLabels sets or updates the fault-domain labels for hostID.
func (s *Store) SetLabels(hostID string, labels map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if strings.TrimSpace(hostID) == "" {
		return invalidf("host ID is required")
	}

	clean := make(map[string]string, len(labels))
	for k, v := range labels {
		if !labelRE.MatchString(k) {
			return invalidf("invalid label key %q: must match ^[a-z0-9][a-z0-9._-]{0,31}$", k)
		}
		if !labelRE.MatchString(v) {
			return invalidf("invalid label value %q: must match ^[a-z0-9][a-z0-9._-]{0,31}$", v)
		}
		clean[k] = v
	}

	prev, had := s.labels[hostID]
	if len(clean) == 0 {
		delete(s.labels, hostID)
	} else {
		s.labels[hostID] = clean
	}
	if err := s.commitLocked(); err != nil {
		if had {
			s.labels[hostID] = prev
		} else {
			delete(s.labels, hostID)
		}
		return err
	}
	return nil
}

// commitLocked saves a change as a new version.
func (s *Store) commitLocked() error {
	s.version++
	if err := s.saveLocked(); err != nil {
		s.version--
		return err
	}
	return nil
}

// Shared is what a cluster's members keep the same: the rules and host labels, versioned.
type Shared struct {
	Version int                          `json:"version"`
	Rules   []Rule                       `json:"rules"`
	Labels  map[string]map[string]string `json:"labels"`
}

// Shared returns the rules and labels with their version.
func (s *Store) Shared() Shared {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v := s.viewLocked()
	return Shared{Version: s.version, Rules: v.Rules, Labels: v.Labels}
}

// Adopt takes the rules and labels of another member if they are newer than these
// (a member that was down when they changed catches up, #37).
func (s *Store) Adopt(sh Shared) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sh.Version <= s.version {
		return false, nil
	}
	prevV, prevR, prevL := s.version, s.rules, s.labels
	s.version, s.rules, s.labels = sh.Version, slices.Clone(sh.Rules), sh.Labels
	if s.rules == nil {
		s.rules = []Rule{}
	}
	if s.labels == nil {
		s.labels = map[string]map[string]string{}
	}
	if err := s.saveLocked(); err != nil {
		s.version, s.rules, s.labels = prevV, prevR, prevL
		return false, err
	}
	return true, nil
}

// Labels returns a copy of all host labels (host ID -> labels).
func (s *Store) Labels() map[string]map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make(map[string]map[string]string, len(s.labels))
	for h, m := range s.labels {
		hostCopy := make(map[string]string, len(m))
		for k, v := range m {
			hostCopy[k] = v
		}
		out[h] = hostCopy
	}
	return out
}

// View returns an immutable snapshot view of placement rules and labels.
func (s *Store) View() View {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.viewLocked()
}

func (s *Store) viewLocked() View {
	labelsCopy := make(map[string]map[string]string, len(s.labels))
	for h, m := range s.labels {
		hostCopy := make(map[string]string, len(m))
		for k, v := range m {
			hostCopy[k] = v
		}
		labelsCopy[h] = hostCopy
	}

	return View{
		Rules:  slices.Clone(s.rules),
		Labels: labelsCopy,
	}
}

func randomHex(nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
