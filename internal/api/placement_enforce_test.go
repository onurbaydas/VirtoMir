package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/onurbaydas/virtomir/internal/drs"
	"github.com/onurbaydas/virtomir/internal/live"
	"github.com/onurbaydas/virtomir/internal/placement"
	"github.com/onurbaydas/virtomir/internal/virt"
)

func (f *fakeBackend) PlanMove(uuid string) (virt.MovePlan, error) {
	f.call("plan-move " + uuid)
	return virt.MovePlan{
		UUID: uuid,
		Name: uuid,
		XML:  "<domain type='kvm'><name>" + uuid + "</name></domain>",
	}, nil
}

func (f *fakeBackend) AdoptDomain(_ string) (string, error) {
	f.call("adopt")
	return "adopted-uuid", nil
}

func (f *fakeBackend) ListPools() ([]virt.Pool, error) {
	if f.pools != nil {
		return f.pools, nil
	}
	return []virt.Pool{
		{
			Name:           "default",
			Active:         true,
			Type:           "dir",
			AvailableBytes: 100 << 30,
			Path:           "/var/lib/libvirt/images",
		},
	}, nil
}

func (f *fakeBackend) ListVolumes(_ string) ([]virt.Volume, error) {
	return []virt.Volume{}, nil
}

// 1. Move to a must-violating host -> 409 naming the rule, no job started
func TestMovePlacementEnforcement_MustViolatingBlocked(t *testing.T) {
	a := newTestNode(t, "alpha", []virt.Domain{
		{UUID: "vm-1", Name: "vm-1", State: virt.StateShutoff},
	})
	b := newTestNode(t, "beta", []virt.Domain{
		{UUID: "vm-2", Name: "vm-2", State: virt.StateRunning},
	})
	joinNodes(t, a, b)

	snapA := live.Snapshot{
		Domains: []live.DomainLive{
			{Domain: virt.Domain{UUID: "vm-1", Name: "vm-1", State: virt.StateShutoff}},
		},
	}
	rawA, _ := json.Marshal(snapA)
	a.lv.backlog = [][]byte{rawA}

	snapB := live.Snapshot{
		Domains: []live.DomainLive{
			{Domain: virt.Domain{UUID: "vm-2", Name: "vm-2", State: virt.StateRunning}},
		},
	}
	rawB, _ := json.Marshal(snapB)
	b.lv.backlog = [][]byte{rawB}

	// Must anti-affinity rule between vm-1 and vm-2
	rule, err := a.placement.AddRule(placement.Rule{
		Name:     "Separate Web & DB",
		Kind:     placement.VMAntiAffinity,
		Enabled:  true,
		Strength: placement.Must,
		Domains:  []string{"vm-1", "vm-2"},
	})
	if err != nil {
		t.Fatal(err)
	}

	admin := a.signIn(t, "onur")
	body := fmt.Sprintf(`{"host":"%s","pool":"default"}`, b.store.SelfID())
	resp, respBody := admin.req(http.MethodPost, "/api/v1/domains/vm-1/move", body, "Origin", a.url)

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected status 409 Conflict, got %d: %s", resp.StatusCode, respBody)
	}

	var res struct {
		Error      string   `json:"error"`
		Soft       bool     `json:"soft"`
		Violations []string `json:"violations"`
	}
	if err := json.Unmarshal([]byte(respBody), &res); err != nil {
		t.Fatalf("unmarshal error response: %v", err)
	}

	if res.Soft {
		t.Errorf("expected soft:false, got true")
	}
	if len(res.Violations) == 0 {
		t.Fatalf("expected violations in response, got none")
	}
	if !containsString(res.Error, rule.Name) {
		t.Errorf("expected error %q to name rule %q", res.Error, rule.Name)
	}
	if len(a.api.jobs.List()) != 0 {
		t.Errorf("expected no job started, got %d jobs", len(a.api.jobs.List()))
	}
}

