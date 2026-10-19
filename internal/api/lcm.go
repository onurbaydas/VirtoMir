package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"time"

	"github.com/onurbaydas/virtomir/internal/auth"
	"github.com/onurbaydas/virtomir/internal/cluster"
	"github.com/onurbaydas/virtomir/internal/lcm"
	"github.com/onurbaydas/virtomir/internal/placement"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// Rolling cluster upgrades (ADR-0070): the orchestrator runs on the member whose admin
// started it and acts on every member as that admin, over the same API the UI uses.

func (s *Server) lcmRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/cluster/lcm", s.lcmStatus)
	mux.HandleFunc("POST /api/v1/cluster/lcm/precheck", s.lcmPrecheck)
	mux.HandleFunc("POST /api/v1/cluster/lcm/start", s.lcmStart)
	mux.HandleFunc("GET /api/v1/cluster/lcm/plans/{id}", s.lcmPlan)
	mux.HandleFunc("POST /api/v1/cluster/lcm/plans/{id}/pause", s.lcmPause)
	mux.HandleFunc("POST /api/v1/cluster/lcm/plans/{id}/resume", s.lcmResume)
	mux.HandleFunc("POST /api/v1/cluster/lcm/plans/{id}/abort", s.lcmAbort)
}

// lcmState is the orchestrator of the plan that runs here, if any.
type lcmState struct {
	mu   sync.Mutex
	orch *lcm.Orchestrator
	plan string
}

func (s *Server) noLCM(w http.ResponseWriter) bool {
	if s.lcm == nil || s.cluster == nil {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "rolling upgrades are not available on this host"})
		return true
	}
	if !s.cluster.Clustered() {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "this host is not in a cluster; update it on its Updates card"})
		return true
	}
	return false
}

func (s *Server) lcmStatus(w http.ResponseWriter, _ *http.Request) {
	if s.lcm == nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "plans": []lcm.Plan{}})
		return
	}
	out := map[string]any{"enabled": s.cluster != nil && s.cluster.Clustered(), "plans": s.lcm.Plans()}
	if p, ok := s.lcm.Active(); ok {
		out["activePlan"] = p
	}
	s.writeJSON(w, http.StatusOK, out)
}

func (s *Server) lcmPlan(w http.ResponseWriter, r *http.Request) {
	if s.lcm == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such plan"})
		return
	}
	p, ok := s.lcm.Plan(r.PathValue("id"))
	if !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such plan"})
		return
	}
	s.writeJSON(w, http.StatusOK, p)
}

// newOrchestrator acts as id on the members.
func (s *Server) newOrchestrator(id auth.Identity) *lcm.Orchestrator {
	return lcm.New(lcm.Options{Driver: &lcmDriver{s: s, id: id}, Store: s.lcm, SelfID: s.cluster.SelfID(), Log: s.log})
}

func (s *Server) lcmPrecheck(w http.ResponseWriter, r *http.Request) {
	if s.noLCM(w) {
		return
	}
	var b struct {
		TargetVersion string `json:"targetVersion"`
	}
	if !s.decodeOptional(w, r, &b) {
		return
	}
	o := s.newOrchestrator(identity(r))
	d := &lcmDriver{s: s, id: identity(r)}
	members, _ := d.Members(r.Context())
	plan := lcm.Plan{TargetVersion: b.TargetVersion}
	checks := make([]lcm.HostCheck, len(members))
	var wg sync.WaitGroup
	for i, m := range members {
		wg.Add(1)
		go func() {
			defer wg.Done()
			checks[i] = o.Precheck(r.Context(), plan, m, members)
		}()
	}
	wg.Wait()
	s.writeJSON(w, http.StatusOK, map[string]any{"checks": checks})
}

