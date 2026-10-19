package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/onurbaydas/virtomir/internal/activity"
	"github.com/onurbaydas/virtomir/internal/dr"
)

// DRStorer holds the persistent DR configuration: protection groups, recovery
// plans and execution-run history (implemented by *dr.Store).
type DRStorer interface {
	ListProtectionGroups() []dr.ProtectionGroup
	GetProtectionGroup(id string) (dr.ProtectionGroup, bool)
	CreateProtectionGroup(g dr.ProtectionGroup) (dr.ProtectionGroup, error)
	UpdateProtectionGroup(g dr.ProtectionGroup) error
	DeleteProtectionGroup(id string) error
	ListPlans(protectionGroupID string) []dr.RecoveryPlan
	GetPlan(id string) (dr.RecoveryPlan, bool)
	CreatePlan(p dr.RecoveryPlan) (dr.RecoveryPlan, error)
	UpdatePlan(p dr.RecoveryPlan) error
	DeletePlan(id string) error
	ListRuns(planID string) []dr.ExecutionRun
	GetRun(id string) (dr.ExecutionRun, bool)
}

// DROrchestrator runs DR test-failover and test-cleanup operations (implemented by
// *dr.Orchestrator).
type DROrchestrator interface {
	ExecuteTestFailover(ctx context.Context, planID string, report func(dr.StepLog)) (dr.ExecutionRun, error)
	ExecuteTestCleanup(ctx context.Context, planID, runID string, report func(dr.StepLog)) (dr.ExecutionRun, error)
	GetDiskMappings(ctx context.Context, group dr.ProtectionGroup) ([]dr.VMDiskMapping, error)
}

// DRFailoverRunner executes real disaster failover and failback, plus on-demand
// group sync (implemented by *dr.FailoverEngine).
type DRFailoverRunner interface {
	ExecuteRealFailover(ctx context.Context, planID string, opts dr.FailoverOpts, report func(dr.StepLog)) (dr.ExecutionRun, error)
	ExecuteFailback(ctx context.Context, planID string, report func(dr.StepLog)) (dr.ExecutionRun, error)
	SyncGroup(ctx context.Context, groupID string) (time.Time, error)
}

func (s *Server) drRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/dr/protection-groups", s.listProtectionGroups)
	mux.HandleFunc("POST /api/v1/dr/protection-groups", s.createProtectionGroup)
	mux.HandleFunc("GET /api/v1/dr/protection-groups/{id}", s.getProtectionGroup)
	mux.HandleFunc("PUT /api/v1/dr/protection-groups/{id}", s.updateProtectionGroup)
	mux.HandleFunc("DELETE /api/v1/dr/protection-groups/{id}", s.deleteProtectionGroup)
	mux.HandleFunc("POST /api/v1/dr/protection-groups/{id}/sync", s.syncProtectionGroup)

	mux.HandleFunc("GET /api/v1/dr/plans", s.listPlans)
	mux.HandleFunc("POST /api/v1/dr/plans", s.createPlan)
	mux.HandleFunc("GET /api/v1/dr/plans/{id}", s.getPlan)
	mux.HandleFunc("PUT /api/v1/dr/plans/{id}", s.updatePlan)
	mux.HandleFunc("DELETE /api/v1/dr/plans/{id}", s.deletePlan)

	mux.HandleFunc("POST /api/v1/dr/plans/{id}/test-failover", s.testFailoverPlan)
	mux.HandleFunc("POST /api/v1/dr/plans/{id}/test-cleanup", s.testCleanupPlan)
	mux.HandleFunc("POST /api/v1/dr/plans/{id}/failover", s.failoverPlan)
	mux.HandleFunc("POST /api/v1/dr/plans/{id}/failback", s.failbackPlan)
	mux.HandleFunc("GET /api/v1/dr/plans/{id}/runs", s.listPlanRuns)
	mux.HandleFunc("GET /api/v1/dr/runs/{runId}", s.getRun)
}

func (s *Server) drAvailable(w http.ResponseWriter) bool {
	if s.drStore == nil {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "disaster recovery is not available on this host"})
		return false
	}
	return true
}

func (s *Server) drError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, dr.ErrNotFound):
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, dr.ErrInvalid):
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
	case errors.Is(err, dr.ErrBusy):
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case errors.Is(err, dr.ErrConflict):
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	default:
		s.writeError(w, err)
	}
}

