package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAPI answers the calls the provider makes, enough to test the client's logic.
type fakeAPI struct {
	mu             sync.Mutex
	calls          []string
	state          string
	settings       map[string]any
	projects       map[string]Project
	placementRules map[string]PlacementRule
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.RequestURI())
	if r.Header.Get("Authorization") != "Bearer tok" {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"sign in required"}`))
		return
	}
	switch {
	case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/domains"):
		_ = json.NewEncoder(w).Encode(map[string]any{"domains": []map[string]any{
			{"uuid": "11111111-1111-1111-1111-111111111111", "name": "web", "state": f.state, "vcpus": 2, "maxMemoryKiB": 2 << 20},
			{"uuid": "22222222-2222-2222-2222-222222222222", "name": "deb13-tpl", "state": "shutoff", "template": true},
		}})
	case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy"):
		_, _ = w.Write([]byte(`{"id":"j1","state":"running"}`))
	case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/jobs/j1"):
		_, _ = w.Write([]byte(`{"id":"j1","state":"succeeded","result":"33333333-3333-3333-3333-333333333333"}`))
	case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/actions/shutdown"):
		// the guest ignores the power button
	case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/actions/force-off"):
		f.state = "shutoff"
	case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/settings"):
		_, _ = w.Write([]byte(`{"vcpus":2,"maxVcpus":2,"sockets":1,"cores":2,"threads":1,"memoryMiB":2048,"currentMemoryMiB":2048,"bootDevices":[{"key":"disk:vda"}],"title":"keep me"}`))
	case r.Method == "PUT" && strings.HasSuffix(r.URL.Path, "/settings"):
		_ = json.NewDecoder(r.Body).Decode(&f.settings)
	case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/domains"):
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":"A machine named web already exists."}`))
	case r.Method == "GET" && r.URL.Path == "/api/v1/projects":
		list := make([]Project, 0, len(f.projects))
		for _, p := range f.projects {
			list = append(list, p)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"projects": list})
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/api/v1/projects/"):
		id := strings.TrimPrefix(r.URL.Path, "/api/v1/projects/")
		p, ok := f.projects[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not found"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(p)
	case r.Method == "POST" && r.URL.Path == "/api/v1/projects":
		var p Project
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if f.projects == nil {
			f.projects = make(map[string]Project)
		}
		f.projects[p.ID] = p
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"project": p})
	case r.Method == "PUT" && strings.HasPrefix(r.URL.Path, "/api/v1/projects/"):
		id := strings.TrimPrefix(r.URL.Path, "/api/v1/projects/")
		if _, ok := f.projects[id]; !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var p Project
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		p.ID = id
		f.projects[id] = p
		_ = json.NewEncoder(w).Encode(map[string]any{"project": p})
	case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/api/v1/projects/"):
		id := strings.TrimPrefix(r.URL.Path, "/api/v1/projects/")
		if _, ok := f.projects[id]; !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		delete(f.projects, id)
		w.WriteHeader(http.StatusNoContent)
	case r.Method == "GET" && r.URL.Path == "/api/v1/placement":
		list := make([]PlacementRule, 0, len(f.placementRules))
		for _, pr := range f.placementRules {
			list = append(list, pr)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"rules":      list,
			"hostLabels": map[string]map[string]string{},
			"violations": []any{},
		})
	case r.Method == "POST" && r.URL.Path == "/api/v1/placement/rules":
		var pr PlacementRule
		if err := json.NewDecoder(r.Body).Decode(&pr); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if pr.ID == "" {
			pr.ID = "1234567890abcdef"
		}
		if f.placementRules == nil {
			f.placementRules = make(map[string]PlacementRule)
		}
		f.placementRules[pr.ID] = pr
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(pr)
	case r.Method == "PUT" && strings.HasPrefix(r.URL.Path, "/api/v1/placement/rules/"):
		id := strings.TrimPrefix(r.URL.Path, "/api/v1/placement/rules/")
		if _, ok := f.placementRules[id]; !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var pr PlacementRule
		if err := json.NewDecoder(r.Body).Decode(&pr); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		pr.ID = id
		f.placementRules[id] = pr
		_ = json.NewEncoder(w).Encode(pr)
	case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/api/v1/placement/rules/"):
		id := strings.TrimPrefix(r.URL.Path, "/api/v1/placement/rules/")
		if _, ok := f.placementRules[id]; !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		delete(f.placementRules, id)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func newTest(t *testing.T) (*Client, *fakeAPI) {
	t.Helper()
	f := &fakeAPI{
		state:          "running",
		projects:       make(map[string]Project),
		placementRules: make(map[string]PlacementRule),
	}
	srv := httptest.NewTLSServer(f)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "tok", true)
	if err != nil {
		t.Fatal(err)
	}
	c.Poll = 10 * time.Millisecond
	return c, f
}