// 2. Move breaking only a should rule -> 409 with soft:true, and 202 with force:true
func TestMovePlacementEnforcement_SoftRuleWarnAndForce(t *testing.T) {
	a := newTestNode(t, "alpha", []virt.Domain{
		{UUID: "vm-1", Name: "vm-1", State: virt.StateShutoff},
	})
	b := newTestNode(t, "beta", []virt.Domain{
		{UUID: "vm-2", Name: "vm-2", State: virt.StateRunning},
	})
	joinNodes(t, a, b)

	snapA := live.Snapshot{
		Domains: []live.DomainLive{
			{Domain: virt.Domain{UUID: "vm-1", Name: "vm-1", State: virt.StateShutoff}},
		},
	}
	rawA, _ := json.Marshal(snapA)
	a.lv.backlog = [][]byte{rawA}

	snapB := live.Snapshot{
		Domains: []live.DomainLive{
			{Domain: virt.Domain{UUID: "vm-2", Name: "vm-2", State: virt.StateRunning}},
		},
	}
	rawB, _ := json.Marshal(snapB)
	b.lv.backlog = [][]byte{rawB}

	// Should anti-affinity rule between vm-1 and vm-2
	rule, err := a.placement.AddRule(placement.Rule{
		Name:     "Prefer Separate Apps",
		Kind:     placement.VMAntiAffinity,
		Enabled:  true,
		Strength: placement.Should,
		Domains:  []string{"vm-1", "vm-2"},
	})
	if err != nil {
		t.Fatal(err)
	}

	admin := a.signIn(t, "onur")

	// Attempt without force -> 409 with soft: true
	body := fmt.Sprintf(`{"host":"%s","pool":"default","force":false}`, b.store.SelfID())
	resp, respBody := admin.req(http.MethodPost, "/api/v1/domains/vm-1/move", body, "Origin", a.url)

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected status 409 Conflict without force, got %d: %s", resp.StatusCode, respBody)
	}

	var res struct {
		Error      string   `json:"error"`
		Soft       bool     `json:"soft"`
		Violations []string `json:"violations"`
	}
	if err := json.Unmarshal([]byte(respBody), &res); err != nil {
		t.Fatalf("unmarshal error response: %v", err)
	}

	if !res.Soft {
		t.Errorf("expected soft:true in response, got false")
	}
	if !containsString(res.Error, rule.Name) {
		t.Errorf("expected error %q to name rule %q", res.Error, rule.Name)
	}
	if len(res.Violations) == 0 {
		t.Errorf("expected violations list, got empty")
	}
	if len(a.api.jobs.List()) != 0 {
		t.Errorf("expected no job started before force, got %d", len(a.api.jobs.List()))
	}

	// Attempt with force: true -> 202 Accepted and job started
	forceBody := fmt.Sprintf(`{"host":"%s","pool":"default","force":true}`, b.store.SelfID())
	respForce, respForceBody := admin.req(http.MethodPost, "/api/v1/domains/vm-1/move", forceBody, "Origin", a.url)

	if respForce.StatusCode != http.StatusAccepted {
		t.Fatalf("expected status 202 Accepted with force:true, got %d: %s", respForce.StatusCode, respForceBody)
	}
	if len(a.api.jobs.List()) != 1 {
		t.Fatalf("expected 1 job started with force:true, got %d", len(a.api.jobs.List()))
	}
}

// 3. Clean move still returns 202 Accepted
func TestMovePlacementEnforcement_CleanMove(t *testing.T) {
	a := newTestNode(t, "alpha", []virt.Domain{
		{UUID: "vm-clean", Name: "vm-clean", State: virt.StateShutoff},
	})
	b := newTestNode(t, "beta", nil)
	joinNodes(t, a, b)

	snapA := live.Snapshot{
		Domains: []live.DomainLive{
			{Domain: virt.Domain{UUID: "vm-clean", Name: "vm-clean", State: virt.StateShutoff}},
		},
	}
	rawA, _ := json.Marshal(snapA)
	a.lv.backlog = [][]byte{rawA}

	snapB := live.Snapshot{Domains: []live.DomainLive{}}
	rawB, _ := json.Marshal(snapB)
	b.lv.backlog = [][]byte{rawB}

	admin := a.signIn(t, "onur")
	body := fmt.Sprintf(`{"host":"%s","pool":"default"}`, b.store.SelfID())
	resp, respBody := admin.req(http.MethodPost, "/api/v1/domains/vm-clean/move", body, "Origin", a.url)

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected status 202 Accepted for clean move, got %d: %s", resp.StatusCode, respBody)
	}
	if len(a.api.jobs.List()) != 1 {
		t.Fatalf("expected 1 job started, got %d", len(a.api.jobs.List()))
	}
}