func (s *Server) lcmStart(w http.ResponseWriter, r *http.Request) {
	if s.noLCM(w) {
		return
	}
	var b struct {
		TargetVersion string `json:"targetVersion"`
		OSUpdates     bool   `json:"osUpdates"`
		Reboot        bool   `json:"reboot"`
		MoveBack      bool   `json:"moveBack"`
	}
	if !s.decodeOptional(w, r, &b) {
		return
	}
	if _, busy := s.lcm.Active(); busy {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": lcm.ErrBusy.Error()})
		return
	}
	id := identity(r)
	d := &lcmDriver{s: s, id: id}
	members, _ := d.Members(r.Context())
	var versions []string
	for _, m := range members {
		if m.Version == "" {
			s.writeJSON(w, http.StatusConflict, map[string]string{"error": m.Name + " does not answer; every member must be up for a rolling upgrade"})
			return
		}
		versions = append(versions, m.Version)
	}
	if b.TargetVersion != "" {
		versions = append(versions, b.TargetVersion)
	}
	if !lcm.SameMajor(versions) {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "the members and the target version differ in their major version; upgrade one major version at a time"})
		return
	}
	self := s.cluster.SelfID()
	var order []string
	for _, m := range members {
		if m.ID != self {
			order = append(order, m.ID)
		}
	}
	order = append(order, self) // this host last: it keeps the plan going the longest
	plan := lcm.Plan{ID: lcmPlanID(), Started: time.Now().UTC(), State: lcm.PlanPlanned, HostOrder: order,
		TargetVersion: b.TargetVersion, OSUpdates: b.OSUpdates, Reboot: b.Reboot, MoveBack: b.MoveBack,
		StartedBy: id.Name, StartedByRole: id.Role.String(), Maintenance: []string{}}
	if err := s.lcm.SavePlan(plan); err != nil {
		s.writeError(w, err)
		return
	}
	if err := s.runPlan(plan.ID, id); err != nil {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	plan, _ = s.lcm.Plan(plan.ID)
	s.writeJSON(w, http.StatusAccepted, plan)
}

func lcmPlanID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// runPlan starts plan id with its own orchestrator, outside any request's lifetime.
func (s *Server) runPlan(planID string, id auth.Identity) error {
	o := s.newOrchestrator(id)
	if err := o.Start(s.lcmCtx(), planID); err != nil {
		return err
	}
	s.lcmRun.mu.Lock()
	s.lcmRun.orch, s.lcmRun.plan = o, planID
	s.lcmRun.mu.Unlock()
	return nil
}

func (s *Server) lcmCtx() context.Context {
	if s.runCtx != nil {
		return s.runCtx
	}
	return context.Background()
}

func (s *Server) lcmPause(w http.ResponseWriter, r *http.Request) {
	s.lcmControl(w, r, (*lcm.Orchestrator).Pause)
}

func (s *Server) lcmResume(w http.ResponseWriter, r *http.Request) {
	s.lcmControl(w, r, (*lcm.Orchestrator).Resume)
}

func (s *Server) lcmAbort(w http.ResponseWriter, r *http.Request) {
	s.lcmControl(w, r, (*lcm.Orchestrator).Abort)
}

func (s *Server) lcmControl(w http.ResponseWriter, r *http.Request, action func(*lcm.Orchestrator, string) error) {
	if s.noLCM(w) {
		return
	}
	planID := r.PathValue("id")
	s.lcmRun.mu.Lock()
	o := s.lcmRun.orch
	running := s.lcmRun.plan == planID
	s.lcmRun.mu.Unlock()
	if o == nil || !running {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "this plan is not running on this host"})
		return
	}
	if err := action(o, planID); err != nil {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	p, _ := s.lcm.Plan(planID)
	s.writeJSON(w, http.StatusOK, p)
}

