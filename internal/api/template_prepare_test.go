package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/onurbaydas/virtomir/internal/virt"
)

// prepBackend is a machine with a guest agent that shuts down when asked.
type prepBackend struct {
	*fakeBackend
	mu        sync.Mutex
	info      virt.GuestInfo
	execs     []string
	templated bool
}

func (p *prepBackend) GuestInfo(string) (virt.GuestInfo, error)      { return p.info, nil }
func (p *prepBackend) SetGuestPassword(string, string, string) error { return nil }
func (p *prepBackend) Deploy(context.Context, string, virt.DeploySpec, func(string)) (string, error) {
	return "", nil
}
func (p *prepBackend) GuestExec(_ context.Context, _ string, argv []string) (virt.GuestExecResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.execs = append(p.execs, strings.Join(argv, " "))
	return virt.GuestExecResult{}, nil
}
func (p *prepBackend) DomainAction(_ string, a virt.Action) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if a == virt.ActionShutdown {
		p.fakeBackend.domains[0].State = virt.StateShutoff
	}
	return nil
}
func (p *prepBackend) ListDomains() ([]virt.Domain, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]virt.Domain(nil), p.fakeBackend.domains...), nil
}
func (p *prepBackend) SetTemplate(_ string, on bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.templated = on
	return nil
}

func TestPrepareTemplateLinux(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	b := &prepBackend{fakeBackend: &fakeBackend{domains: []virt.Domain{{UUID: id, Name: "golden", State: virt.StateRunning}}, detail: virt.DomainDetail{Name: "golden"}},
		info: virt.GuestInfo{Connected: true, OS: "Debian GNU/Linux 13 (trixie)"}}
	h := newTestServer(b, &fakeLive{}).Handler()
	rec := do(h, "POST", "/api/v1/domains/"+id+"/template/prepare", "", nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("prepare: %d %s", rec.Code, rec.Body)
	}
	var job struct{ ID, Title string }
	_ = json.Unmarshal(rec.Body.Bytes(), &job)
	if job.Title != "Prepare golden as a template" {
		t.Errorf("title %q", job.Title)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		rec = do(h, "GET", "/api/v1/jobs/"+job.ID, "", nil)
		if !strings.Contains(rec.Body.String(), `"running"`) || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !strings.Contains(rec.Body.String(), `"succeeded"`) || !b.templated || len(b.execs) != 1 || !strings.Contains(b.execs[0], "cloud-init clean") || !strings.Contains(b.execs[0], "ssh_host_") {
		t.Errorf("job %s, templated %v, execs %q", rec.Body, b.templated, b.execs)
	}
}

func TestPrepareTemplateNeedsTheAgent(t *testing.T) {
	b := &prepBackend{fakeBackend: &fakeBackend{domains: []virt.Domain{{UUID: "x", State: virt.StateRunning}}}}
	rec := do(newTestServer(b, &fakeLive{}).Handler(), "POST", "/api/v1/domains/x/template/prepare", "", nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "guest agent") {
		t.Errorf("no agent: %d %s", rec.Code, rec.Body)
	}
}
