package drs

import (
	"context"
	"testing"

	"github.com/onurbaydas/virtomir/internal/placement"
)

func TestDRS_BalancedCluster(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	hosts := []HostMetrics{
		{ID: "host-1", Name: "hv-01", Reachable: true, CPUPct: 40.0, MemPct: 50.0},
		{ID: "host-2", Name: "hv-02", Reachable: true, CPUPct: 42.0, MemPct: 48.0},
	}

	status := eng.Evaluate(context.Background(), hosts, nil, placement.View{})
	if status.BalanceScore < 90.0 {
		t.Errorf("expected high balance score, got %.1f", status.BalanceScore)
	}
	if len(status.Recommendations) != 0 {
		t.Errorf("expected no recommendations on balanced cluster, got %d", len(status.Recommendations))
	}
}

func TestDRS_UnbalancedClusterGeneratesRecommendation(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	hosts := []HostMetrics{
		{ID: "host-1", Name: "hv-01", Reachable: true, CPUPct: 85.0, MemPct: 80.0},
		{ID: "host-2", Name: "hv-02", Reachable: true, CPUPct: 10.0, MemPct: 15.0},
	}

	domains := []DomainPlacement{
		{UUID: "vm-1", Name: "heavy-app", HostID: "host-1", State: "running", CPUPct: 35.0, MemUsedB: 8 * 1024 * 1024 * 1024},
		{UUID: "vm-2", Name: "light-app", HostID: "host-1", State: "running", CPUPct: 10.0, MemUsedB: 2 * 1024 * 1024 * 1024},
	}

	status := eng.Evaluate(context.Background(), hosts, domains, placement.View{})
	if status.BalanceScore > 50.0 {
		t.Errorf("expected low balance score on skewed cluster, got %.1f", status.BalanceScore)
	}
	if len(status.Recommendations) == 0 {
		t.Fatal("expected at least one recommendation for skewed cluster")
	}

	rec := status.Recommendations[0]
	if rec.SourceHostID != "host-1" || rec.TargetHostID != "host-2" {
		t.Errorf("expected migration from host-1 to host-2, got %s -> %s", rec.SourceHostID, rec.TargetHostID)
	}
	if rec.DomainUUID != "vm-1" {
		t.Errorf("expected heavy-app (vm-1) migration, got %s", rec.DomainUUID)
	}
	// Applying re-evaluates and looks the recommendation up by ID.
	if again := eng.Evaluate(context.Background(), hosts, domains, placement.View{}); again.Recommendations[0].ID != rec.ID {
		t.Errorf("recommendation ID changed between evaluations: %s, then %s", rec.ID, again.Recommendations[0].ID)
	}
}

func TestDRS_AntiAffinityRuleViolation(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rule, err := eng.AddRule(AffinityRule{
		Name:      "DNS Redundancy",
		Kind:      AntiAffinityVMVM,
		Mandatory: true,
		Domains:   []string{"dns-01", "dns-02"},
	})
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}

	hosts := []HostMetrics{
		{ID: "host-1", Name: "hv-01", Reachable: true, CPUPct: 20.0, MemPct: 20.0},
		{ID: "host-2", Name: "hv-02", Reachable: true, CPUPct: 20.0, MemPct: 20.0},
	}

	// Both VMs are on host-1!
	domains := []DomainPlacement{
		{UUID: "dns-01", Name: "dns-01", HostID: "host-1", State: "running", CPUPct: 5.0, MemUsedB: 1024 * 1024 * 1024},
		{UUID: "dns-02", Name: "dns-02", HostID: "host-1", State: "running", CPUPct: 5.0, MemUsedB: 1024 * 1024 * 1024},
	}

	view := placement.View{
		Rules: []placement.Rule{
			{
				ID:       rule.ID,
				Name:     rule.Name,
				Kind:     placement.VMAntiAffinity,
				Enabled:  true,
				Strength: placement.Must,
				Domains:  rule.Domains,
			},
		},
	}

	status := eng.Evaluate(context.Background(), hosts, domains, view)
	if len(status.Recommendations) == 0 {
		t.Fatal("expected recommendation to resolve anti-affinity violation")
	}

	rec := status.Recommendations[0]
	if rec.Priority != 5 {
		t.Errorf("expected priority 5 for rule violation, got %d", rec.Priority)
	}
	if rec.DomainUUID != "dns-02" {
		t.Errorf("expected second VM to move, got %s", rec.DomainUUID)
	}
	if rec.TargetHostID != "host-2" {
		t.Errorf("expected move to host-2, got %s", rec.TargetHostID)
	}

	// Test deleting rule
	if err := eng.DeleteRule(rule.ID); err != nil {
		t.Fatalf("DeleteRule: %v", err)
	}
	if len(eng.GetConfig().Rules) != 0 {
		t.Errorf("expected 0 rules after deletion")
	}
}

