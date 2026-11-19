package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/onurbaydas/virtomir/internal/hostsetup"
)

type setupRunner struct {
	calls   []string
	failSSH bool
}

func (r *setupRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	if name == "ssh" && r.failSSH {
		return nil, errors.New("Permission denied (publickey)")
	}
	return nil, nil
}

func newSetupServer(t *testing.T, onPath ...string) (*Server, *setupRunner, string) {
	t.Helper()
	root := t.TempDir()
	r := &setupRunner{}
	s := newTestServer(&fakeBackend{}, &fakeLive{})
	s.hostSetup = &hostsetup.Setup{Run: r, Root: root, Look: func(c string) (string, error) {
		for _, p := range onPath {
			if p == c {
				return "/usr/bin/" + c, nil
			}
		}
		return "", errors.New("not found")
	}}
	return s, r, root
}

func TestHostSetupRoutes(t *testing.T) {
	s, runner, root := newSetupServer(t, "apt-get", "restic")
	h := s.Handler()

	rec := do(h, "GET", "/api/v1/host/setup", "", nil)
	var st struct {
		Features map[string]struct {
			Title     string
			Installed bool
		}
		Watchdog bool
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil || len(st.Features) != 4 || !st.Features["offsite"].Installed || st.Features["ceph"].Installed {
		t.Fatalf("GET setup: %d %s", rec.Code, rec.Body)
	}

	if rec = do(h, "POST", "/api/v1/host/setup/packages/../../etc", "", nil); rec.Code == http.StatusAccepted {
		t.Errorf("unknown feature accepted: %d", rec.Code)
	}
	rec = do(h, "POST", "/api/v1/host/setup/packages/offsite", "", nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("install: %d %s", rec.Code, rec.Body)
	}
	var job struct{ ID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &job)
	deadline := time.Now().Add(5 * time.Second)
	for {
		rec = do(h, "GET", "/api/v1/jobs/"+job.ID, "", nil)
		if !strings.Contains(rec.Body.String(), `"running"`) || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(rec.Body.String(), `"succeeded"`) || !strings.Contains(strings.Join(runner.calls, "\n"), "apt-get install -y -q --no-install-recommends restic") {
		t.Errorf("job: %s, calls %q", rec.Body, runner.calls)
	}

	// No boot loader VirtoMir knows: refused with what to do, not a 500.
	if rec = do(h, "POST", "/api/v1/host/setup/iommu", "", nil); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "kernel command line") {
		t.Errorf("iommu: %d %s", rec.Code, rec.Body)
	}

	if rec = do(h, "POST", "/api/v1/host/setup/ssh-authorize", `{"key":"command=\"x\" ssh-ed25519 AAAA"}`, nil); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("bad key: %d", rec.Code)
	}
	if rec = do(h, "POST", "/api/v1/host/setup/ssh-authorize", `{"key":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK virtomir@hv-02"}`, nil); rec.Code != http.StatusNoContent {
		t.Errorf("good key: %d %s", rec.Code, rec.Body)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "root/.ssh/authorized_keys")); !strings.Contains(string(b), "virtomir@hv-02") {
		t.Errorf("authorized_keys = %q", b)
	}

	// A target outside the cluster that does not take the key yet: ask for root's password.
	runner.failSSH = true
	_ = os.WriteFile(filepath.Join(root, "root/.ssh/id_ed25519"), []byte("k"), 0o600)
	_ = os.WriteFile(filepath.Join(root, "root/.ssh/id_ed25519.pub"), []byte("ssh-ed25519 AAAAC3Nza virtomir@hv-01\n"), 0o644)
	rec = do(h, "POST", "/api/v1/host/setup/ssh-trust", `{"target":"root@10.0.0.9"}`, nil)
	var trust sshTrust
	_ = json.Unmarshal(rec.Body.Bytes(), &trust)
	if rec.Code != http.StatusOK || trust.Authorized || !trust.NeedsPassword || !strings.HasPrefix(trust.PublicKey, "ssh-ed25519") || !strings.Contains(trust.Reason, "password") {
		t.Errorf("trust: %d %s", rec.Code, rec.Body)
	}
	// It takes the key already: nothing to ask.
	runner.failSSH = false
	trust = sshTrust{}
	rec = do(h, "POST", "/api/v1/host/setup/ssh-trust", `{"target":"10.0.0.9"}`, nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &trust)
	if !trust.Verified || trust.NeedsPassword {
		t.Errorf("trust with a working key: %s", rec.Body)
	}
	// A wrong password (nothing listens there): an error, never a 401 that would sign the admin out.
	rec = do(h, "POST", "/api/v1/host/setup/ssh-trust", `{"target":"127.0.0.1","password":"x"}`, nil)
	if rec.Code == http.StatusOK || rec.Code == http.StatusUnauthorized {
		t.Errorf("unreachable target with a password: %d %s", rec.Code, rec.Body)
	}
	for _, bad := range []string{`{"target":"admin@10.0.0.9"}`, `{"target":"10.0.0.9; reboot"}`, `{"target":""}`} {
		if rec = do(h, "POST", "/api/v1/host/setup/ssh-trust", bad, nil); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("trust %s: %d", bad, rec.Code)
		}
	}
}

func TestHostSetupIsAdminOnly(t *testing.T) {
	for _, p := range []string{"/api/v1/host/setup/packages/ceph", "/api/v1/host/setup/iommu", "/api/v1/host/setup/watchdog", "/api/v1/host/setup/ssh-authorize", "/api/v1/host/setup/ssh-trust"} {
		if got := requiredRole(http.MethodPost, p).String(); got != "admin" {
			t.Errorf("%s needs %s, want admin", p, got)
		}
	}
}

func TestSplitTarget(t *testing.T) {
	for in, want := range map[string]string{"root@10.0.0.2": "10.0.0.2", "nas.lan": "nas.lan", "[fd00::2]": "fd00::2", "a b": "", "x;y": "", "-oProxyCommand=x": ""} {
		if _, got := splitTarget(in); got != want {
			t.Errorf("splitTarget(%q) = %q, want %q", in, got, want)
		}
	}
}