// 4. Migrate path (running machine) gets the same block on hard violation
func TestMigratePlacementEnforcement_RunningMachineBlocked(t *testing.T) {
	a := newTestNode(t, "alpha", []virt.Domain{
		{UUID: "vm-running", Name: "vm-running", State: virt.StateRunning},
	})
	b := newTestNode(t, "beta", []virt.Domain{
		{UUID: "vm-other", Name: "vm-other", State: virt.StateRunning},
	})
	joinNodes(t, a, b)

	snapA := live.Snapshot{
		Domains: []live.DomainLive{
			{Domain: virt.Domain{UUID: "vm-running", Name: "vm-running", State: virt.StateRunning}},
		},
	}
	rawA, _ := json.Marshal(snapA)
	a.lv.backlog = [][]byte{rawA}

	snapB := live.Snapshot{
		Domains: []live.DomainLive{
			{Domain: virt.Domain{UUID: "vm-other", Name: "vm-other", State: virt.StateRunning}},
		},
	}
	rawB, _ := json.Marshal(snapB)
	b.lv.backlog = [][]byte{rawB}

	// Must anti-affinity rule between vm-running and vm-other
	rule, err := a.placement.AddRule(placement.Rule{
		Name:     "Strict Separation",
		Kind:     placement.VMAntiAffinity,
		Enabled:  true,
		Strength: placement.Must,
		Domains:  []string{"vm-running", "vm-other"},
	})
	if err != nil {
		t.Fatal(err)
	}

	admin := a.signIn(t, "onur")
	body := fmt.Sprintf(`{"host":"%s","pool":"default"}`, b.store.SelfID())
	resp, respBody := admin.req(http.MethodPost, "/api/v1/domains/vm-running/move", body, "Origin", a.url)

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected status 409 Conflict for running VM violating must rule, got %d: %s", resp.StatusCode, respBody)
	}

	var res struct {
		Error      string   `json:"error"`
		Violations []string `json:"violations"`
		Soft       bool     `json:"soft"`
	}
	if err := json.Unmarshal([]byte(respBody), &res); err != nil {
		t.Fatalf("unmarshal error response: %v", err)
	}

	if res.Soft {
		t.Errorf("expected soft:false, got true")
	}
	if !containsString(res.Error, rule.Name) {
		t.Errorf("expected error %q to name rule %q", res.Error, rule.Name)
	}
	if len(res.Violations) == 0 {
		t.Errorf("expected violations list, got empty")
	}
	if len(a.api.jobs.List()) != 0 {
		t.Errorf("expected no job started, got %d", len(a.api.jobs.List()))
	}
}

