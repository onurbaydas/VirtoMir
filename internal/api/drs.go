package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/onurbaydas/virtomir/internal/activity"
	"github.com/onurbaydas/virtomir/internal/auth"
	"github.com/onurbaydas/virtomir/internal/cluster"
	"github.com/onurbaydas/virtomir/internal/drs"
	"github.com/onurbaydas/virtomir/internal/jobs"
	"github.com/onurbaydas/virtomir/internal/live"
	"github.com/onurbaydas/virtomir/internal/placement"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// drsRoutes registers DRS endpoints and unified cluster inventory routes.
func (s *Server) drsRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/cluster/drs", s.getDRS)
	mux.HandleFunc("PUT /api/v1/cluster/drs/config", s.setDRSConfig)
	mux.HandleFunc("POST /api/v1/cluster/drs/rules", s.createDRSRule)
	mux.HandleFunc("DELETE /api/v1/cluster/drs/rules/{id}", s.deleteDRSRule)
	mux.HandleFunc("POST /api/v1/cluster/drs/recommendations/{id}/apply", s.applyDRSRecommendation)
	mux.HandleFunc("GET /api/v1/cluster/drs/placement", s.drsPlacement)

	// Unified vCenter-style cluster inventory: returns all VMs across all cluster nodes
	mux.HandleFunc("GET /api/v1/cluster/inventory/machines", s.getClusterInventoryMachines)
}

// ClusterDomainSummary is a domain with its physical host information for the unified view.
type ClusterDomainSummary struct {
	virt.Domain
	HostID   string `json:"hostId"`
	HostName string `json:"hostName"`
}

func (s *Server) getDRS(w http.ResponseWriter, r *http.Request) {
	if s.drs == nil {
		var rules []drs.AffinityRule
		if s.placement != nil {
			rules = placementRulesToDRS(s.placement.Rules())
		}
		s.writeJSON(w, http.StatusOK, drs.Status{Enabled: false, BalanceScore: 100.0, Rules: rules})
		return
	}

	hosts, domains := s.collectClusterPlacements(r.Context(), identity(r))
	status := s.drs.Evaluate(r.Context(), hosts, domains, s.placementView())
	if s.placement != nil {
		status.Rules = placementRulesToDRS(s.placement.Rules())
	} else {
		status.Rules = []drs.AffinityRule{}
	}
	s.writeJSON(w, http.StatusOK, status)
}

func (s *Server) setDRSConfig(w http.ResponseWriter, r *http.Request) {
	if s.drs == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "DRS is not available on this host"})
		return
	}
	var req drs.Config
	if !s.decode(w, r, &req) {
		return
	}
	if err := s.drs.SetConfig(req); err != nil {
		s.writeError(w, err)
		return
	}
	s.replicateToMembers(r, http.MethodPut, "/api/v1/cluster/drs/config", req)
	s.writeJSON(w, http.StatusOK, s.drs.GetConfig())
}

func (s *Server) createDRSRule(w http.ResponseWriter, r *http.Request) {
	if s.placement == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "placement is not available"})
		return
	}
	var req drs.AffinityRule
	if !s.decode(w, r, &req) {
		return
	}
	rule, err := drsRuleToPlacement(req)
	if err != nil {
		s.writeError(w, err)
		return
	}
	created, err := s.placement.AddRule(rule)
	if err != nil {
		s.writeError(w, err)
		return
	}
	resp := placementRuleToDRS(created)
	if s.drs != nil {
		if _, err := s.drs.AddRule(resp); err != nil {
			s.log.Warn("failed to update DRS engine with rule", "id", resp.ID, "err", err)
		}
	}
	s.replicateToMembers(r, http.MethodPost, "/api/v1/cluster/drs/rules", resp)
	s.writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) deleteDRSRule(w http.ResponseWriter, r *http.Request) {
	if s.placement == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "placement is not available"})
		return
	}
	id := r.PathValue("id")
	if err := s.placement.DeleteRule(id); err != nil {
		s.writeError(w, err)
		return
	}
	if s.drs != nil {
		if err := s.drs.DeleteRule(id); err != nil && !errors.Is(err, drs.ErrNotFound) {
			s.log.Warn("failed to delete rule from DRS engine", "id", id, "err", err)
		}
	}
	s.replicateToMembers(r, http.MethodDelete, "/api/v1/cluster/drs/rules/"+id, nil)
	w.WriteHeader(http.StatusNoContent)
}