func TestDRS_MustVMAffinity_ColocationRecommendation(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	hosts := []HostMetrics{
		{ID: "host-1", Name: "hv-01", Reachable: true, CPUPct: 20.0, MemPct: 20.0},
		{ID: "host-2", Name: "hv-02", Reachable: true, CPUPct: 20.0, MemPct: 20.0},
	}

	domains := []DomainPlacement{
		{UUID: "vm-1", Name: "db-01", HostID: "host-1", State: "running", CPUPct: 10.0, MemUsedB: 1024 * 1024 * 1024},
		{UUID: "vm-2", Name: "cache-01", HostID: "host-2", State: "running", CPUPct: 10.0, MemUsedB: 1024 * 1024 * 1024},
	}

	view := placement.View{
		Rules: []placement.Rule{
			{
				ID:       "aff001",
				Name:     "DB & Cache Collocation",
				Kind:     placement.VMAffinity,
				Enabled:  true,
				Strength: placement.Must,
				Domains:  []string{"vm-1", "vm-2"},
			},
		},
	}

	status := eng.Evaluate(context.Background(), hosts, domains, view)
	if len(status.Recommendations) != 1 {
		t.Fatalf("expected 1 recommendation, got %d", len(status.Recommendations))
	}
	rec := status.Recommendations[0]
	if rec.Priority != 5 {
		t.Errorf("expected priority 5, got %d", rec.Priority)
	}
	if rec.DomainUUID != "vm-2" {
		t.Errorf("expected culprit vm-2 to move, got %s", rec.DomainUUID)
	}
	if rec.SourceHostID != "host-2" || rec.TargetHostID != "host-1" {
		t.Errorf("expected move from host-2 to host-1, got %s -> %s", rec.SourceHostID, rec.TargetHostID)
	}
}

func TestDRS_MustVMHost_GroupRecommendation(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	hosts := []HostMetrics{
		{ID: "host-1", Name: "hv-01", Reachable: true, CPUPct: 20.0, MemPct: 20.0},
		{ID: "host-2", Name: "hv-02", Reachable: true, CPUPct: 20.0, MemPct: 20.0},
	}

	domains := []DomainPlacement{
		{UUID: "vm-1", Name: "gpu-worker", HostID: "host-2", State: "running", CPUPct: 10.0, MemUsedB: 1024 * 1024 * 1024},
	}

	view := placement.View{
		Rules: []placement.Rule{
			{
				ID:       "host001",
				Name:     "Pinned to Host 1",
				Kind:     placement.VMHost,
				Enabled:  true,
				Strength: placement.Must,
				Domains:  []string{"vm-1"},
				Hosts:    []string{"host-1"},
			},
		},
	}

	status := eng.Evaluate(context.Background(), hosts, domains, view)
	if len(status.Recommendations) != 1 {
		t.Fatalf("expected 1 recommendation, got %d", len(status.Recommendations))
	}
	rec := status.Recommendations[0]
	if rec.Priority != 5 {
		t.Errorf("expected priority 5, got %d", rec.Priority)
	}
	if rec.DomainUUID != "vm-1" {
		t.Errorf("expected vm-1 to move, got %s", rec.DomainUUID)
	}
	if rec.SourceHostID != "host-2" || rec.TargetHostID != "host-1" {
		t.Errorf("expected move from host-2 to host-1, got %s -> %s", rec.SourceHostID, rec.TargetHostID)
	}
}