func (s *Server) drRPOStatus(group dr.ProtectionGroup) dr.RPOStatus {
	status := dr.RPOStatus{
		GroupID:    group.ID,
		RPOMinutes: group.RPOMinutes,
	}
	if s.zfsRep != nil {
		var latest time.Time
		found := false
		for _, st := range s.zfsRep.List() {
			if st.Spec.TargetHost == group.TargetHost && st.LastSync.After(latest) {
				latest = st.LastSync
				found = true
			}
		}
		if found {
			status = dr.CalculateRPO(group, latest, time.Now())
		}
	}
	return status
}

type protectionGroupResponse struct {
	dr.ProtectionGroup
	RPO dr.RPOStatus `json:"rpoStatus"`
}

type protectionGroupDetailResponse struct {
	dr.ProtectionGroup
	DiskMappings []dr.VMDiskMapping `json:"diskMappings"`
	RPO          dr.RPOStatus       `json:"rpoStatus"`
}

func (s *Server) listProtectionGroups(w http.ResponseWriter, _ *http.Request) {
	if !s.drAvailable(w) {
		return
	}
	groups := s.drStore.ListProtectionGroups()
	out := make([]protectionGroupResponse, 0, len(groups))
	for _, g := range groups {
		out = append(out, protectionGroupResponse{ProtectionGroup: g, RPO: s.drRPOStatus(g)})
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"groups": out})
}

func (s *Server) createProtectionGroup(w http.ResponseWriter, r *http.Request) {
	if !s.drAvailable(w) {
		return
	}
	var g dr.ProtectionGroup
	if !s.decode(w, r, &g) {
		return
	}
	created, err := s.drStore.CreateProtectionGroup(g)
	if err != nil {
		s.drError(w, err)
		return
	}
	id := identity(r)
	s.record(activity.Entry{
		User:   id.Name,
		Token:  id.Token,
		Remote: r.RemoteAddr,
		Method: http.MethodPost,
		Path:   "/api/v1/dr/protection-groups",
		Status: http.StatusCreated,
		Error:  fmt.Sprintf("DR protection group created: %s", created.ID),
	})
	s.writeJSON(w, http.StatusCreated, created)
}

func (s *Server) getProtectionGroup(w http.ResponseWriter, r *http.Request) {
	if !s.drAvailable(w) {
		return
	}
	id := r.PathValue("id")
	g, ok := s.drStore.GetProtectionGroup(id)
	if !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "protection group not found"})
		return
	}
	resp := protectionGroupDetailResponse{
		ProtectionGroup: g,
		RPO:             s.drRPOStatus(g),
	}
	if s.drOrchestrator != nil {
		mappings, err := s.drOrchestrator.GetDiskMappings(r.Context(), g)
		if err != nil {
			s.log.Warn("DR disk mappings", "group", id, "err", err)
		}
		resp.DiskMappings = mappings
	}
	s.writeJSON(w, http.StatusOK, resp)
}

func (s *Server) updateProtectionGroup(w http.ResponseWriter, r *http.Request) {
	if !s.drAvailable(w) {
		return
	}
	var g dr.ProtectionGroup
	if !s.decode(w, r, &g) {
		return
	}
	g.ID = r.PathValue("id")
	if err := s.drStore.UpdateProtectionGroup(g); err != nil {
		s.drError(w, err)
		return
	}
	updated, _ := s.drStore.GetProtectionGroup(g.ID)
	id := identity(r)
	s.record(activity.Entry{
		User:   id.Name,
		Token:  id.Token,
		Remote: r.RemoteAddr,
		Method: http.MethodPut,
		Path:   "/api/v1/dr/protection-groups/" + g.ID,
		Status: http.StatusOK,
		Error:  fmt.Sprintf("DR protection group updated: %s", g.ID),
	})
	s.writeJSON(w, http.StatusOK, updated)
}

