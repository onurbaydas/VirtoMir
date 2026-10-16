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
	mu       sync.Mutex
	calls    []string
	state    string
	settings map[string]any
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
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func newTest(t *testing.T) (*Client, *fakeAPI) {
	t.Helper()
	f := &fakeAPI{state: "running"}
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