// replicateToMembers repeats a cluster-wide setting or rule change on the other members.
// ?local=1 marks the copies, which are not passed on again.
func (s *Server) replicateToMembers(r *http.Request, method, path string, body any) {
	if r.URL.Query().Get("local") != "" || s.cluster == nil || !s.cluster.Clustered() {
		return
	}
	id := identity(r)
	members, _ := s.cluster.Members()
	var wg sync.WaitGroup
	for _, n := range members {
		if n.ID == s.cluster.SelfID() {
			continue
		}
		wg.Add(1)
		go func(n cluster.Node) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			defer cancel()
			if err := s.peer(n).As(id.Name, id.Role.String(), id.Token).Call(ctx, method, path+"?local=1", body, nil); err != nil {
				s.log.Warn("cluster change not copied to a member", "member", n.Name, "err", err)
			}
		}(n)
	}
	wg.Wait()
}

func placementRuleToDRS(r placement.Rule) drs.AffinityRule {
	var k drs.RuleKind
	switch r.Kind {
	case placement.VMAffinity:
		k = drs.AffinityVMVM
	case placement.VMAntiAffinity:
		k = drs.AntiAffinityVMVM
	case placement.VMHost:
		k = drs.AffinityVMHost
	default:
		k = ""
	}
	targetHost := ""
	if r.Kind == placement.VMHost && len(r.Hosts) == 1 {
		targetHost = r.Hosts[0]
	}
	return drs.AffinityRule{
		ID:         r.ID,
		Name:       r.Name,
		Kind:       k,
		Enabled:    r.Enabled,
		Mandatory:  r.Strength == placement.Must || r.Strength == "",
		Domains:    r.Domains,
		TargetHost: targetHost,
	}
}

func placementRulesToDRS(rules []placement.Rule) []drs.AffinityRule {
	out := make([]drs.AffinityRule, 0, len(rules))
	for _, r := range rules {
		drsRule := placementRuleToDRS(r)
		if drsRule.Kind == "" {
			continue
		}
		out = append(out, drsRule)
	}
	return out
}

func drsRuleToPlacement(r drs.AffinityRule) (placement.Rule, error) {
	var k placement.Kind
	switch r.Kind {
	case drs.AffinityVMVM:
		k = placement.VMAffinity
	case drs.AntiAffinityVMVM:
		k = placement.VMAntiAffinity
	case drs.AffinityVMHost, "vm_host":
		k = placement.VMHost
	default:
		return placement.Rule{}, fmt.Errorf("%w: unsupported rule kind %q", drs.ErrInvalid, r.Kind)
	}
	str := placement.Should
	if r.Mandatory {
		str = placement.Must
	}
	var hosts []string
	if k == placement.VMHost && r.TargetHost != "" {
		hosts = []string{r.TargetHost}
	}
	return placement.Rule{
		ID:       r.ID,
		Name:     r.Name,
		Kind:     k,
		Enabled:  r.Enabled,
		Strength: str,
		Domains:  r.Domains,
		Hosts:    hosts,
	}, nil
}

func (s *Server) applyDRSRecommendation(w http.ResponseWriter, r *http.Request) {
	if s.drs == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "DRS is not available"})
		return
	}
	if s.noCluster(w) {
		return
	}

	hosts, domains := s.collectClusterPlacements(r.Context(), identity(r))
	status := s.drs.Evaluate(r.Context(), hosts, domains, s.placementView())

	recID := r.PathValue("id")
	var rec *drs.Recommendation
	for i := range status.Recommendations {
		if status.Recommendations[i].ID == recID {
			rec = &status.Recommendations[i]
			break
		}
	}
	if rec == nil {
		if s.placement != nil {
			vms := drs.VMRefs(domains)
			for _, d := range domains {
				if d.State != "running" || d.HostID == "" {
					continue
				}
				for _, h := range hosts {
					if !h.Reachable || h.ID == d.HostID {
						continue
					}
					if drs.RecommendationID(d.UUID, d.HostID, h.ID) == recID {
						hard, _ := placement.Check(s.placement.Rules(), vms, d.UUID, h.ID)
						if len(hard) > 0 {
							s.writeJSON(w, http.StatusConflict, map[string]string{
								"error": "recommendation is stale: " + strings.Join(hard, "; "),
							})
							return
						}
					}
				}
			}
		}
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "recommendation not found or already applied"})
		return
	}

	if s.placement != nil {
		vms := drs.VMRefs(domains)
		hard, _ := placement.Check(s.placement.Rules(), vms, rec.DomainUUID, rec.TargetHostID)
		if len(hard) > 0 {
			s.writeJSON(w, http.StatusConflict, map[string]string{
				"error": "recommendation is stale: " + strings.Join(hard, "; "),
			})
			return
		}
	}

	s.applyRecommendation(r.Context(), w, *rec, identity(r))
}