func TestDRS_ShouldRule_Priority2(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	hosts := []HostMetrics{
		{ID: "host-1", Name: "hv-01", Reachable: true, CPUPct: 20.0, MemPct: 20.0},
		{ID: "host-2", Name: "hv-02", Reachable: true, CPUPct: 20.0, MemPct: 20.0},
	}

	domains := []DomainPlacement{
		{UUID: "web-01", Name: "web-01", HostID: "host-1", State: "running", CPUPct: 10.0, MemUsedB: 1024 * 1024 * 1024},
		{UUID: "web-02", Name: "web-02", HostID: "host-1", State: "running", CPUPct: 10.0, MemUsedB: 1024 * 1024 * 1024},
	}

	view := placement.View{
		Rules: []placement.Rule{
			{
				ID:       "soft001",
				Name:     "Prefer Separate Web Instances",
				Kind:     placement.VMAntiAffinity,
				Enabled:  true,
				Strength: placement.Should,
				Domains:  []string{"web-01", "web-02"},
			},
		},
	}

	status := eng.Evaluate(context.Background(), hosts, domains, view)
	if len(status.Recommendations) != 1 {
		t.Fatalf("expected 1 recommendation, got %d", len(status.Recommendations))
	}
	rec := status.Recommendations[0]
	if rec.Priority != 2 {
		t.Errorf("expected priority 2 for should rule, got %d", rec.Priority)
	}
	if rec.DomainUUID != "web-02" {
		t.Errorf("expected web-02 to move, got %s", rec.DomainUUID)
	}
	if rec.TargetHostID != "host-2" {
		t.Errorf("expected target host-2, got %s", rec.TargetHostID)
	}
}

func TestDRS_BalanceSuppressed_WhenTargetBreaksMust(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	hosts := []HostMetrics{
		{ID: "host-1", Name: "hv-01", Reachable: true, CPUPct: 90.0, MemPct: 85.0},
		{ID: "host-2", Name: "hv-02", Reachable: true, CPUPct: 10.0, MemPct: 10.0},
	}

	domains := []DomainPlacement{
		{UUID: "heavy-vm", Name: "heavy-vm", HostID: "host-1", State: "running", CPUPct: 40.0, MemUsedB: 8 * 1024 * 1024 * 1024},
	}

	// Must rule prevents heavy-vm from running on host-2
	view := placement.View{
		Rules: []placement.Rule{
			{
				ID:       "pinned-01",
				Name:     "Must stay on host-1",
				Kind:     placement.VMHost,
				Enabled:  true,
				Strength: placement.Must,
				Domains:  []string{"heavy-vm"},
				Hosts:    []string{"host-1"},
			},
		},
	}

	status := eng.Evaluate(context.Background(), hosts, domains, view)
	if len(status.Recommendations) != 0 {
		t.Errorf("expected balance recommendation to be suppressed, got %+v", status.Recommendations)
	}
}

func TestDRS_NoRecommendationTargetsHardViolatingHost(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	hosts := []HostMetrics{
		{ID: "host-1", Name: "hv-01", Reachable: true, CPUPct: 30.0, MemPct: 30.0},
		{ID: "host-2", Name: "hv-02", Reachable: true, CPUPct: 5.0, MemPct: 5.0},   // lowest load, but hard-violating!
		{ID: "host-3", Name: "hv-03", Reachable: true, CPUPct: 25.0, MemPct: 25.0}, // higher load, but valid
	}

	domains := []DomainPlacement{
		{UUID: "vm-1", Name: "vm-1", HostID: "host-1", State: "running", CPUPct: 5.0, MemUsedB: 1024 * 1024 * 1024},
		{UUID: "vm-2", Name: "vm-2", HostID: "host-1", State: "running", CPUPct: 5.0, MemUsedB: 1024 * 1024 * 1024},
	}

	// Must anti-affinity between vm-1 and vm-2.
	// AND must rule forbidding vm-2 from host-2 (even though host-2 has lowest load).
	view := placement.View{
		Rules: []placement.Rule{
			{
				ID:       "anti-01",
				Name:     "Separate VMs",
				Kind:     placement.VMAntiAffinity,
				Enabled:  true,
				Strength: placement.Must,
				Domains:  []string{"vm-1", "vm-2"},
			},
			{
				ID:       "exclude-01",
				Name:     "Exclude host-2 for vm-2",
				Kind:     placement.VMHost,
				Enabled:  true,
				Strength: placement.Must,
				Domains:  []string{"vm-2"},
				Hosts:    []string{"host-2"},
				Exclude:  true,
			},
		},
	}

	status := eng.Evaluate(context.Background(), hosts, domains, view)
	if len(status.Recommendations) == 0 {
		t.Fatal("expected recommendation to resolve anti-affinity violation")
	}

	vms := make([]placement.VMRef, len(domains))
	for i, d := range domains {
		vms[i] = placement.VMRef{UUID: d.UUID, Name: d.Name, HostID: d.HostID, Running: d.State == "running"}
	}

	for _, rec := range status.Recommendations {
		if rec.TargetHostID == "host-2" {
			t.Errorf("recommendation targeted hard-violating host-2: %+v", rec)
		}
		hard, _ := placement.Check(view.Rules, vms, rec.DomainUUID, rec.TargetHostID)
		if len(hard) > 0 {
			t.Errorf("recommendation %s targets hard-violating host %s: %v", rec.ID, rec.TargetHostID, hard)
		}
	}

	if status.Recommendations[0].TargetHostID != "host-3" {
		t.Errorf("expected target host-3, got %s", status.Recommendations[0].TargetHostID)
	}
}