// 5 & 6. DRS Placement with uuid and without uuid
func TestDRSPlacement_SolverAwareAndCompat(t *testing.T) {
	alpha := newTestNode(t, "alpha", nil)
	beta := newTestNode(t, "beta", nil)
	gamma := newTestNode(t, "gamma", nil)
	delta := newTestNode(t, "delta", nil)

	joinNodes(t, alpha, beta)
	joinNodes(t, alpha, gamma)
	joinNodes(t, alpha, delta)

	drsEng, err := drs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = drsEng.SetConfig(drs.Config{Enabled: true, Mode: drs.ModePartiallyAutomated})
	alpha.api.drs = drsEng

	// Snapshots:
	// alpha (self): load = 40% CPU + 40% Mem = 80%
	snapAlpha := live.Snapshot{
		Host: live.HostLive{
			CPUPercent: 40,
			MemTotalKiB: 10240,
			MemUsedKiB:  4096,
		},
		Domains: []live.DomainLive{},
	}
	rawA, _ := json.Marshal(snapAlpha)
	alpha.lv.backlog = [][]byte{rawA}

	// beta: higher load = 35% CPU + 35% Mem = 70%
	// Holds vm-collocate (target for should affinity)
	snapBeta := live.Snapshot{
		Host: live.HostLive{
			CPUPercent: 35,
			MemTotalKiB: 10240,
			MemUsedKiB:  3584,
		},
		Domains: []live.DomainLive{
			{Domain: virt.Domain{UUID: "vm-collocate", Name: "vm-collocate", State: virt.StateRunning}},
		},
	}
	rawB, _ := json.Marshal(snapBeta)
	beta.lv.backlog = [][]byte{rawB}

	// gamma: lower load = 15% CPU + 15% Mem = 30%
	// Holds no conflicting or preferred VMs
	snapGamma := live.Snapshot{
		Host: live.HostLive{
			CPUPercent: 15,
			MemTotalKiB: 10240,
			MemUsedKiB:  1536,
		},
		Domains: []live.DomainLive{},
	}
	rawG, _ := json.Marshal(snapGamma)
	gamma.lv.backlog = [][]byte{rawG}

	// delta: lowest load = 5% CPU + 5% Mem = 10%
	// Holds vm-enemy (must anti-affinity collision for vm-target)
	snapDelta := live.Snapshot{
		Host: live.HostLive{
			CPUPercent: 5,
			MemTotalKiB: 10240,
			MemUsedKiB:  512,
		},
		Domains: []live.DomainLive{
			{Domain: virt.Domain{UUID: "vm-enemy", Name: "vm-enemy", State: virt.StateRunning}},
		},
	}
	rawD, _ := json.Marshal(snapDelta)
	delta.lv.backlog = [][]byte{rawD}

	// Rules:
	// 1. Must anti-affinity between vm-target and vm-enemy -> hard blocks delta
	_, err = alpha.placement.AddRule(placement.Rule{
		Name:     "Must Separate From Enemy",
		Kind:     placement.VMAntiAffinity,
		Enabled:  true,
		Strength: placement.Must,
		Domains:  []string{"vm-target", "vm-enemy"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// 2. Should affinity between vm-target and vm-collocate -> prefers beta over gamma
	_, err = alpha.placement.AddRule(placement.Rule{
		Name:     "Prefer Collocate With Buddy",
		Kind:     placement.VMAffinity,
		Enabled:  true,
		Strength: placement.Should,
		Domains:  []string{"vm-target", "vm-collocate"},
	})
	if err != nil {
		t.Fatal(err)
	}

	admin := alpha.signIn(t, "onur")

	// Case 5: GET /api/v1/cluster/drs/placement?uuid=vm-target&memoryMiB=1
	// - Skips hard-blocked delta (even though delta has lowest load: 10%)
	// - Prefers beta (which satisfies should affinity) over gamma (which has lower load: 30% vs 70%)
	resp, body := admin.req(http.MethodGet, "/api/v1/cluster/drs/placement?uuid=vm-target&memoryMiB=1", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("drs placement with uuid: status %d, body: %s", resp.StatusCode, body)
	}

	var resWithUUID struct {
		HostID     string   `json:"hostId"`
		HostName   string   `json:"hostName"`
		CPUPct     float64  `json:"cpuPct"`
		MemPct     float64  `json:"memPct"`
		SoftMissed []string `json:"softMissed"`
	}
	if err := json.Unmarshal([]byte(body), &resWithUUID); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if resWithUUID.HostID != beta.store.SelfID() {
		t.Fatalf("expected suggested host to be beta (%s), got %s (%s)", beta.store.SelfID(), resWithUUID.HostID, resWithUUID.HostName)
	}
	if len(resWithUUID.SoftMissed) != 0 {
		t.Errorf("expected softMissed to be empty for beta, got %+v", resWithUUID.SoftMissed)
	}

	// Case 5b: If preferred host does not have enough memory, fall back to next tier with softMissed populated
	// Beta has 10240 - 3584 = 6656 KiB free (< 7 MiB). Gamma has 10240 - 1536 = 8704 KiB free (> 7 MiB).
	respFallback, bodyFallback := admin.req(http.MethodGet, "/api/v1/cluster/drs/placement?uuid=vm-target&memoryMiB=7", "")
	if respFallback.StatusCode != http.StatusOK {
		t.Fatalf("drs placement fallback: status %d, body: %s", respFallback.StatusCode, bodyFallback)
	}
	var resFallback struct {
		HostID     string   `json:"hostId"`
		SoftMissed []string `json:"softMissed"`
	}
	if err := json.Unmarshal([]byte(bodyFallback), &resFallback); err != nil {
		t.Fatal(err)
	}
	if resFallback.HostID != gamma.store.SelfID() {
		t.Fatalf("expected fallback host gamma (%s), got %s", gamma.store.SelfID(), resFallback.HostID)
	}
	if len(resFallback.SoftMissed) == 0 || !containsString(resFallback.SoftMissed[0], "Prefer Collocate With Buddy") {
		t.Errorf("expected softMissed to name the missed rule, got %+v", resFallback.SoftMissed)
	}

	// Case 6: GET /api/v1/cluster/drs/placement?memoryMiB=1 (WITHOUT uuid)
	// - Solver ranking is skipped: behaves exactly as before
	// - Picks the lowest-load reachable host (delta: 10% load)
	respNoUUID, bodyNoUUID := admin.req(http.MethodGet, "/api/v1/cluster/drs/placement?memoryMiB=1", "")
	if respNoUUID.StatusCode != http.StatusOK {
		t.Fatalf("drs placement without uuid: status %d, body: %s", respNoUUID.StatusCode, bodyNoUUID)
	}

	var resNoUUID struct {
		HostID     string   `json:"hostId"`
		HostName   string   `json:"hostName"`
		SoftMissed []string `json:"softMissed"`
	}
	if err := json.Unmarshal([]byte(bodyNoUUID), &resNoUUID); err != nil {
		t.Fatalf("unmarshal response without uuid: %v", err)
	}

	if resNoUUID.HostID != delta.store.SelfID() {
		t.Fatalf("expected drsPlacement without uuid to pick lowest loaded host delta (%s), got %s (%s)", delta.store.SelfID(), resNoUUID.HostID, resNoUUID.HostName)
	}
	if len(resNoUUID.SoftMissed) != 0 {
		t.Errorf("expected softMissed empty without uuid, got %+v", resNoUUID.SoftMissed)
	}
}

func containsString(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || (len(needle) > 0 && jsonQuoteContains(haystack, needle)))
}

func jsonQuoteContains(s, sub string) bool {
	return len(s) > 0 && len(sub) > 0 && (s == sub || stringsContains(s, sub))
}

func stringsContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestApplyDRSRecommendation_StaleRecommendationBlocked(t *testing.T) {
	a := newTestNode(t, "alpha", []virt.Domain{
		{UUID: "vm-1", Name: "vm-1", State: virt.StateRunning},
		{UUID: "vm-2", Name: "vm-2", State: virt.StateRunning},
	})
	b := newTestNode(t, "beta", []virt.Domain{})
	joinNodes(t, a, b)

	drsEng, err := drs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.api.drs = drsEng

	snapA := live.Snapshot{
		Domains: []live.DomainLive{
			{Domain: virt.Domain{UUID: "vm-1", Name: "vm-1", State: virt.StateRunning}},
			{Domain: virt.Domain{UUID: "vm-2", Name: "vm-2", State: virt.StateRunning}},
		},
	}
	rawA, _ := json.Marshal(snapA)
	a.lv.backlog = [][]byte{rawA}

	snapB := live.Snapshot{
		Domains: []live.DomainLive{},
	}
	rawB, _ := json.Marshal(snapB)
	b.lv.backlog = [][]byte{rawB}

	admin := a.signIn(t, "onur")

	// 1. Must anti-affinity rule between vm-1 and vm-2 created via API, replicating across nodes
	rule1Body := `{"name":"Separate Web & DB","kind":"vm_anti_affinity","enabled":true,"strength":"must","domains":["vm-1","vm-2"]}`
	resp, body := admin.req(http.MethodPost, "/api/v1/placement/rules", rule1Body, "Origin", a.url)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create anti-affinity rule: status %d, body: %s", resp.StatusCode, body)
	}

	// Verify rule replication to both nodes
	if len(a.placement.Rules()) != 1 || len(b.placement.Rules()) != 1 {
		t.Fatalf("expected rule replicated to both nodes, alpha has %d, beta has %d", len(a.placement.Rules()), len(b.placement.Rules()))
	}

	// Get recommendations: anti-affinity violation produces a valid recommendation to migrate vm-2 to beta
	resp, body = admin.req(http.MethodGet, "/api/v1/cluster/drs", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get drs status: %d, body: %s", resp.StatusCode, body)
	}

	var status drs.Status
	if err := json.Unmarshal([]byte(body), &status); err != nil {
		t.Fatalf("unmarshal drs status: %v", err)
	}

	if len(status.Recommendations) == 0 {
		t.Fatal("expected recommendation from drs")
	}

	rec := status.Recommendations[0]
	if rec.DomainUUID != "vm-2" || rec.TargetHostID != b.store.SelfID() {
		t.Fatalf("unexpected recommendation: %+v", rec)
	}

	// 2. Cluster state changes: new exclude rule for vm-2 on beta is created, replicating to both nodes
	rule2Body := fmt.Sprintf(`{"name":"vm-2 excluded from beta","kind":"vm_host","enabled":true,"strength":"must","domains":["vm-2"],"hosts":["%s"],"exclude":true}`, b.store.SelfID())
	resp, body = admin.req(http.MethodPost, "/api/v1/placement/rules", rule2Body, "Origin", a.url)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create exclude rule: status %d, body: %s", resp.StatusCode, body)
	}

	if len(a.placement.Rules()) != 2 || len(b.placement.Rules()) != 2 {
		t.Fatalf("expected both rules replicated to both nodes, alpha has %d, beta has %d", len(a.placement.Rules()), len(b.placement.Rules()))
	}

	// Attempt to apply the earlier recommendation -> must fail with 409 Conflict because target host now violates hard constraint (stale recommendation)
	applyResp, applyBody := admin.req(http.MethodPost, "/api/v1/cluster/drs/recommendations/"+rec.ID+"/apply", "", "Origin", a.url)
	if applyResp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 Conflict, got %d: %s", applyResp.StatusCode, applyBody)
	}

	if !strings.Contains(applyBody, "recommendation is stale") {
		t.Errorf("expected 'recommendation is stale' in error, got: %s", applyBody)
	}
	if len(a.api.jobs.List()) != 0 {
		t.Errorf("expected no job started for stale recommendation, got %d", len(a.api.jobs.List()))
	}
}
