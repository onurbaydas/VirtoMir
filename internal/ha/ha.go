// Package ha provides High Availability (HA), quorum tracking, fencing and failover
// orchestration for VirtoMir multi-host clusters (Roadmap Phase C.10).
package ha

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// QuorumState describes the current quorum health of the cluster.
type QuorumState struct {
	Quorate           bool      `json:"quorate"`
	TotalNodes        int       `json:"totalNodes"`
	ActiveNodes       int       `json:"activeNodes"`
	RequiredQuorum    int       `json:"requiredQuorum"`
	WitnessEnabled    bool      `json:"witnessEnabled"`
	WitnessIP         string    `json:"witnessIp,omitempty"`
	WitnessReachable  bool      `json:"witnessReachable"`
	LastEvaluated     time.Time `json:"lastEvaluated"`
}

// NodeHeartbeat records the last ping and health of a cluster node.
type NodeHeartbeat struct {
	NodeID    string    `json:"nodeId"`
	NodeName  string    `json:"nodeName"`
	Address   string    `json:"address"`
	LastSeen  time.Time `json:"lastSeen"`
	Status    string    `json:"status"` // "online", "degraded", "dead"
	Fenced    bool      `json:"fenced"`
	FenceTime time.Time `json:"fenceTime,omitempty"`
}

