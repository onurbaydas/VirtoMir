package lcm

import (
	"strings"
	"testing"
)

func TestQuorumSafeWithout(t *testing.T) {
	for _, tc := range []struct {
		name    string
		members []string
		ha      bool
		votes   int
		witness bool
		safe    bool
	}{
		{"HA off", []string{"a", "b"}, false, 2, false, true},
		{"2 nodes, no witness", []string{"a", "b"}, true, 2, false, false},
		{"2 nodes + witness", []string{"a", "b"}, true, 3, true, true},
		{"2 nodes, witness down", []string{"a", "b"}, true, 3, false, false},
		{"3 nodes", []string{"a", "b", "c"}, true, 3, false, true},
		{"3 nodes, one already in maintenance", []string{"a", "c"}, true, 3, false, false},
	} {
		safe, msg := QuorumSafeWithout(tc.members, "a", tc.ha, tc.votes, tc.witness)
		if safe != tc.safe || (!safe && !strings.Contains(msg, "quorum")) {
			t.Errorf("%s: safe %v (%q), want %v", tc.name, safe, msg, tc.safe)
		}
	}
}

func TestEvacuationFeasible(t *testing.T) {
	gib := uint64(1 << 20)
	vms := []VM{
		{UUID: "small", Name: "small", MemKiB: 1 * gib, Running: true},
		{UUID: "big", Name: "big", MemKiB: 6 * gib, Running: true},
		{UUID: "off", Name: "off", MemKiB: 64 * gib},
	}
	targets := []Target{{HostID: "b", Name: "b", FreeKiB: int64(8 * gib)}, {HostID: "c", Name: "c", FreeKiB: int64(4 * gib)}}
	moves, problems := EvacuationFeasible(vms, targets, nil)
	if len(problems) != 0 || len(moves) != 2 || moves[0].VMUUID != "big" || moves[0].TargetHostID != "b" || moves[1].TargetHostID != "c" {
		t.Fatalf("moves %+v problems %v (biggest first, most free memory first)", moves, problems)
	}

	// A placement rule keeps "big" off b, and c is too small.
	_, problems = EvacuationFeasible(vms, targets, func(vm, host string) ([]string, []string) {
		if vm == "big" && host == "b" {
			return []string{"anti-affinity"}, nil
		}
		return nil, nil
	})
	if len(problems) != 1 || !strings.Contains(problems[0], "big") {
		t.Errorf("placement-blocked: %v", problems)
	}

	_, problems = EvacuationFeasible([]VM{{Name: "gpu", Running: true, HasHostDevice: true}}, targets, nil)
	if len(problems) != 1 || !strings.Contains(problems[0], "host device") {
		t.Errorf("host device: %v", problems)
	}
	_, problems = EvacuationFeasible([]VM{{Name: "huge", MemKiB: 100 * gib, Running: true}}, targets, nil)
	if len(problems) != 1 {
		t.Errorf("over capacity: %v", problems)
	}
}

func TestVersions(t *testing.T) {
	if !SameMajor([]string{"v1.2.0", "1.9.3", "dev"}) || SameMajor([]string{"0.9.0", "1.0.0"}) {
		t.Error("SameMajor")
	}
	if got := MinVersion([]string{"0.10.0", "dev", "0.9.2"}); got != "0.9.2" {
		t.Errorf("MinVersion = %s", got)
	}
	if !FeatureAllowed("0.10.0", "0.9.0") || FeatureAllowed("0.8.9", "0.9.0") || !FeatureAllowed("dev", "9.0.0") || !FeatureAllowed("", "1.0.0") {
		t.Error("FeatureAllowed")
	}
	if !AtLeast("v0.10.0", "0.9.0") || AtLeast("0.9.0", "0.10.0") || !AtLeast("0.9.0", "") {
		t.Error("AtLeast")
	}
	if ok, msg := RunningJobsSafe(2); ok || !strings.Contains(msg, "2 running") {
		t.Errorf("RunningJobsSafe: %v %q", ok, msg)
	}
}
