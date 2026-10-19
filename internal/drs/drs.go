// Package drs provides Distributed Resource Scheduling (DRS) and workload rebalancing
// across cluster nodes in VirtoMir. It monitors host CPU, RAM, and I/O pressure,
// calculates standard deviation of cluster utilization (Cluster Balance Score),
// evaluates VM-VM and VM-Host Affinity / Anti-Affinity rules, and generates actionable
// rebalancing recommendations with priority ratings.
package drs

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/onurbaydas/virtomir/internal/placement"
)

// DRSMode represents the operational automation level of DRS.
type DRSMode string

const (
	// ModeManual generates recommendations and asks admins for 1-click execution.
	ModeManual DRSMode = "manual"
	// ModePartiallyAutomated automatically places new VMs on optimal hosts and recommends rebalances.
	ModePartiallyAutomated DRSMode = "partially_automated"
	// ModeFullyAutomated rebalances cluster workloads automatically when thresholds are exceeded.
	ModeFullyAutomated DRSMode = "fully_automated"
)

// RuleKind defines the type of placement affinity constraint.
type RuleKind string

const (
	// AntiAffinityVMVM prevents specified VMs from running on the same physical host.
	AntiAffinityVMVM RuleKind = "vm_anti_affinity"
	// AffinityVMVM keeps specified VMs collocated on the same physical host.
	AffinityVMVM RuleKind = "vm_affinity"
	// AffinityVMHost pins specified VMs to a preferred or mandatory target host.
	AffinityVMHost RuleKind = "vm_host_affinity"
)

// AffinityRule defines an affinity or anti-affinity constraint.
type AffinityRule struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Kind       RuleKind `json:"kind"`
	Enabled    bool     `json:"enabled"`
	Mandatory  bool     `json:"mandatory"`            // hard constraint vs soft preference
	Domains    []string `json:"domains"`              // VM UUIDs
	TargetHost string   `json:"targetHost,omitempty"` // for vm_host_affinity
}

// HostMetrics represents the current utilization metrics of a cluster node.
type HostMetrics struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Reachable bool    `json:"reachable"`
	CPUPct    float64 `json:"cpuPct"` // 0.0 to 100.0
	MemPct    float64 `json:"memPct"` // 0.0 to 100.0
	MemUsedB  uint64  `json:"memUsedB"`
	MemTotalB uint64  `json:"memTotalB"`
	VMCount   int     `json:"vmCount"`
}

// DomainPlacement represents a VM's current host assignment and load.
type DomainPlacement struct {
	UUID     string  `json:"uuid"`
	Name     string  `json:"name"`
	HostID   string  `json:"hostId"`
	State    string  `json:"state"` // running, shut_off, etc.
	CPUPct   float64 `json:"cpuPct"`
	MemUsedB uint64  `json:"memUsedB"`
}

// Recommendation represents a DRS proposed live migration action.
type Recommendation struct {
	ID           string    `json:"id"`
	Priority     int       `json:"priority"` // 1 (lowest) to 5 (critical)
	DomainUUID   string    `json:"domainUuid"`
	DomainName   string    `json:"domainName"`
	SourceHostID string    `json:"sourceHostId"`
	SourceName   string    `json:"sourceName"`
	TargetHostID string    `json:"targetHostId"`
	TargetName   string    `json:"targetName"`
	Reason       string    `json:"reason"`
	ExpectedGain float64   `json:"expectedGain"` // estimated improvement in balance score (0-100)
	CreatedAt    time.Time `json:"createdAt"`
}

// Status represents the complete DRS state exposed to the UI and API.
type Status struct {
	Enabled            bool             `json:"enabled"`
	Mode               DRSMode          `json:"mode"`
	BalanceScore       float64          `json:"balanceScore"` // 0.0 to 100.0 (100% = perfectly balanced)
	CPUStdDev          float64          `json:"cpuStdDev"`
	MemStdDev          float64          `json:"memStdDev"`
	ThresholdStdDev    float64          `json:"thresholdStdDev"` // default 15.0%
	Hosts              []HostMetrics    `json:"hosts"`
	Recommendations    []Recommendation `json:"recommendations"`
	Rules              []AffinityRule   `json:"rules"`
	LastEvaluationTime time.Time        `json:"lastEvaluationTime"`
}

// Config stores persisted DRS configuration.
type Config struct {
	// Version counts the changes; across a cluster the newest version wins (#37).
	Version         int            `json:"version"`
	Enabled         bool           `json:"enabled"`
	Mode            DRSMode        `json:"mode"`
	ThresholdStdDev float64        `json:"thresholdStdDev"`
	Rules           []AffinityRule `json:"rules"`
}