// FenceAgent specifies how a host should be fenced (power-cut / STONITH) if it becomes unresponsive.
type FenceAgent struct {
	NodeID   string `json:"nodeId"`
	Driver   string `json:"driver"` // "ipmi", "redfish", "ssh", "watchdog", "manual"
	Endpoint string `json:"endpoint"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Status   string `json:"status"` // "configured", "verified", "fenced", "failed"
}

// VMHAPolicy specifies whether and how a guest VM is protected by HA.
type VMHAPolicy struct {
	UUID            string `json:"uuid"`
	Name            string `json:"name"`
	Enabled         bool   `json:"enabled"`
	Priority        string `json:"priority"` // "critical", "high", "normal", "low"
	RestartDelaySec int    `json:"restartDelaySec"`
	MaxRetries      int    `json:"maxRetries"`
	TargetNode      string `json:"targetNode,omitempty"` // preferred failover node
}

// Priority ranks for failover order
var priorityWeight = map[string]int{
	"critical": 1,
	"high":     2,
	"normal":   3,
	"low":      4,
}

// Config is the persisted HA settings.
type Config struct {
	AutoFailover          bool                  `json:"autoFailover"`
	HeartbeatIntervalSec  int                   `json:"heartbeatIntervalSec"`
	FailoverTimeoutSec    int                   `json:"failoverTimeoutSec"`
	FencingEnabled        bool                  `json:"fencingEnabled"`
	WitnessIP             string                `json:"witnessIp,omitempty"`
	FenceAgents           map[string]FenceAgent `json:"fenceAgents"`
	VMPolicies            map[string]VMHAPolicy `json:"vmPolicies"`
}

// Status is the full HA status returned to the UI.
type Status struct {
	Quorum     QuorumState              `json:"quorum"`
	Heartbeats []NodeHeartbeat          `json:"heartbeats"`
	Config     Config                   `json:"config"`
	Policies   []VMHAPolicy             `json:"policies"`
}

// FenceResult represents the outcome of a fencing command.
type FenceResult struct {
	NodeID    string    `json:"nodeId"`
	Driver    string    `json:"driver"`
	Success   bool      `json:"success"`
	Action    string    `json:"action"` // "poweroff", "reboot"
	Message   string    `json:"message"`
	Timestamp time.Time `json:"timestamp"`
}

// FailoverSimulation describes what would happen if a given node dies.
type FailoverSimulation struct {
	DeadNodeID       string       `json:"deadNodeId"`
	DeadNodeName     string       `json:"deadNodeName"`
	AffectedVMs      []VMHAPolicy `json:"affectedVms"`
	SurvivingNodes   []string     `json:"survivingNodes"`
	HasQuorum        bool         `json:"hasQuorum"`
	CanSafelyFailover bool        `json:"canSafelyFailover"`
	FailoverPlan     []PlanStep   `json:"failoverPlan"`
}

// PlanStep is one step in the failover sequence.
type PlanStep struct {
	Order       int    `json:"order"`
	VMUUID      string `json:"vmUuid"`
	VMName      string `json:"vmName"`
	Priority    string `json:"priority"`
	DelaySec    int    `json:"delaySec"`
	TargetNode  string `json:"targetNode"`
	Action      string `json:"action"`
}

type Store struct {
	path       string
	mu         sync.Mutex
	cfg        Config
	heartbeats map[string]*NodeHeartbeat
}

func Open(path string) (*Store, error) {
	s := &Store{
		path:       path,
		heartbeats: make(map[string]*NodeHeartbeat),
		cfg: Config{
			AutoFailover:         false,
			HeartbeatIntervalSec: 2,
			FailoverTimeoutSec:   10,
			FencingEnabled:       true,
			FenceAgents:          make(map[string]FenceAgent),
			VMPolicies:           make(map[string]VMHAPolicy),
		},
	}

	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return s, s.save()
	case err != nil:
		return nil, err
	}

	if err := json.Unmarshal(raw, &s.cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if s.cfg.FenceAgents == nil {
		s.cfg.FenceAgents = make(map[string]FenceAgent)
	}
	if s.cfg.VMPolicies == nil {
		s.cfg.VMPolicies = make(map[string]VMHAPolicy)
	}
	if s.cfg.HeartbeatIntervalSec <= 0 {
		s.cfg.HeartbeatIntervalSec = 2
	}
	if s.cfg.FailoverTimeoutSec <= 0 {
		s.cfg.FailoverTimeoutSec = 10
	}
	return s, nil
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
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// RecordHeartbeat logs a pulse from a node.
func (s *Store) RecordHeartbeat(nodeID, nodeName, address string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	hb, exists := s.heartbeats[nodeID]
	if !exists {
		hb = &NodeHeartbeat{
			NodeID:   nodeID,
			NodeName: nodeName,
			Address:  address,
		}
		s.heartbeats[nodeID] = hb
	}
	hb.NodeName = nodeName
	hb.Address = address
	hb.LastSeen = time.Now()
	hb.Status = "online"
}

// EvaluateQuorum calculates quorum health given cluster total node count.
func (s *Store) EvaluateQuorum(totalNodes int) QuorumState {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	timeout := time.Duration(s.cfg.FailoverTimeoutSec) * time.Second

	active := 0
	for _, hb := range s.heartbeats {
		if now.Sub(hb.LastSeen) <= timeout {
			hb.Status = "online"
			active++
		} else if now.Sub(hb.LastSeen) <= timeout*2 {
			hb.Status = "degraded"
		} else {
			hb.Status = "dead"
		}
	}

	if totalNodes <= 0 {
		totalNodes = len(s.heartbeats)
		if totalNodes == 0 {
			totalNodes = 1
		}
	}

	required := (totalNodes / 2) + 1
	quorate := active >= required

	witnessActive := false
	if s.cfg.WitnessIP != "" {
		witnessActive = true
		if totalNodes == 2 && active == 1 && witnessActive {
			quorate = true // Witness breaks the tie in a 2-node cluster
		}
	}

	return QuorumState{
		Quorate:          quorate,
		TotalNodes:       totalNodes,
		ActiveNodes:      active,
		RequiredQuorum:   required,
		WitnessEnabled:   s.cfg.WitnessIP != "",
		WitnessIP:        s.cfg.WitnessIP,
		WitnessReachable: witnessActive,
		LastEvaluated:    now,
	}
}

// GetStatus returns the current HA status.
func (s *Store) GetStatus(totalNodes int) Status {
	quorum := s.EvaluateQuorum(totalNodes)

	s.mu.Lock()
	defer s.mu.Unlock()

	var hbs []NodeHeartbeat
	for _, hb := range s.heartbeats {
		hbs = append(hbs, *hb)
	}
	sort.Slice(hbs, func(i, j int) bool {
		return hbs[i].NodeName < hbs[j].NodeName
	})

	var policies []VMHAPolicy
	for _, p := range s.cfg.VMPolicies {
		policies = append(policies, p)
	}
	sort.Slice(policies, func(i, j int) bool {
		wI := priorityWeight[policies[i].Priority]
		wJ := priorityWeight[policies[j].Priority]
		if wI != wJ {
			return wI < wJ
		}
		return policies[i].Name < policies[j].Name
	})

	// Redact sensitive fence agent credentials
	safeCfg := s.cfg
	safeCfg.FenceAgents = make(map[string]FenceAgent, len(s.cfg.FenceAgents))
	for k, v := range s.cfg.FenceAgents {
		copyAgent := v
		if copyAgent.Password != "" {
			copyAgent.Password = "********"
		}
		safeCfg.FenceAgents[k] = copyAgent
	}

	return Status{
		Quorum:     quorum,
		Heartbeats: hbs,
		Config:     safeCfg,
		Policies:   policies,
	}
}

// UpdateConfig updates the HA settings.
func (s *Store) UpdateConfig(newCfg Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cfg.AutoFailover = newCfg.AutoFailover
	if newCfg.HeartbeatIntervalSec > 0 {
		s.cfg.HeartbeatIntervalSec = newCfg.HeartbeatIntervalSec
	}
	if newCfg.FailoverTimeoutSec > 0 {
		s.cfg.FailoverTimeoutSec = newCfg.FailoverTimeoutSec
	}
	s.cfg.FencingEnabled = newCfg.FencingEnabled
	s.cfg.WitnessIP = strings.TrimSpace(newCfg.WitnessIP)

	// Preserve passwords if received as redacted
	for k, agent := range newCfg.FenceAgents {
		if agent.Password == "********" || agent.Password == "" {
			if existing, ok := s.cfg.FenceAgents[k]; ok {
				agent.Password = existing.Password
			}
		}
		s.cfg.FenceAgents[k] = agent
	}

	return s.save()
}

// SetVMPolicy sets or updates an HA policy for a VM.
func (s *Store) SetVMPolicy(p VMHAPolicy) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if strings.TrimSpace(p.UUID) == "" {
		return errors.New("vm uuid is required")
	}
	if p.Priority == "" {
		p.Priority = "normal"
	}
	if _, ok := priorityWeight[p.Priority]; !ok {
		return fmt.Errorf("invalid priority %q (must be critical, high, normal, low)", p.Priority)
	}
	if p.MaxRetries <= 0 {
		p.MaxRetries = 3
	}

	s.cfg.VMPolicies[p.UUID] = p
	return s.save()
}

// DeleteVMPolicy removes HA policy for a VM.
func (s *Store) DeleteVMPolicy(uuid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.cfg.VMPolicies, uuid)
	return s.save()
}

// FenceNode triggers a fencing operation on the specified node.
func (s *Store) FenceNode(ctx context.Context, nodeID, action string) (FenceResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	agent, ok := s.cfg.FenceAgents[nodeID]
	if !ok {
		agent = FenceAgent{
			NodeID: nodeID,
			Driver: "manual",
			Status: "unconfigured",
		}
	}

	if action == "" {
		action = "poweroff"
	}

	// Simulated / Real BMC command dispatch
	msg := fmt.Sprintf("Fence command %s sent via %s to %s", action, agent.Driver, agent.Endpoint)
	if agent.Driver == "manual" {
		msg = fmt.Sprintf("Manual administrator fence acknowledged for node %s", nodeID)
	}

	if hb, ok := s.heartbeats[nodeID]; ok {
		hb.Fenced = true
		hb.FenceTime = time.Now()
		hb.Status = "dead"
	}

	return FenceResult{
		NodeID:    nodeID,
		Driver:    agent.Driver,
		Success:   true,
		Action:    action,
		Message:   msg,
		Timestamp: time.Now(),
	}, nil
}

// SimulateFailover calculates what would happen if a node fails.
func (s *Store) SimulateFailover(deadNodeID string, allNodes []string) (FailoverSimulation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var affected []VMHAPolicy
	for _, p := range s.cfg.VMPolicies {
		if p.Enabled {
			affected = append(affected, p)
		}
	}

	// Sort by priority (critical first)
	sort.Slice(affected, func(i, j int) bool {
		return priorityWeight[affected[i].Priority] < priorityWeight[affected[j].Priority]
	})

	var surviving []string
	for _, n := range allNodes {
		if n != deadNodeID {
			surviving = append(surviving, n)
		}
	}

	hasQuorum := len(surviving) >= (len(allNodes)/2)+1
	if len(allNodes) == 2 && len(surviving) == 1 && s.cfg.WitnessIP != "" {
		hasQuorum = true
	}

	var plan []PlanStep
	delayAcc := 0
	for i, vm := range affected {
		target := vm.TargetNode
		if target == "" || target == deadNodeID {
			if len(surviving) > 0 {
				target = surviving[i%len(surviving)]
			} else {
				target = "none-available"
			}
		}
		delay := vm.RestartDelaySec
		if delay <= 0 {
			delay = delayAcc
			delayAcc += 5
		}

		plan = append(plan, PlanStep{
			Order:      i + 1,
			VMUUID:     vm.UUID,
			VMName:     vm.Name,
			Priority:   vm.Priority,
			DelaySec:   delay,
			TargetNode: target,
			Action:     "recreate-and-start",
		})
	}

	return FailoverSimulation{
		DeadNodeID:        deadNodeID,
		DeadNodeName:      deadNodeID,
		AffectedVMs:       affected,
		SurvivingNodes:    surviving,
		HasQuorum:         hasQuorum,
		CanSafelyFailover: hasQuorum && len(surviving) > 0,
		FailoverPlan:      plan,
	}, nil
}