func (s *Server) deleteProtectionGroup(w http.ResponseWriter, r *http.Request) {
	if !s.drAvailable(w) {
		return
	}
	id := r.PathValue("id")
	if err := s.drStore.DeleteProtectionGroup(id); err != nil {
		s.drError(w, err)
		return
	}
	id2 := identity(r)
	s.record(activity.Entry{
		User:   id2.Name,
		Token:  id2.Token,
		Remote: r.RemoteAddr,
		Method: http.MethodDelete,
		Path:   "/api/v1/dr/protection-groups/" + id,
		Status: http.StatusNoContent,
		Error:  fmt.Sprintf("DR protection group deleted: %s", id),
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) syncProtectionGroup(w http.ResponseWriter, r *http.Request) {
	if !s.drAvailable(w) {
		return
	}
	groupID := r.PathValue("id")
	if _, ok := s.drStore.GetProtectionGroup(groupID); !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "protection group not found"})
		return
	}
	id := identity(r)
	job := s.jobs.Start("dr-group-sync", "DR sync group "+groupID, func(ctx context.Context, report func(string)) (string, error) {
		if s.drFailover == nil {
			return "", errors.New("DR failover engine is not available")
		}
		synced, err := s.drFailover.SyncGroup(ctx, groupID)
		status := http.StatusOK
		errStr := fmt.Sprintf("dr group sync finished: group=%s lastSync=%s", groupID, synced.UTC().Format(time.RFC3339))
		if err != nil {
			status = http.StatusInternalServerError
			errStr = fmt.Sprintf("dr group sync failed: group=%s err=%v", groupID, err)
		}
		s.record(activity.Entry{
			User:   id.Name,
			Token:  id.Token,
			Method: "JOB",
			Path:   "/api/v1/dr/protection-groups/" + groupID + "/sync",
			Status: status,
			Error:  errStr,
		})
		if s.live != nil {
			s.live.Refresh()
		}
		if err != nil {
			return "", err
		}
		return synced.UTC().Format(time.RFC3339), nil
	})
	s.record(activity.Entry{
		User:   id.Name,
		Token:  id.Token,
		Remote: r.RemoteAddr,
		Method: http.MethodPost,
		Path:   "/api/v1/dr/protection-groups/" + groupID + "/sync",
		Status: http.StatusAccepted,
		Error:  fmt.Sprintf("DR group sync started: group=%s", groupID),
	})
	s.writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) listPlans(w http.ResponseWriter, r *http.Request) {
	if !s.drAvailable(w) {
		return
	}
	plans := s.drStore.ListPlans(r.URL.Query().Get("group"))
	s.writeJSON(w, http.StatusOK, map[string]any{"plans": plans})
}

func (s *Server) createPlan(w http.ResponseWriter, r *http.Request) {
	if !s.drAvailable(w) {
		return
	}
	var p dr.RecoveryPlan
	if !s.decode(w, r, &p) {
		return
	}
	created, err := s.drStore.CreatePlan(p)
	if err != nil {
		s.drError(w, err)
		return
	}
	id := identity(r)
	s.record(activity.Entry{
		User:   id.Name,
		Token:  id.Token,
		Remote: r.RemoteAddr,
		Method: http.MethodPost,
		Path:   "/api/v1/dr/plans",
		Status: http.StatusCreated,
		Error:  fmt.Sprintf("DR recovery plan created: %s", created.ID),
	})
	s.writeJSON(w, http.StatusCreated, created)
}

func (s *Server) getPlan(w http.ResponseWriter, r *http.Request) {
	if !s.drAvailable(w) {
		return
	}
	id := r.PathValue("id")
	p, ok := s.drStore.GetPlan(id)
	if !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "recovery plan not found"})
		return
	}
	s.writeJSON(w, http.StatusOK, p)
}

func (s *Server) updatePlan(w http.ResponseWriter, r *http.Request) {
	if !s.drAvailable(w) {
		return
	}
	var p dr.RecoveryPlan
	if !s.decode(w, r, &p) {
		return
	}
	p.ID = r.PathValue("id")
	if err := s.drStore.UpdatePlan(p); err != nil {
		s.drError(w, err)
		return
	}
	updated, _ := s.drStore.GetPlan(p.ID)
	id := identity(r)
	s.record(activity.Entry{
		User:   id.Name,
		Token:  id.Token,
		Remote: r.RemoteAddr,
		Method: http.MethodPut,
		Path:   "/api/v1/dr/plans/" + p.ID,
		Status: http.StatusOK,
		Error:  fmt.Sprintf("DR recovery plan updated: %s", p.ID),
	})
	s.writeJSON(w, http.StatusOK, updated)
}

func (s *Server) deletePlan(w http.ResponseWriter, r *http.Request) {
	if !s.drAvailable(w) {
		return
	}
	id := r.PathValue("id")
	if err := s.drStore.DeletePlan(id); err != nil {
		s.drError(w, err)
		return
	}
	id2 := identity(r)
	s.record(activity.Entry{
		User:   id2.Name,
		Token:  id2.Token,
		Remote: r.RemoteAddr,
		Method: http.MethodDelete,
		Path:   "/api/v1/dr/plans/" + id,
		Status: http.StatusNoContent,
		Error:  fmt.Sprintf("DR recovery plan deleted: %s", id),
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) planAvailable(w http.ResponseWriter, planID string) (dr.RecoveryPlan, bool) {
	if !s.drAvailable(w) {
		return dr.RecoveryPlan{}, false
	}
	p, ok := s.drStore.GetPlan(planID)
	if !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "recovery plan not found"})
		return dr.RecoveryPlan{}, false
	}
	return p, true
}