func TestDeployAndResize(t *testing.T) {
	c, f := newTest(t)
	ctx := context.Background()
	tpl, err := c.DomainByName(ctx, "deb13-tpl")
	if err != nil {
		t.Fatal(err)
	}
	uuid, err := c.Deploy(ctx, tpl.UUID, DeploySpec{Name: "web2", Linked: true})
	if err != nil || uuid != "33333333-3333-3333-3333-333333333333" {
		t.Fatalf("%q %v", uuid, err)
	}
	if err := c.Resize(ctx, uuid, 4, 4096); err != nil {
		t.Fatal(err)
	}
	s := f.settings
	if s["vcpus"].(float64) != 4 || s["maxVcpus"].(float64) != 4 || s["cores"].(float64) != 4 || s["memoryMiB"].(float64) != 4096 || s["title"] != "keep me" {
		t.Errorf("settings sent: %v", s)
	}
	if _, ok := s["bootDevices"]; ok {
		t.Error("read-only fields sent back")
	}
}

func TestStopFallsBackToForceOff(t *testing.T) {
	c, f := newTest(t)
	if err := c.Stop(context.Background(), "11111111-1111-1111-1111-111111111111", 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(f.calls, "\n")
	if !strings.Contains(joined, "actions/shutdown") || !strings.Contains(joined, "actions/force-off") {
		t.Errorf("calls:\n%s", joined)
	}
}

func TestErrorsAndHostRouting(t *testing.T) {
	c, f := newTest(t)
	ctx := context.Background()
	_, err := c.CreateDomain(ctx, CreateSpec{Name: "web"})
	var api *APIError
	if !errors.As(err, &api) || api.Status != 422 || !strings.Contains(api.Message, "already exists") {
		t.Errorf("err %v", err)
	}
	if _, err := c.Domain(ctx, "99999999-9999-9999-9999-999999999999"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing machine: %v", err)
	}
	c.Host = "hv2"
	_, _ = c.Domains(ctx)
	if last := f.calls[len(f.calls)-1]; last != "GET /api/v1/hosts/hv2/domains" {
		t.Errorf("cluster routing: %s", last)
	}
	bad, _ := New(strings.Replace(c.base, "https", "https", 1), "wrong", true)
	if _, err := bad.Domains(ctx); !errors.As(err, &api) || api.Status != 401 {
		t.Errorf("bad token: %v", err)
	}
	if _, err := New("hv1:8420", "", false); err == nil {
		t.Error("url without scheme accepted")
	}
}

func TestProjectCRUD(t *testing.T) {
	c, _ := newTest(t)
	ctx := context.Background()

	// Initial list should be empty
	projects, err := c.Projects(ctx)
	if err != nil {
		t.Fatalf("Projects(): %v", err)
	}
	if len(projects) != 0 {
		t.Fatalf("expected 0 projects, got %d", len(projects))
	}

	// Create
	newProj := Project{
		ID:          "dev",
		Name:        "Development",
		Description: "Dev environment",
		Limits: &ProjectLimits{
			Machines:  5,
			VCPUs:     10,
			MemoryMiB: 8192,
			DiskGiB:   100,
		},
		Members: []ProjectMember{
			{Kind: "user", Name: "alice", Role: "admin"},
			{Kind: "group", Name: "devs", Role: "operator"},
		},
		StoragePools: []string{"default", "fast"},
		Networks:     []string{"default"},
	}
	if err := c.CreateProject(ctx, newProj); err != nil {
		t.Fatalf("CreateProject(): %v", err)
	}

	// Read one
	p, err := c.Project(ctx, "dev")
	if err != nil {
		t.Fatalf("Project(dev): %v", err)
	}
	if p.ID != "dev" || p.Name != "Development" || p.Description != "Dev environment" {
		t.Errorf("unexpected project: %+v", p)
	}
	if p.Limits == nil {
		t.Fatal("expected non-nil limits")
	}
	if p.Limits.Machines != 5 || p.Limits.VCPUs != 10 || p.Limits.MemoryMiB != 8192 || p.Limits.DiskGiB != 100 {
		t.Errorf("unexpected limits: %+v", p.Limits)
	}
	if len(p.Members) != 2 || p.Members[0].Name != "alice" || p.Members[1].Name != "devs" {
		t.Errorf("unexpected members: %+v", p.Members)
	}
	if len(p.StoragePools) != 2 || p.StoragePools[0] != "default" || p.StoragePools[1] != "fast" {
		t.Errorf("unexpected storage pools: %+v", p.StoragePools)
	}
	if len(p.Networks) != 1 || p.Networks[0] != "default" {
		t.Errorf("unexpected networks: %+v", p.Networks)
	}

	// List
	projects, err = c.Projects(ctx)
	if err != nil {
		t.Fatalf("Projects(): %v", err)
	}
	if len(projects) != 1 || projects[0].ID != "dev" {
		t.Errorf("unexpected projects list: %+v", projects)
	}

	// Update
	updateProj := p
	updateProj.Name = "Dev Team"
	if updateProj.Limits != nil {
		updateProj.Limits.VCPUs = 20
	}
	if err := c.UpdateProject(ctx, "dev", updateProj); err != nil {
		t.Fatalf("UpdateProject(): %v", err)
	}

	pUpdated, err := c.Project(ctx, "dev")
	if err != nil {
		t.Fatalf("Project(dev) after update: %v", err)
	}
	if pUpdated.Name != "Dev Team" || pUpdated.Limits == nil || pUpdated.Limits.VCPUs != 20 {
		t.Errorf("project not updated: %+v", pUpdated)
	}

	// Delete
	if err := c.DeleteProject(ctx, "dev"); err != nil {
		t.Fatalf("DeleteProject(): %v", err)
	}

	// Read after delete -> ErrNotFound
	if _, err := c.Project(ctx, "dev"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got: %v", err)
	}
}

func TestPlacementRuleCRUD(t *testing.T) {
	c, _ := newTest(t)
	ctx := context.Background()

	// Initial list should be empty
	rules, err := c.PlacementRules(ctx)
	if err != nil {
		t.Fatalf("PlacementRules(): %v", err)
	}
	if len(rules) != 0 {
		t.Fatalf("expected 0 rules, got %d", len(rules))
	}

	// Create
	rule := PlacementRule{
		Name:     "anti-web",
		Kind:     PlacementVMAntiAffinity,
		Enabled:  true,
		Strength: PlacementStrengthMust,
		Domains:  []string{"uuid-1", "uuid-2"},
	}
	created, err := c.CreatePlacementRule(ctx, rule)
	if err != nil {
		t.Fatalf("CreatePlacementRule(): %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected non-empty rule ID")
	}
	if created.Name != "anti-web" || created.Kind != PlacementVMAntiAffinity || !created.Enabled || created.Strength != PlacementStrengthMust {
		t.Errorf("unexpected created rule: %+v", created)
	}

	// Read one
	r, err := c.PlacementRule(ctx, created.ID)
	if err != nil {
		t.Fatalf("PlacementRule(%s): %v", created.ID, err)
	}
	if r.ID != created.ID || r.Name != "anti-web" || len(r.Domains) != 2 {
		t.Errorf("unexpected read rule: %+v", r)
	}

	// List
	rules, err = c.PlacementRules(ctx)
	if err != nil {
		t.Fatalf("PlacementRules(): %v", err)
	}
	if len(rules) != 1 || rules[0].ID != created.ID {
		t.Errorf("unexpected rules list: %+v", rules)
	}

	// Update
	updateRule := r
	updateRule.Name = "anti-web-soft"
	updateRule.Strength = PlacementStrengthShould
	updateRule.Enabled = false
	if err := c.UpdatePlacementRule(ctx, created.ID, updateRule); err != nil {
		t.Fatalf("UpdatePlacementRule(): %v", err)
	}

	rUpdated, err := c.PlacementRule(ctx, created.ID)
	if err != nil {
		t.Fatalf("PlacementRule(%s) after update: %v", created.ID, err)
	}
	if rUpdated.Name != "anti-web-soft" || rUpdated.Strength != PlacementStrengthShould || rUpdated.Enabled {
		t.Errorf("rule not updated properly: %+v", rUpdated)
	}

	// Delete
	if err := c.DeletePlacementRule(ctx, created.ID); err != nil {
		t.Fatalf("DeletePlacementRule(): %v", err)
	}

	// Read after delete -> ErrNotFound
	if _, err := c.PlacementRule(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got: %v", err)
	}
}

func TestProjectOmitsLimits(t *testing.T) {
	c, _ := newTest(t)
	ctx := context.Background()

	newProj := Project{
		ID:          "nolim",
		Name:        "No Limits",
		Description: "Project without limits",
		Members: []ProjectMember{
			{Kind: "user", Name: "bob", Role: "viewer"},
		},
		StoragePools: []string{"default"},
		Networks:     []string{"default"},
	}
	if err := c.CreateProject(ctx, newProj); err != nil {
		t.Fatalf("CreateProject(): %v", err)
	}

	p, err := c.Project(ctx, "nolim")
	if err != nil {
		t.Fatalf("Project(nolim): %v", err)
	}
	if p.Limits != nil {
		t.Errorf("expected nil limits when not set, got: %+v", p.Limits)
	}
}

func TestPlacementRuleClearHosts(t *testing.T) {
	c, _ := newTest(t)
	ctx := context.Background()

	hosts := []string{"host1", "host2"}
	rule := PlacementRule{
		Name:    "host-rule",
		Kind:    PlacementVMHost,
		Domains: []string{"uuid-1"},
		Hosts:   &hosts,
	}
	created, err := c.CreatePlacementRule(ctx, rule)
	if err != nil {
		t.Fatalf("CreatePlacementRule(): %v", err)
	}
	if created.Hosts == nil || len(*created.Hosts) != 2 {
		t.Fatalf("expected 2 hosts, got: %v", created.Hosts)
	}

	// Clear hosts by sending an empty (non-nil) slice
	created.Hosts = &[]string{}
	if err := c.UpdatePlacementRule(ctx, created.ID, created); err != nil {
		t.Fatalf("UpdatePlacementRule(): %v", err)
	}

	r, err := c.PlacementRule(ctx, created.ID)
	if err != nil {
		t.Fatalf("PlacementRule(%s): %v", created.ID, err)
	}
	if r.Hosts == nil {
		t.Fatal("expected non-nil hosts pointer after clearing")
	}
	if len(*r.Hosts) != 0 {
		t.Errorf("expected empty hosts, got: %v", *r.Hosts)
	}

	// Set hosts to nil (omitted from JSON) — should not clear, just untouched
	r.Hosts = nil
	if err := c.UpdatePlacementRule(ctx, r.ID, r); err != nil {
		t.Fatalf("UpdatePlacementRule() with nil hosts: %v", err)
	}
	r2, err := c.PlacementRule(ctx, r.ID)
	if err != nil {
		t.Fatalf("PlacementRule(%s): %v", r.ID, err)
	}
	if r2.Hosts != nil {
		t.Errorf("expected nil hosts after unsetting, got: %v", *r2.Hosts)
	}
}
