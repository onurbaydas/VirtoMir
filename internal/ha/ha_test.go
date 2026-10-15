package ha

import (
	"context"
	"path/filepath"
	"testing"
)

func TestHA_QuorumCalculation(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "ha.json"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Single node cluster
	store.RecordHeartbeat("node-1", "hv-01", "https://192.168.77.10:8420")
	q := store.EvaluateQuorum(1)
	if !q.Quorate || q.ActiveNodes != 1 || q.RequiredQuorum != 1 {
		t.Errorf("expected 1-node cluster quorate, got %+v", q)
	}

	// 3-node cluster with 2 online
	store.RecordHeartbeat("node-2", "hv-02", "https://192.168.77.11:8420")
	q = store.EvaluateQuorum(3)
	if !q.Quorate || q.ActiveNodes != 2 || q.RequiredQuorum != 2 {
		t.Errorf("expected 3-node cluster with 2 alive to have quorum, got %+v", q)
	}

	// 2-node cluster without witness with 1 alive -> lost quorum
	store = &Store{
		path:       filepath.Join(dir, "ha2.json"),
		heartbeats: make(map[string]*NodeHeartbeat),
		cfg: Config{
			FailoverTimeoutSec: 10,
		},
	}
	store.RecordHeartbeat("node-1", "hv-01", "https://192.168.77.10:8420")
	q = store.EvaluateQuorum(2)
	if q.Quorate {
		t.Errorf("expected 2-node cluster with 1 node to lose quorum without witness")
	}

	// 2-node cluster WITH witness with 1 alive -> quorate
	store.cfg.WitnessIP = "192.168.77.1"
	q = store.EvaluateQuorum(2)
	if !q.Quorate {
		t.Errorf("expected 2-node cluster with witness to maintain quorum")
	}
}

func TestHA_VMPoliciesAndSimulation(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "ha.json"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	err = store.SetVMPolicy(VMHAPolicy{
		UUID:     "vm-1",
		Name:     "DB-Master",
		Enabled:  true,
		Priority: "critical",
	})
	if err != nil {
		t.Fatalf("SetVMPolicy: %v", err)
	}

	err = store.SetVMPolicy(VMHAPolicy{
		UUID:     "vm-2",
		Name:     "App-Server",
		Enabled:  true,
		Priority: "normal",
	})
	if err != nil {
		t.Fatalf("SetVMPolicy: %v", err)
	}

	sim, err := store.SimulateFailover("node-1", []string{"node-1", "node-2", "node-3"})
	if err != nil {
		t.Fatalf("SimulateFailover: %v", err)
	}

	if !sim.HasQuorum || !sim.CanSafelyFailover {
		t.Errorf("expected safe failover, got %+v", sim)
	}
	if len(sim.FailoverPlan) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(sim.FailoverPlan))
	}
	// Critical priority should come first
	if sim.FailoverPlan[0].VMUUID != "vm-1" {
		t.Errorf("expected DB-Master (critical) first, got %s", sim.FailoverPlan[0].VMName)
	}

	// Test fencing
	res, err := store.FenceNode(context.Background(), "node-1", "poweroff")
	if err != nil {
		t.Fatalf("FenceNode: %v", err)
	}
	if !res.Success {
		t.Errorf("expected fence success, got %+v", res)
	}
}