func (s *Server) testFailoverPlan(w http.ResponseWriter, r *http.Request) {
	p, ok := s.planAvailable(w, r.PathValue("id"))
	if !ok {
		return
	}
	planID := p.ID
	planName := p.Name
	id := identity(r)
	job := s.jobs.Start("dr-test-failover", "Test failover: "+planName, func(ctx context.Context, report func(string)) (string, error) {
		run, err := s.drOrchestrator.ExecuteTestFailover(ctx, planID, func(sl dr.StepLog) {
			report(sl.Name + ": " + string(sl.Status))
		})
		status := http.StatusOK
		errStr := fmt.Sprintf("dr test failover finished: plan=%s run=%s state=%s", planID, run.ID, run.State)
		if err != nil {
			status = http.StatusInternalServerError
			errStr = fmt.Sprintf("dr test failover failed: plan=%s err=%v", planID, err)
		}
		s.record(activity.Entry{
			User:   id.Name,
			Token:  id.Token,
			Method: "JOB",
			Path:   "/api/v1/dr/plans/" + planID + "/test-failover",
			Status: status,
			Error:  errStr,
		})
		if s.live != nil {
			s.live.Refresh()
		}
		if err != nil {
			return "", err
		}
		return run.ID, nil
	})
	s.record(activity.Entry{
		User:   id.Name,
		Token:  id.Token,
		Remote: r.RemoteAddr,
		Method: http.MethodPost,
		Path:   "/api/v1/dr/plans/" + planID + "/test-failover",
		Status: http.StatusAccepted,
		Error:  fmt.Sprintf("DR test failover started: plan=%s", planID),
	})
	s.writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) testCleanupPlan(w http.ResponseWriter, r *http.Request) {
	planID := r.PathValue("id")
	if !s.drAvailable(w) {
		return
	}
	if _, ok := s.drStore.GetPlan(planID); !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "recovery plan not found"})
		return
	}
	runID := ""
	for _, run := range s.drStore.ListRuns(planID) {
		if run.Mode == dr.ModeTestFailover && (run.State == dr.StateTestActive || run.State == dr.StateRunning) {
			runID = run.ID
			break
		}
	}
	if runID == "" {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no active test failover run for this plan"})
		return
	}
	planName := ""
	if p, ok := s.drStore.GetPlan(planID); ok {
		planName = p.Name
	}
	id := identity(r)
	job := s.jobs.Start("dr-test-cleanup", "Test cleanup: "+planName, func(ctx context.Context, report func(string)) (string, error) {
		run, err := s.drOrchestrator.ExecuteTestCleanup(ctx, planID, runID, func(sl dr.StepLog) {
			report(sl.Name + ": " + string(sl.Status))
		})
		status := http.StatusOK
		errStr := fmt.Sprintf("dr test cleanup finished: plan=%s run=%s state=%s", planID, run.ID, run.State)
		if err != nil {
			status = http.StatusInternalServerError
			errStr = fmt.Sprintf("dr test cleanup failed: plan=%s run=%s err=%v", planID, runID, err)
		}
		s.record(activity.Entry{
			User:   id.Name,
			Token:  id.Token,
			Method: "JOB",
			Path:   "/api/v1/dr/plans/" + planID + "/test-cleanup",
			Status: status,
			Error:  errStr,
		})
		if s.live != nil {
			s.live.Refresh()
		}
		if err != nil {
			return "", err
		}
		return run.ID, nil
	})
	s.record(activity.Entry{
		User:   id.Name,
		Token:  id.Token,
		Remote: r.RemoteAddr,
		Method: http.MethodPost,
		Path:   "/api/v1/dr/plans/" + planID + "/test-cleanup",
		Status: http.StatusAccepted,
		Error:  fmt.Sprintf("DR test cleanup started: plan=%s run=%s", planID, runID),
	})
	s.writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) failoverPlan(w http.ResponseWriter, r *http.Request) {
	p, ok := s.planAvailable(w, r.PathValue("id"))
	if !ok {
		return
	}
	planID := p.ID
	planName := p.Name
	var opts dr.FailoverOpts
	if r.Body != nil && r.ContentLength > 0 {
		if !s.decode(w, r, &opts) {
			return
		}
	}
	id := identity(r)
	job := s.jobs.Start("dr-failover", "Failover: "+planName, func(ctx context.Context, report func(string)) (string, error) {
		run, err := s.drFailover.ExecuteRealFailover(ctx, planID, opts, func(sl dr.StepLog) {
			report(sl.Name + ": " + string(sl.Status))
		})
		status := http.StatusOK
		errStr := fmt.Sprintf("dr failover finished: plan=%s run=%s state=%s", planID, run.ID, run.State)
		if err != nil {
			status = http.StatusInternalServerError
			errStr = fmt.Sprintf("dr failover failed: plan=%s err=%v", planID, err)
		}
		s.record(activity.Entry{
			User:   id.Name,
			Token:  id.Token,
			Method: "JOB",
			Path:   "/api/v1/dr/plans/" + planID + "/failover",
			Status: status,
			Error:  errStr,
		})
		if s.live != nil {
			s.live.Refresh()
		}
		if err != nil {
			return "", err
		}
		return run.ID, nil
	})
	s.record(activity.Entry{
		User:   id.Name,
		Token:  id.Token,
		Remote: r.RemoteAddr,
		Method: http.MethodPost,
		Path:   "/api/v1/dr/plans/" + planID + "/failover",
		Status: http.StatusAccepted,
		Error:  fmt.Sprintf("DR failover started: plan=%s force=%t skipPreSync=%t", planID, opts.Force, opts.SkipPreSync),
	})
	s.writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) failbackPlan(w http.ResponseWriter, r *http.Request) {
	p, ok := s.planAvailable(w, r.PathValue("id"))
	if !ok {
		return
	}
	planID := p.ID
	planName := p.Name
	id := identity(r)
	job := s.jobs.Start("dr-failback", "Failback: "+planName, func(ctx context.Context, report func(string)) (string, error) {
		run, err := s.drFailover.ExecuteFailback(ctx, planID, func(sl dr.StepLog) {
			report(sl.Name + ": " + string(sl.Status))
		})
		status := http.StatusOK
		errStr := fmt.Sprintf("dr failback finished: plan=%s run=%s state=%s", planID, run.ID, run.State)
		if err != nil {
			status = http.StatusInternalServerError
			errStr = fmt.Sprintf("dr failback failed: plan=%s err=%v", planID, err)
		}
		s.record(activity.Entry{
			User:   id.Name,
			Token:  id.Token,
			Method: "JOB",
			Path:   "/api/v1/dr/plans/" + planID + "/failback",
			Status: status,
			Error:  errStr,
		})
		if s.live != nil {
			s.live.Refresh()
		}
		if err != nil {
			return "", err
		}
		return run.ID, nil
	})
	s.record(activity.Entry{
		User:   id.Name,
		Token:  id.Token,
		Remote: r.RemoteAddr,
		Method: http.MethodPost,
		Path:   "/api/v1/dr/plans/" + planID + "/failback",
		Status: http.StatusAccepted,
		Error:  fmt.Sprintf("DR failback started: plan=%s", planID),
	})
	s.writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) listPlanRuns(w http.ResponseWriter, r *http.Request) {
	planID := r.PathValue("id")
	if !s.drAvailable(w) {
		return
	}
	if _, ok := s.drStore.GetPlan(planID); !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "recovery plan not found"})
		return
	}
	runs := s.drStore.ListRuns(planID)
	s.writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	if !s.drAvailable(w) {
		return
	}
	runID := r.PathValue("runId")
	run, ok := s.drStore.GetRun(runID)
	if !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "execution run not found"})
		return
	}
	s.writeJSON(w, http.StatusOK, run)
}

// RunDRRPO checks DR RPO compliance every 60 seconds, posting notice (through
// the alert channels) when a protection group falls behind its RPO target.
func (s *Server) RunDRRPO(ctx context.Context) {
	store, ok := s.drStore.(*dr.Store)
	if !ok {
		return
	}
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			dr.CheckRPOCompliance(store, s.drLastSync, s.alerts, time.Now())
		}
	}
}

func (s *Server) drLastSync(group dr.ProtectionGroup) (time.Time, error) {
	if s.zfsRep == nil {
		return time.Time{}, dr.ErrNotFound
	}
	var latest time.Time
	found := false
	for _, st := range s.zfsRep.List() {
		if st.Spec.TargetHost == group.TargetHost && st.LastSync.After(latest) {
			latest = st.LastSync
			found = true
		}
	}
	if !found {
		return time.Time{}, dr.ErrNotFound
	}
	return latest, nil
}