// applyRecommendation starts rec's live migration as id: the machine's host runs it through
// the same path as the Move button (checks, job, activity), and w gets that answer.
func (s *Server) applyRecommendation(ctx context.Context, w http.ResponseWriter, rec drs.Recommendation, id auth.Identity) {
	s.log.Info("applying DRS recommendation", "vm", rec.DomainName, "from", rec.SourceName, "to", rec.TargetName, "by", id.Name)
	movePath := "/api/v1/domains/" + rec.DomainUUID + "/move"
	body := map[string]string{"host": rec.TargetHostID}
	if rec.SourceHostID == s.cluster.SelfID() {
		raw, _ := json.Marshal(body)
		inner, err := http.NewRequestWithContext(context.WithValue(ctx, identityKey, id), http.MethodPost, movePath, bytes.NewReader(raw))
		if err != nil {
			s.writeError(w, err)
			return
		}
		inner.SetPathValue("uuid", rec.DomainUUID)
		s.moveDomain(w, inner)
		return
	}
	src, ok := s.cluster.Member(rec.SourceHostID)
	if !ok {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "the machine's host is no longer a cluster member"})
		return
	}
	var job json.RawMessage
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := s.peer(src).As(id.Name, id.Role.String(), id.Token).Call(c, http.MethodPost, movePath, body, &job); err != nil {
		s.writeJSON(w, http.StatusBadGateway, map[string]string{"error": src.Name + ": " + err.Error()})
		return
	}
	// The job runs on the machine's host; the UI follows it at /api/v1/hosts/<id>/jobs/<job>.
	s.writeJSON(w, http.StatusAccepted, map[string]any{
		"message":        "Live migration of " + rec.DomainName + " to " + rec.TargetName + " started on " + src.Name,
		"recommendation": rec,
		"job":            job,
		"jobHost":        src.ID,
	})
}

const (
	// drsInterval is how often fully automated DRS looks at the cluster.
	drsInterval = 5 * time.Minute
	// drsCooldown keeps DRS from moving the same machine again soon (no ping-pong).
	drsCooldown = 30 * time.Minute
)

// RunDRS carries out recommendations by itself while DRS is fully automated (ADR-0057). Each member
// acts only for the machines it runs, one live migration at a time, so members never move
// the same machine twice; the overloaded host sheds its own load.
func (s *Server) RunDRS(ctx context.Context) {
	if s.drs == nil || s.cluster == nil {
		return
	}
	moved := map[string]time.Time{}
	t := time.NewTicker(drsInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.autoBalance(ctx, moved, time.Now())
	}
}

func (s *Server) autoBalance(ctx context.Context, moved map[string]time.Time, now time.Time) {
	cfg := s.drs.GetConfig()
	if !cfg.Enabled || cfg.Mode != drs.ModeFullyAutomated || !s.cluster.Clustered() {
		return
	}
	for _, j := range s.jobs.List() {
		if j.State == jobs.Running && (j.Kind == "migrate" || j.Kind == "move") {
			return // one at a time; the next pass sees the new balance
		}
	}
	hosts, domains := s.collectClusterPlacements(ctx, drsIdentity)
	recs := s.drs.Evaluate(ctx, hosts, domains, s.placementView()).Recommendations
	slices.SortStableFunc(recs, func(a, b drs.Recommendation) int { return b.Priority - a.Priority })
	var vms []placement.VMRef
	if s.placement != nil {
		vms = drs.VMRefs(domains)
	}
	for _, rec := range recs {
		if rec.SourceHostID != s.cluster.SelfID() || now.Sub(moved[rec.DomainUUID]) < drsCooldown {
			continue
		}
		if s.placement != nil {
			hard, _ := placement.Check(s.placement.Rules(), vms, rec.DomainUUID, rec.TargetHostID)
			if len(hard) > 0 {
				continue
			}
		}
		moved[rec.DomainUUID] = now
		rw := &statusRecorder{ResponseWriter: discard{}, status: http.StatusOK}
		s.applyRecommendation(ctx, rw, rec, drsIdentity)
		s.record(activity.Entry{
			User: "drs", Method: http.MethodPost, Path: "/api/v1/domains/" + rec.DomainUUID + "/move",
			Status: rw.status, Error: rw.errorMessage(),
		})
		return
	}
}