// RunLCM is called once before the API serves: it continues, in the background, a plan
// this host was running when it restarted (it upgrades itself last, so it restarts in the
// middle of its own plan).
func (s *Server) RunLCM(ctx context.Context) {
	s.runCtx = ctx
	if s.lcm == nil || s.cluster == nil {
		return
	}
	p, ok := s.lcm.Active()
	if !ok || len(p.HostOrder) == 0 || p.HostOrder[len(p.HostOrder)-1] != s.cluster.SelfID() {
		return
	}
	role, err := auth.ParseRole(p.StartedByRole)
	if err != nil {
		role = auth.Admin
	}
	id := auth.Identity{Name: p.StartedBy, Role: role}
	go func() {
		// Give the members a moment to see this host again.
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
		if err := s.runPlan(p.ID, id); err != nil {
			s.log.Warn("rolling upgrade not continued", "plan", p.ID, "err", err)
			return
		}
		s.log.Info("rolling upgrade continued after this host restarted", "plan", p.ID)
	}()
}

// lcmDriver is lcm.Driver over the members' API, acting as id.
type lcmDriver struct {
	s  *Server
	id auth.Identity
}

// call runs an API request on host hostID: in-process for this host, over the peer
// protocol for the others.
func (d *lcmDriver) call(ctx context.Context, hostID, method, path string, in, out any) error {
	s := d.s
	if hostID == s.cluster.SelfID() {
		var body io.Reader = http.NoBody
		if in != nil {
			raw, err := json.Marshal(in)
			if err != nil {
				return err
			}
			body = bytes.NewReader(raw)
		}
		req, err := http.NewRequestWithContext(context.WithValue(ctx, identityKey, d.id), method, path, body)
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		if s.inner == nil {
			return errors.New("the API is not serving yet")
		}
		s.inner.ServeHTTP(rec, req)
		if rec.Code >= 300 {
			var e struct{ Error string }
			_ = json.Unmarshal(rec.Body.Bytes(), &e)
			return &cluster.StatusError{Code: rec.Code, Message: cmpOrStr(e.Error, http.StatusText(rec.Code))}
		}
		if out != nil && rec.Body.Len() > 0 {
			return json.Unmarshal(rec.Body.Bytes(), out)
		}
		return nil
	}
	n, ok := s.cluster.Member(hostID)
	if !ok {
		return fmt.Errorf("%s is no longer a cluster member", hostID)
	}
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return s.peer(n).As(d.id.Name, d.id.Role.String(), d.id.Token).Call(c, method, path, in, out)
}

func cmpOrStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (d *lcmDriver) Members(ctx context.Context) ([]lcm.Member, error) {
	nodes, _ := d.s.cluster.Members()
	out := make([]lcm.Member, len(nodes))
	var wg sync.WaitGroup
	for i, n := range nodes {
		out[i] = lcm.Member{ID: n.ID, Name: n.Name}
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i].Version, _ = d.Hello(ctx, n.ID)
		}()
	}
	wg.Wait()
	return out, nil
}

func (d *lcmDriver) Hello(ctx context.Context, hostID string) (string, error) {
	if hostID == d.s.cluster.SelfID() {
		return d.s.version, nil
	}
	n, ok := d.s.cluster.Member(hostID)
	if !ok {
		return "", fmt.Errorf("%s is no longer a cluster member", hostID)
	}
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	h, err := d.s.peer(n).Hello(c)
	if err == nil && h.ID != n.ID {
		err = errors.New("another VirtoMir answers at its address")
	}
	return h.Version, err
}

func (d *lcmDriver) RunningVMs(ctx context.Context, hostID string) ([]lcm.VM, error) {
	var list struct{ Domains []virt.Domain }
	if err := d.call(ctx, hostID, http.MethodGet, "/api/v1/domains", nil, &list); err != nil {
		return nil, err
	}
	var out []lcm.VM
	for _, dom := range list.Domains {
		if dom.Kind == "container" {
			continue // containers are not live-migrated
		}
		vm := lcm.VM{UUID: dom.UUID, Name: dom.Name, MemKiB: dom.MaxMemoryKiB,
			Running: dom.State == virt.StateRunning || dom.State == virt.StatePaused || dom.State == virt.StateBlocked}
		if vm.Running {
			var det virt.DomainDetail
			if err := d.call(ctx, hostID, http.MethodGet, "/api/v1/domains/"+dom.UUID, nil, &det); err == nil {
				vm.HasHostDevice = slices.ContainsFunc(det.Devices, func(x virt.Device) bool { return x.Kind == "hostdev" })
			}
		}
		out = append(out, vm)
	}
	return out, nil
}