func TestDRS_MustVMAffinity_3VMs_MajorityHostRecommendation(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	hosts := []HostMetrics{
		{ID: "host-1", Name: "hv-01", Reachable: true, CPUPct: 20.0, MemPct: 20.0},
		{ID: "host-2", Name: "hv-02", Reachable: true, CPUPct: 20.0, MemPct: 20.0},
	}

	// vm-1 and vm-2 are on host-1 (majority: 2 VMs). vm-3 is on host-2 (minority: 1 VM).
	domains := []DomainPlacement{
		{UUID: "vm-1", Name: "db-01", HostID: "host-1", State: "running", CPUPct: 10.0, MemUsedB: 1024 * 1024 * 1024},
		{UUID: "vm-2", Name: "cache-01", HostID: "host-1", State: "running", CPUPct: 10.0, MemUsedB: 1024 * 1024 * 1024},
		{UUID: "vm-3", Name: "app-01", HostID: "host-2", State: "running", CPUPct: 10.0, MemUsedB: 1024 * 1024 * 1024},
	}

	view := placement.View{
		Rules: []placement.Rule{
			{
				ID:       "aff003",
				Name:     "3-VM Stack Collocation",
				Kind:     placement.VMAffinity,
				Enabled:  true,
				Strength: placement.Must,
				Domains:  []string{"vm-1", "vm-2", "vm-3"},
			},
		},
	}

	status := eng.Evaluate(context.Background(), hosts, domains, view)
	if len(status.Recommendations) != 1 {
		t.Fatalf("expected 1 recommendation, got %d", len(status.Recommendations))
	}
	rec := status.Recommendations[0]
	if rec.Priority != 5 {
		t.Errorf("expected priority 5, got %d", rec.Priority)
	}
	if rec.DomainUUID != "vm-3" {
		t.Errorf("expected minority culprit vm-3 to move, got %s", rec.DomainUUID)
	}
	if rec.SourceHostID != "host-2" || rec.TargetHostID != "host-1" {
		t.Errorf("expected move from host-2 to host-1, got %s -> %s", rec.SourceHostID, rec.TargetHostID)
	}
}

func TestDRS_MustVMAntiAffinity_SelectsMovableCulprit(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	hosts := []HostMetrics{
		{ID: "host-1", Name: "hv-01", Reachable: true, CPUPct: 20.0, MemPct: 20.0},
		{ID: "host-2", Name: "hv-02", Reachable: true, CPUPct: 20.0, MemPct: 20.0},
	}

	// vm-1, vm-2, and vm-3 are all on host-1.
	domains := []DomainPlacement{
		{UUID: "vm-1", Name: "vm-1", HostID: "host-1", State: "running", CPUPct: 5.0, MemUsedB: 1024 * 1024 * 1024},
		{UUID: "vm-2", Name: "vm-2", HostID: "host-1", State: "running", CPUPct: 5.0, MemUsedB: 1024 * 1024 * 1024},
		{UUID: "vm-3", Name: "vm-3", HostID: "host-1", State: "running", CPUPct: 5.0, MemUsedB: 1024 * 1024 * 1024},
	}

	// Must anti-affinity among all 3.
	// But vm-2 is explicitly excluded from host-2 (cannot move there).
	view := placement.View{
		Rules: []placement.Rule{
			{
				ID:       "anti-3vm",
				Name:     "Separate 3 VMs",
				Kind:     placement.VMAntiAffinity,
				Enabled:  true,
				Strength: placement.Must,
				Domains:  []string{"vm-1", "vm-2", "vm-3"},
			},
			{
				ID:       "exclude-vm2",
				Name:     "Exclude host-2 for vm-2",
				Kind:     placement.VMHost,
				Enabled:  true,
				Strength: placement.Must,
				Domains:  []string{"vm-2"},
				Hosts:    []string{"host-2"},
				Exclude:  true,
			},
		},
	}

	status := eng.Evaluate(context.Background(), hosts, domains, view)
	if len(status.Recommendations) == 0 {
		t.Fatal("expected recommendation to resolve anti-affinity violation")
	}

	rec := status.Recommendations[0]
	// vm-2 must NOT be chosen because host-2 is excluded for vm-2
	if rec.DomainUUID == "vm-2" {
		t.Errorf("expected movable VM to be chosen, not excluded vm-2")
	}
	if rec.TargetHostID != "host-2" {
		t.Errorf("expected target host-2, got %s", rec.TargetHostID)
	}
	if rec.DomainUUID != "vm-3" && rec.DomainUUID != "vm-1" {
		t.Errorf("expected vm-3 or vm-1, got %s", rec.DomainUUID)
	}
}