func (s *Server) placementView() placement.View {
	if s.placement == nil {
		return placement.View{}
	}
	return s.placement.View()
}

// drsIdentity is who automatic DRS acts as.
var drsIdentity = auth.Identity{Name: "drs", Role: auth.Admin}

// discard is a ResponseWriter for answers nobody reads (automatic actions).
type discard struct{}

func (discard) Header() http.Header         { return http.Header{} }
func (discard) Write(b []byte) (int, error) { return len(b), nil }
func (discard) WriteHeader(int)             {}

// drsPlacement suggests the member a new machine fits best while DRS places machines
// (partially or fully automated). No suggestion: 204.
func (s *Server) drsPlacement(w http.ResponseWriter, r *http.Request) {
	if s.drs == nil || s.cluster == nil || !s.cluster.Clustered() {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if cfg := s.drs.GetConfig(); !cfg.Enabled || cfg.Mode == drs.ModeManual {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	mib, _ := strconv.ParseUint(r.URL.Query().Get("memoryMiB"), 10, 64)
	uuid := r.URL.Query().Get("uuid")
	hosts, domains := s.collectClusterPlacements(r.Context(), identity(r))

	// An empty uuid represents a brand-new machine, which no rule references yet —
	// only vm_host groups and labels could apply, so behaviour is unchanged.
	if uuid == "" || s.placement == nil {
		best, ok := drs.Placement(hosts, mib<<20)
		if !ok {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]any{
			"hostId":     best.ID,
			"hostName":   best.Name,
			"cpuPct":     best.CPUPct,
			"memPct":     best.MemPct,
			"softMissed": []string{},
		})
		return
	}

	// Build candidates from reachable hosts.
	var candidateIDs []string
	hostMap := make(map[string]drs.HostMetrics)
	for _, h := range hosts {
		if h.Reachable {
			candidateIDs = append(candidateIDs, h.ID)
			hostMap[h.ID] = h
		}
	}

	rules := s.placement.Rules()
	vms := make([]placement.VMRef, len(domains))
	for i, d := range domains {
		vms[i] = placement.VMRef{
			UUID:    d.UUID,
			Name:    d.Name,
			HostID:  d.HostID,
			Running: d.State == "running",
		}
	}

	okHosts, _ := placement.Rank(rules, vms, uuid, candidateIDs)
	if len(okHosts) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Group ok candidates by soft violations count (score tier) so that should-satisfying
	// hosts are preferred over higher-loaded ones, while preserving load-based tie-breaking
	// and memory fit check via drs.Placement within each tier.
	type tier struct {
		metrics []drs.HostMetrics
		soft    map[string][]string
	}
	tierByCount := make(map[int]*tier)
	var counts []int

	for _, hID := range okHosts {
		h, ok := hostMap[hID]
		if !ok {
			continue
		}
		_, soft := placement.Check(rules, vms, uuid, hID)
		cnt := len(soft)
		t, exists := tierByCount[cnt]
		if !exists {
			counts = append(counts, cnt)
			t = &tier{soft: make(map[string][]string)}
			tierByCount[cnt] = t
		}
		t.metrics = append(t.metrics, h)
		t.soft[hID] = soft
	}
	slices.Sort(counts) // fewest soft violations (best score) first

	var best drs.HostMetrics
	var found bool
	var softMissed []string

	for _, cnt := range counts {
		t := tierByCount[cnt]
		candidate, ok := drs.Placement(t.metrics, mib<<20)
		if ok {
			best = candidate
			found = true
			softMissed = t.soft[candidate.ID]
			break
		}
	}

	if !found {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if softMissed == nil {
		softMissed = []string{}
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"hostId":     best.ID,
		"hostName":   best.Name,
		"cpuPct":     best.CPUPct,
		"memPct":     best.MemPct,
		"softMissed": softMissed,
	})
}

// getClusterInventoryMachines returns all machines across all nodes in the cluster.
func (s *Server) getClusterInventoryMachines(w http.ResponseWriter, r *http.Request) {
	out, err := s.clusterDomains(r.Context(), identity(r))
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, out)
}