func (d *lcmDriver) FreeMemory(ctx context.Context, hostID string) (int64, error) {
	var sum struct {
		Host struct {
			MemTotalKiB int64 `json:"memTotalKiB"`
			MemUsedKiB  int64 `json:"memUsedKiB"`
		} `json:"host"`
	}
	if err := d.call(ctx, hostID, http.MethodGet, "/api/v1/summary", nil, &sum); err != nil {
		return 0, err
	}
	return sum.Host.MemTotalKiB - sum.Host.MemUsedKiB, nil
}

func (d *lcmDriver) RunningJobs(ctx context.Context, hostID string) (int, error) {
	var list struct {
		Jobs []struct{ State string } `json:"jobs"`
	}
	if err := d.call(ctx, hostID, http.MethodGet, "/api/v1/jobs", nil, &list); err != nil {
		return 0, err
	}
	n := 0
	for _, j := range list.Jobs {
		if j.State == "running" {
			n++
		}
	}
	return n, nil
}

func (d *lcmDriver) CheckPlacement(ctx context.Context, vmUUID, targetHostID string) ([]string, []string, error) {
	if d.s.placement == nil {
		return nil, nil, nil
	}
	_, domains := d.s.collectClusterPlacements(ctx, d.id)
	vms := make([]placement.VMRef, len(domains))
	for i, dom := range domains {
		vms[i] = placement.VMRef{UUID: dom.UUID, Name: dom.Name, HostID: dom.HostID, Running: dom.State == "running"}
	}
	hard, soft := placement.Check(d.s.placement.Rules(), vms, vmUUID, targetHostID)
	return hard, soft, nil
}

func (d *lcmDriver) MoveVM(ctx context.Context, hostID, uuid, targetHostID string) (string, error) {
	var job struct{ ID string }
	err := d.call(ctx, hostID, http.MethodPost, "/api/v1/domains/"+uuid+"/move", map[string]any{"host": targetHostID}, &job)
	return job.ID, err
}

func (d *lcmDriver) JobState(ctx context.Context, hostID, jobID string) (string, string, error) {
	var job struct{ State, Message string }
	err := d.call(ctx, hostID, http.MethodGet, "/api/v1/jobs/"+jobID, nil, &job)
	return job.State, job.Message, err
}

func (d *lcmDriver) InstallVirtomir(ctx context.Context, hostID string) (string, error) {
	var job struct{ ID string }
	err := d.call(ctx, hostID, http.MethodPost, "/api/v1/update/install", nil, &job)
	return job.ID, err
}

func (d *lcmDriver) UpgradeOS(ctx context.Context, hostID string, reboot bool) (string, error) {
	var job struct{ ID string }
	err := d.call(ctx, hostID, http.MethodPost, "/api/v1/system/upgrade", map[string]bool{"reboot": reboot}, &job)
	return job.ID, err
}

// Quorum matters only while high availability protects machines: without any, a member
// that loses quorum stops nothing.
func (d *lcmDriver) Quorum(context.Context) (bool, int, bool) {
	if d.s.haMgr == nil || d.s.ha == nil {
		return false, 0, false
	}
	st := d.s.haMgr.Status()
	protected := false
	for _, p := range st.Config.VMPolicies {
		protected = protected || p.Enabled
	}
	return protected, st.Quorum.TotalVotes, st.Quorum.WitnessReachable
}