// Engine manages DRS state, evaluations, and rules.
type Engine struct {
	path string
	mu   sync.RWMutex
	cfg  Config
}

// New creates or loads the DRS engine.
func New(stateDir string) (*Engine, error) {
	path := filepath.Join(stateDir, "drs.json")
	e := &Engine{
		path: path,
		cfg: Config{
			Enabled:         true,
			Mode:            ModeManual,
			ThresholdStdDev: 15.0,
			Rules:           []AffinityRule{},
		},
	}

	raw, err := os.ReadFile(path)
	if err == nil {
		var loaded Config
		if json.Unmarshal(raw, &loaded) == nil {
			if loaded.Mode == "" {
				loaded.Mode = ModeManual
			}
			if loaded.ThresholdStdDev <= 0 {
				loaded.ThresholdStdDev = 15.0
			}
			e.cfg = loaded
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("read drs config: %w", err)
	}

	return e, nil
}

func (e *Engine) saveLocked() error {
	if e.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(e.cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := e.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, e.path)
}

// GetConfig returns current DRS configuration.
func (e *Engine) GetConfig() Config {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.cfg
}

// SetConfig updates DRS mode and thresholds.
func (e *Engine) SetConfig(cfg Config) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if cfg.ThresholdStdDev <= 0 {
		cfg.ThresholdStdDev = 15.0
	}
	if cfg.Mode == "" {
		cfg.Mode = ModeManual
	}
	if cfg.Mode != ModeManual && cfg.Mode != ModePartiallyAutomated && cfg.Mode != ModeFullyAutomated {
		return invalidf("mode is manual, partially_automated or fully_automated")
	}
	prev := e.cfg
	e.cfg.Enabled = cfg.Enabled
	e.cfg.Mode = cfg.Mode
	e.cfg.ThresholdStdDev = cfg.ThresholdStdDev
	e.cfg.Version++
	if err := e.saveLocked(); err != nil {
		e.cfg = prev
		return err
	}
	return nil
}