func TestDRS_NoRecommendation_WhenAllCandidateHostsHardViolate(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	hosts := []HostMetrics{
		{ID: "host-1", Name: "hv-01", Reachable: true, CPUPct: 20.0, MemPct: 20.0},
		{ID: "host-2", Name: "hv-02", Reachable: true, CPUPct: 10.0, MemPct: 10.0},
	}

	domains := []DomainPlacement{
		{UUID: "vm-1", Name: "vm-1", HostID: "host-1", State: "running", CPUPct: 5.0, MemUsedB: 1024 * 1024 * 1024},
		{UUID: "vm-2", Name: "vm-2", HostID: "host-1", State: "running", CPUPct: 5.0, MemUsedB: 1024 * 1024 * 1024},
	}

	// Must anti-affinity between vm-1 and vm-2.
	// AND both vm-1 and vm-2 are excluded from host-2 (Rank returns empty).
	view := placement.View{
		Rules: []placement.Rule{
			{
				ID:       "anti-01",
				Name:     "Separate VMs",
				Kind:     placement.VMAntiAffinity,
				Enabled:  true,
				Strength: placement.Must,
				Domains:  []string{"vm-1", "vm-2"},
			},
			{
				ID:       "exclude-vm1",
				Name:     "Exclude host-2 for vm-1",
				Kind:     placement.VMHost,
				Enabled:  true,
				Strength: placement.Must,
				Domains:  []string{"vm-1"},
				Hosts:    []string{"host-2"},
				Exclude:  true,
			},
			{
				ID:       "exclude-vm2",
				Name:     "Exclude host-2 for vm-2",
				Kind:     placement.VMHost,
				Enabled:  true,
				Strength: placement.Must,
				Domains:  []string{"vm-2"},
				Hosts:    []string{"host-2"},
				Exclude:  true,
			},
		},
	}

	status := eng.Evaluate(context.Background(), hosts, domains, view)
	// Must NOT recommend moving to host-2 (which violates hard rules)
	if len(status.Recommendations) != 0 {
		t.Errorf("expected 0 recommendations when all candidates hard-violate, got %+v", status.Recommendations)
	}
}

func TestPlacementPicksTheLeastLoadedHostWithRoom(t *testing.T) {
	const gib = 1 << 30
	hosts := []HostMetrics{
		{ID: "busy", Reachable: true, CPUPct: 70, MemPct: 60, MemUsedB: 60 * gib, MemTotalB: 100 * gib},
		{ID: "idle-but-full", Reachable: true, CPUPct: 5, MemPct: 98, MemUsedB: 98 * gib, MemTotalB: 100 * gib},
		{ID: "quiet", Reachable: true, CPUPct: 20, MemPct: 30, MemUsedB: 30 * gib, MemTotalB: 100 * gib},
		{ID: "down", CPUPct: 0, MemPct: 0, MemTotalB: 100 * gib},
	}
	if h, ok := Placement(hosts, 8*gib); !ok || h.ID != "quiet" {
		t.Errorf("placement = %s, %v", h.ID, ok)
	}
	if _, ok := Placement(hosts, 80*gib); ok {
		t.Error("placed a machine no host has room for")
	}
}

// #37: DRS settings are versioned; a member catches up with a newer version only.
func TestConfigVersionAdopt(t *testing.T) {
	a, _ := New(t.TempDir())
	b, _ := New(t.TempDir())
	if err := a.SetConfig(Config{Enabled: true, Mode: ModeFullyAutomated, ThresholdStdDev: 10}); err != nil {
		t.Fatal(err)
	}
	cfg := a.GetConfig()
	if cfg.Version != 1 {
		t.Fatalf("version = %d", cfg.Version)
	}
	if ok, err := b.Adopt(cfg); !ok || err != nil {
		t.Fatalf("adopt: %v %v", ok, err)
	}
	if got := b.GetConfig(); got.Mode != ModeFullyAutomated || got.ThresholdStdDev != 10 || got.Version != 1 {
		t.Errorf("adopted = %+v", got)
	}
	if ok, _ := b.Adopt(Config{Version: 1, Mode: ModeManual}); ok {
		t.Error("the same version adopted again")
	}
}