// clusterDomains lists the machines of every member (a member that does not answer within
// a few seconds is left out).
func (s *Server) clusterDomains(ctx context.Context, id auth.Identity) ([]ClusterDomainSummary, error) {
	selfID := ""
	if s.cluster != nil {
		selfID = s.cluster.SelfID()
	}

	localVMs, err := s.backend.ListDomains()
	if err != nil {
		return nil, err
	}
	out := make([]ClusterDomainSummary, 0, len(localVMs))
	for _, vm := range localVMs {
		out = append(out, ClusterDomainSummary{Domain: vm, HostID: selfID, HostName: s.hostName})
	}

	if s.cluster != nil && s.cluster.Clustered() {
		members, _ := s.cluster.Members()
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, node := range members {
			if node.ID == selfID {
				continue
			}
			wg.Add(1)
			go func(n cluster.Node) {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
				defer cancel()
				var remote struct {
					Domains []virt.Domain `json:"domains"`
				}
				if err := s.peer(n).As(id.Name, id.Role.String(), id.Token).Call(ctx, http.MethodGet, "/api/v1/domains", nil, &remote); err != nil {
					return
				}
				mu.Lock()
				for _, rvm := range remote.Domains {
					out = append(out, ClusterDomainSummary{Domain: rvm, HostID: n.ID, HostName: n.Name})
				}
				mu.Unlock()
			}(node)
		}
		wg.Wait()
	}

	return out, nil
}

// placements turns one host's live snapshot into DRS input.
func placements(id, name string, snap live.Snapshot) (drs.HostMetrics, []drs.DomainPlacement) {
	h := drs.HostMetrics{
		ID: id, Name: name, Reachable: true,
		CPUPct:    snap.Host.CPUPercent,
		MemUsedB:  snap.Host.MemUsedKiB << 10,
		MemTotalB: snap.Host.MemTotalKiB << 10,
		VMCount:   len(snap.Domains),
	}
	if snap.Host.MemTotalKiB > 0 {
		h.MemPct = float64(snap.Host.MemUsedKiB) * 100 / float64(snap.Host.MemTotalKiB)
	}
	doms := make([]drs.DomainPlacement, 0, len(snap.Domains))
	for _, d := range snap.Domains {
		if d.Kind == "container" || d.GPUPool != "" {
			continue // containers and machines holding this host's GPUs do not live-migrate
		}
		doms = append(doms, drs.DomainPlacement{
			UUID: d.UUID, Name: d.Name, HostID: id,
			State:    strings.ToLower(string(d.State)),
			CPUPct:   d.CPUPercent,
			MemUsedB: d.MemoryKiB << 10,
		})
	}
	return h, doms
}

// collectClusterPlacements reads every member's live snapshot, asking the others on behalf
// of id (members answer only the cluster protocol on their own behalf).
func (s *Server) collectClusterPlacements(ctx context.Context, id auth.Identity) ([]drs.HostMetrics, []drs.DomainPlacement) {
	selfID := ""
	if s.cluster != nil {
		selfID = s.cluster.SelfID()
	}

	var local live.Snapshot
	backlog, _, unsubscribe := s.live.Subscribe()
	unsubscribe()
	if len(backlog) > 0 {
		_ = json.Unmarshal(backlog[len(backlog)-1], &local)
	}
	self, domains := placements(selfID, s.hostName, local)
	hosts := []drs.HostMetrics{self}

	if s.cluster != nil && s.cluster.Clustered() {
		members, _ := s.cluster.Members()
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, node := range members {
			if node.ID == selfID {
				continue
			}
			wg.Add(1)
			go func(n cluster.Node) {
				defer wg.Done()
				c, cancel := context.WithTimeout(ctx, 3*time.Second)
				defer cancel()
				var snap live.Snapshot
				err := s.peer(n).As(id.Name, id.Role.String(), id.Token).Call(c, http.MethodGet, "/api/v1/summary", nil, &snap)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					hosts = append(hosts, drs.HostMetrics{ID: n.ID, Name: n.Name})
					return
				}
				h, d := placements(n.ID, n.Name, snap)
				hosts = append(hosts, h)
				domains = append(domains, d...)
			}(node)
		}
		wg.Wait()
	}
	return hosts, domains
}