// Adopt takes the settings of another member if they are newer than these (a member that
// was down when they changed catches up, #37). Rules live in the placement store.
func (e *Engine) Adopt(cfg Config) (bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if cfg.Version <= e.cfg.Version {
		return false, nil
	}
	if cfg.Mode != ModeManual && cfg.Mode != ModePartiallyAutomated && cfg.Mode != ModeFullyAutomated {
		return false, invalidf("mode is manual, partially_automated or fully_automated")
	}
	prev := e.cfg
	e.cfg.Version, e.cfg.Enabled, e.cfg.Mode, e.cfg.ThresholdStdDev = cfg.Version, cfg.Enabled, cfg.Mode, cfg.ThresholdStdDev
	if e.cfg.ThresholdStdDev <= 0 {
		e.cfg.ThresholdStdDev = 15.0
	}
	if err := e.saveLocked(); err != nil {
		e.cfg = prev
		return false, err
	}
	return true, nil
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

func invalidf(format string, a ...any) error { return classed{ErrInvalid, fmt.Sprintf(format, a...)} }

var ruleIDRE = regexp.MustCompile(`^[0-9a-f]{16}$`)

// AddRule adds an affinity or anti-affinity rule. A rule with an ID (as copied from another
// member, so all members share one set of rules) replaces the rule with that ID.
func (e *Engine) AddRule(rule AffinityRule) (AffinityRule, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if strings.TrimSpace(rule.Name) == "" {
		return rule, invalidf("rule name is required")
	}
	if rule.Kind != AntiAffinityVMVM && rule.Kind != AffinityVMVM && rule.Kind != AffinityVMHost {
		return rule, invalidf("unsupported rule kind %q", rule.Kind)
	}
	if len(rule.Domains) < 1 {
		return rule, invalidf("at least one VM must be assigned to the rule")
	}
	if rule.Kind == AntiAffinityVMVM && len(rule.Domains) < 2 {
		return rule, invalidf("anti-affinity rule requires at least two VMs")
	}
	if rule.Kind == AffinityVMHost && strings.TrimSpace(rule.TargetHost) == "" {
		return rule, invalidf("vm-host affinity rule requires a target host")
	}
	if rule.ID != "" && !ruleIDRE.MatchString(rule.ID) {
		return rule, invalidf("a rule ID is 16 hex digits")
	}

	if rule.ID == "" {
		rule.ID = randomHex(8)
	}
	e.cfg.Rules = slices.DeleteFunc(e.cfg.Rules, func(r AffinityRule) bool { return r.ID == rule.ID })
	rule.Enabled = true
	e.cfg.Rules = append(e.cfg.Rules, rule)
	if err := e.saveLocked(); err != nil {
		return rule, err
	}
	return rule, nil
}

// DeleteRule removes an affinity rule by ID.
func (e *Engine) DeleteRule(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	idx := slices.IndexFunc(e.cfg.Rules, func(r AffinityRule) bool { return r.ID == id })
	if idx < 0 {
		return classed{ErrNotFound, fmt.Sprintf("rule %q not found", id)}
	}
	e.cfg.Rules = slices.Delete(e.cfg.Rules, idx, idx+1)
	return e.saveLocked()
}

// Evaluate analyzes the cluster state, verifies rules, calculates balance score and generates recommendations.
func (e *Engine) Evaluate(ctx context.Context, hosts []HostMetrics, domains []DomainPlacement, view placement.View) Status {
	e.mu.RLock()
	cfg := e.cfg
	rules := append([]AffinityRule(nil), cfg.Rules...)
	e.mu.RUnlock()

	status := Status{
		Enabled:            cfg.Enabled,
		Mode:               cfg.Mode,
		ThresholdStdDev:    cfg.ThresholdStdDev,
		Hosts:              hosts,
		Rules:              rules,
		Recommendations:    []Recommendation{},
		LastEvaluationTime: time.Now(),
	}

	reachableHosts := make([]HostMetrics, 0, len(hosts))
	for _, h := range hosts {
		if h.Reachable {
			reachableHosts = append(reachableHosts, h)
		}
	}

	if len(reachableHosts) < 2 {
		// Standalone or single reachable host has a static 100% score (no rebalance possible)
		status.BalanceScore = 100.0
		return status
	}

	// 1. Calculate Mean and Standard Deviation for CPU & Memory
	var totalCPU, totalMem float64
	for _, h := range reachableHosts {
		totalCPU += h.CPUPct
		totalMem += h.MemPct
	}
	meanCPU := totalCPU / float64(len(reachableHosts))
	meanMem := totalMem / float64(len(reachableHosts))

	var varCPU, varMem float64
	for _, h := range reachableHosts {
		varCPU += math.Pow(h.CPUPct-meanCPU, 2)
		varMem += math.Pow(h.MemPct-meanMem, 2)
	}
	cpuStdDev := math.Sqrt(varCPU / float64(len(reachableHosts)))
	memStdDev := math.Sqrt(varMem / float64(len(reachableHosts)))

	status.CPUStdDev = math.Round(cpuStdDev*10) / 10
	status.MemStdDev = math.Round(memStdDev*10) / 10

	// Combined imbalance: max of CPU and Memory skew
	maxDev := math.Max(cpuStdDev, memStdDev)
	// Balance Score: 100 - (maxDev * 2) capped in [0, 100]
	balance := math.Max(0.0, math.Min(100.0, 100.0-(maxDev*2.0)))
	status.BalanceScore = math.Round(balance*10) / 10

	if !cfg.Enabled {
		return status
	}

	// Convert domains to []placement.VMRef (Running = State=="running")
	vms := VMRefs(domains)

	hostByID := make(map[string]HostMetrics, len(hosts))
	for _, h := range hosts {
		hostByID[h.ID] = h
	}

	domainByID := make(map[string]DomainPlacement, len(domains))
	for _, d := range domains {
		domainByID[d.UUID] = d
	}

	// Map of hostID -> []DomainPlacement
	vmsOnHost := make(map[string][]DomainPlacement)
	for _, d := range domains {
		if d.State == "running" {
			vmsOnHost[d.HostID] = append(vmsOnHost[d.HostID], d)
		}
	}

	// 2. Check rule violations first (Top priority)
	seenRecIDs := make(map[string]bool)
	violations := placement.Violations(view.Rules, vms)
	for _, v := range violations {
		culprit, targetHostID, ok := selectCulpritAndTarget(v, vms, domainByID, hostByID, reachableHosts, view.Rules)
		if !ok {
			continue
		}

		recID := recommendationID(culprit.UUID, culprit.HostID, targetHostID)
		if seenRecIDs[recID] {
			continue
		}
		seenRecIDs[recID] = true

		priority := 2
		if v.Hard {
			priority = 5
		}

		sourceHost := hostByID[culprit.HostID]
		targetHost := hostByID[targetHostID]

		status.Recommendations = append(status.Recommendations, Recommendation{
			ID:           recID,
			Priority:     priority,
			DomainUUID:   culprit.UUID,
			DomainName:   culprit.Name,
			SourceHostID: culprit.HostID,
			SourceName:   sourceHost.Name,
			TargetHostID: targetHostID,
			TargetName:   targetHost.Name,
			Reason:       v.Message,
			ExpectedGain: 15.0,
			CreatedAt:    time.Now(),
		})
	}

	// 3. Rebalance based on load skew if standard deviation exceeds threshold
	if maxDev > cfg.ThresholdStdDev {
		// Identify most overloaded host and most idle host
		var mostLoaded, leastLoaded HostMetrics
		for i, h := range reachableHosts {
			if i == 0 || (h.CPUPct+h.MemPct) > (mostLoaded.CPUPct+mostLoaded.MemPct) {
				mostLoaded = h
			}
			if i == 0 || (h.CPUPct+h.MemPct) < (leastLoaded.CPUPct+leastLoaded.MemPct) {
				leastLoaded = h
			}
		}

		if mostLoaded.ID != leastLoaded.ID && len(vmsOnHost[mostLoaded.ID]) > 0 {
			// Find a suitable VM to migrate from mostLoaded to leastLoaded
			candidates := vmsOnHost[mostLoaded.ID]
			// Sort candidates by moderate resource footprint (not too large, not zero)
			slices.SortFunc(candidates, func(a, b DomainPlacement) int {
				return int(b.MemUsedB - a.MemUsedB)
			})

			for _, candidate := range candidates {
				// Verify candidate does not break mandatory rules on target
				hard, _ := placement.Check(view.Rules, vms, candidate.UUID, leastLoaded.ID)
				if len(hard) == 0 {
					priority := 3
					if maxDev > 25.0 {
						priority = 4
					}
					gain := math.Min(25.0, maxDev*0.7)

					status.Recommendations = append(status.Recommendations, Recommendation{
						ID:           recommendationID(candidate.UUID, mostLoaded.ID, leastLoaded.ID),
						Priority:     priority,
						DomainUUID:   candidate.UUID,
						DomainName:   candidate.Name,
						SourceHostID: mostLoaded.ID,
						SourceName:   mostLoaded.Name,
						TargetHostID: leastLoaded.ID,
						TargetName:   leastLoaded.Name,
						Reason: fmt.Sprintf("Cluster skew: %s is overloaded (CPU: %.1f%%, RAM: %.1f%%) while %s is underutilized",
							mostLoaded.Name, mostLoaded.CPUPct, mostLoaded.MemPct, leastLoaded.Name),
						ExpectedGain: math.Round(gain*10) / 10,
						CreatedAt:    time.Now(),
					})
					break // Propose top recommendation per evaluation cycle
				}
			}
		}
	}

	return status
}

// VMRefs converts DomainPlacement slice into placement.VMRef slice.
func VMRefs(domains []DomainPlacement) []placement.VMRef {
	vms := make([]placement.VMRef, len(domains))
	for i, d := range domains {
		vms[i] = placement.VMRef{
			UUID:    d.UUID,
			Name:    d.Name,
			HostID:  d.HostID,
			Running: d.State == "running",
		}
	}
	return vms
}

func selectCulpritAndTarget(
	v placement.Violation,
	vms []placement.VMRef,
	domainByID map[string]DomainPlacement,
	hostByID map[string]HostMetrics,
	reachableHosts []HostMetrics,
	rules []placement.Rule,
) (culprit DomainPlacement, targetHostID string, ok bool) {
	switch v.Kind {
	case placement.VMHost:
		if len(v.DomainUUIDs) == 0 {
			return DomainPlacement{}, "", false
		}
		c, exists := domainByID[v.DomainUUIDs[0]]
		if !exists || c.State != "running" || c.HostID == "" {
			return DomainPlacement{}, "", false
		}
		target, found := pickBestHost(c, vms, hostByID, reachableHosts, rules)
		if !found {
			return DomainPlacement{}, "", false
		}
		return c, target, true

	case placement.VMAntiAffinity:
		// Multiple VMs of this rule are running on v.HostID.
		// Try moving one VM away from v.HostID.
		// Prefer subsequent VMs first (v.DomainUUIDs[1:]), then v.DomainUUIDs[0] for stable 2-VM behavior.
		candidateUUIDs := make([]string, 0, len(v.DomainUUIDs))
		if len(v.DomainUUIDs) > 1 {
			candidateUUIDs = append(candidateUUIDs, v.DomainUUIDs[1:]...)
		}
		if len(v.DomainUUIDs) > 0 {
			candidateUUIDs = append(candidateUUIDs, v.DomainUUIDs[0])
		}

		for _, uid := range candidateUUIDs {
			c, exists := domainByID[uid]
			if !exists || c.State != "running" || c.HostID == "" {
				continue
			}
			target, found := pickBestHost(c, vms, hostByID, reachableHosts, rules)
			if found {
				return c, target, true
			}
		}
		return DomainPlacement{}, "", false

	case placement.VMAffinity:
		// Rule VMs are spread across multiple hosts.
		// Count VMs per host among running domains of this rule.
		hostCounts := make(map[string]int)
		for _, uid := range v.DomainUUIDs {
			if d, exists := domainByID[uid]; exists && d.State == "running" && d.HostID != "" {
				hostCounts[d.HostID]++
			}
		}
		if len(hostCounts) <= 1 {
			return DomainPlacement{}, "", false
		}

		// Majority host has the most VMs of this rule.
		// Tie-breaker: host of the first domain in v.DomainUUIDs.
		var majorityHost string
		maxCount := -1
		for _, uid := range v.DomainUUIDs {
			if d, exists := domainByID[uid]; exists && d.State == "running" && d.HostID != "" {
				cnt := hostCounts[d.HostID]
				if cnt > maxCount {
					maxCount = cnt
					majorityHost = d.HostID
				}
			}
		}

		// Candidates to move: minority VMs first (not on majority host), then majority VMs.
		var minorityVMs []DomainPlacement
		var majorityVMs []DomainPlacement
		for _, uid := range v.DomainUUIDs {
			if d, exists := domainByID[uid]; exists && d.State == "running" && d.HostID != "" {
				if d.HostID != majorityHost {
					minorityVMs = append(minorityVMs, d)
				} else {
					majorityVMs = append(majorityVMs, d)
				}
			}
		}

		candidates := append(minorityVMs, majorityVMs...)
		for _, c := range candidates {
			target, found := pickBestHost(c, vms, hostByID, reachableHosts, rules)
			if found {
				return c, target, true
			}
		}
		return DomainPlacement{}, "", false

	default:
		for _, uid := range v.DomainUUIDs {
			c, exists := domainByID[uid]
			if !exists || c.State != "running" || c.HostID == "" {
				continue
			}
			target, found := pickBestHost(c, vms, hostByID, reachableHosts, rules)
			if found {
				return c, target, true
			}
		}
		return DomainPlacement{}, "", false
	}
}

func pickBestHost(
	culprit DomainPlacement,
	vms []placement.VMRef,
	hostByID map[string]HostMetrics,
	reachableHosts []HostMetrics,
	rules []placement.Rule,
) (string, bool) {
	var candidateIDs []string
	for _, h := range reachableHosts {
		if h.ID != culprit.HostID {
			candidateIDs = append(candidateIDs, h.ID)
		}
	}
	if len(candidateIDs) == 0 {
		return "", false
	}

	// Sort candidate hosts by load (lowest load first) so that Rank's
	// stable sort preserves least-loaded preference among equal-scoring hosts.
	slices.SortFunc(candidateIDs, func(a, b string) int {
		loadA := hostByID[a].CPUPct + hostByID[a].MemPct
		loadB := hostByID[b].CPUPct + hostByID[b].MemPct
		return cmp.Compare(loadA, loadB)
	})

	okHosts, _ := placement.Rank(rules, vms, culprit.UUID, candidateIDs)
	if len(okHosts) == 0 {
		return "", false
	}
	return okHosts[0], true
}

// RecommendationID is stable for the same move, so a recommendation shown in one evaluation
// can be found again when it is applied in the next.
func RecommendationID(domainUUID, from, to string) string {
	sum := sha256.Sum256([]byte(domainUUID + "|" + from + "|" + to))
	return hex.EncodeToString(sum[:8])
}

func recommendationID(domainUUID, from, to string) string {
	return RecommendationID(domainUUID, from, to)
}

func randomHex(bytes int) string {
	b := make([]byte, bytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Placement is the reachable host a new machine needing memB bytes of memory fits best: the
// least loaded one (CPU and memory together) that still has that much memory free. ok is
// false when none has room.
func Placement(hosts []HostMetrics, memB uint64) (best HostMetrics, ok bool) {
	lowest := math.MaxFloat64
	for _, h := range hosts {
		if !h.Reachable || (h.MemTotalB > 0 && h.MemTotalB-min(h.MemUsedB, h.MemTotalB) < memB) {
			continue
		}
		if load := h.CPUPct + h.MemPct; load < lowest {
			best, lowest, ok = h, load, true
		}
	}
	return best, ok
}
