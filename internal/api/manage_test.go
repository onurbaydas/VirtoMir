package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/onurbaydas/virtomir/internal/auth"
	"github.com/onurbaydas/virtomir/internal/jobs"
	"github.com/onurbaydas/virtomir/internal/virt"
)

func (f *fakeBackend) DefineXML(uuid, x string) (virt.DefineResult, error) {
	f.call("define " + uuid + " " + x)
	return virt.DefineResult{AppliesOnNextBoot: true}, f.err
}
func (f *fakeBackend) Delete(uuid string, rm bool) (virt.DeleteResult, error) {
	if rm {
		f.call("delete+storage " + uuid)
	} else {
		f.call("delete " + uuid)
	}
	return virt.DeleteResult{RemovedVolumes: []string{"/a.qcow2"}, KeptVolumes: []virt.KeptVolume{}}, f.err
}
func (f *fakeBackend) Clone(_ context.Context, uuid, name string, report func(string)) (string, error) {
	report("copying")
	f.call("clone " + uuid + " " + name)
	return "new-uuid", f.err
}
func (f *fakeBackend) CreateDomain(spec virt.CreateSpec) (virt.CreateResult, error) {
	f.call("create " + spec.Name)
	return virt.CreateResult{UUID: "u-new"}, f.err
}
func (f *fakeBackend) AttachDisk(uuid string, spec virt.DiskSpec) (string, error) {
	f.call("attach " + spec.Pool)
	return "vdb", f.err
}
func (f *fakeBackend) ListNetworks() ([]virt.VNetwork, error) {
	return []virt.VNetwork{{Name: "default", Mode: "nat", UsedBy: []string{}}}, f.err
}

type stubAuthn struct{}

func (stubAuthn) Authenticate(_ context.Context, u, p string) error {
	if (u == "onur" || u == "op" || u == "view") && p == "pw" {
		return nil
	}
	return auth.ErrBadCredentials
}

func authServer(b Backend) http.Handler {
	m := auth.New(stubAuthn{}, func(u string) auth.Role {
		switch u {
		case "onur":
			return auth.Admin
		case "op":
			return auth.Operator
		case "view":
			return auth.Viewer
		}
		return auth.NoAccess
	})
	return NewServer(Options{Backend: b, Live: &fakeLive{}, Auth: m, Version: "t", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Handler()
}

func do(h http.Handler, method, path, body string, cookie *http.Cookie, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if cookie != nil {
		req.AddCookie(cookie)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAuthGuard(t *testing.T) {
	h := authServer(&fakeBackend{domains: []virt.Domain{}})

	if rec := do(h, "GET", "/api/v1/domains", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no session: status %d", rec.Code)
	}
	// Public: version and the session probe.
	if rec := do(h, "GET", "/api/v1/version", "", nil); rec.Code != http.StatusOK {
		t.Errorf("version must be public: %d", rec.Code)
	}
	if rec := do(h, "GET", "/api/v1/auth/session", "", nil); rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), `"mode":"pam"`) {
		t.Errorf("session probe: %d %s", rec.Code, rec.Body)
	}

	if rec := do(h, "POST", "/api/v1/auth/login", `{"user":"onur","password":"nope"}`, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("bad password: %d", rec.Code)
	}
	rec := do(h, "POST", "/api/v1/auth/login", `{"user":"onur","password":"pw"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	cookie := rec.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Errorf("cookie flags: %+v", cookie)
	}
	if rec := do(h, "GET", "/api/v1/domains", "", cookie); rec.Code != http.StatusOK {
		t.Errorf("with session: %d", rec.Code)
	}
	if rec := do(h, "GET", "/api/v1/auth/session", "", cookie); !strings.Contains(rec.Body.String(), `"user":"onur"`) {
		t.Errorf("session: %s", rec.Body)
	}

	do(h, "POST", "/api/v1/auth/logout", "", cookie)
	if rec := do(h, "GET", "/api/v1/domains", "", cookie); rec.Code != http.StatusUnauthorized {
		t.Errorf("after logout: %d", rec.Code)
	}
}

func TestCrossSiteWritesRefused(t *testing.T) {
	b := &fakeBackend{}
	h := newTestServer(b, &fakeLive{}).Handler()
	rec := do(h, "POST", "/api/v1/domains/u1/actions/force-off", "", nil, "Origin", "https://evil.example")
	if rec.Code != http.StatusForbidden || b.lastAction != "" {
		t.Errorf("cross-site write: %d, action %q", rec.Code, b.lastAction)
	}
	rec = do(h, "POST", "/api/v1/domains/u1/actions/force-off", "", nil, "Sec-Fetch-Site", "cross-site")
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-site (fetch metadata): %d", rec.Code)
	}
	// Same origin is fine (httptest requests use Host example.com).
	rec = do(h, "POST", "/api/v1/domains/u1/actions/force-off", "", nil, "Origin", "http://example.com")
	if rec.Code != http.StatusAccepted {
		t.Errorf("same-origin write: %d", rec.Code)
	}
}

func TestPutXML(t *testing.T) {
	b := &fakeBackend{}
	h := newTestServer(b, &fakeLive{}).Handler()
	rec := do(h, "PUT", "/api/v1/domains/u1/xml", "<domain/>", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"appliesOnNextBoot":true`) || b.calls[0] != "define u1 <domain/>" {
		t.Errorf("put xml: %d %s %v", rec.Code, rec.Body, b.calls)
	}
	b.err = &virt.InvalidError{Message: "The XML must keep <uuid>"}
	if rec := do(h, "PUT", "/api/v1/domains/u1/xml", "<domain/>", nil); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("invalid xml: %d", rec.Code)
	}
}

func TestDeleteDomain(t *testing.T) {
	b := &fakeBackend{}
	h := newTestServer(b, &fakeLive{}).Handler()
	do(h, "DELETE", "/api/v1/domains/u1", "", nil)
	do(h, "DELETE", "/api/v1/domains/u1?storage=remove", "", nil)
	if strings.Join(b.calls, ",") != "delete u1,delete+storage u1" {
		t.Errorf("calls = %v", b.calls)
	}
}

func TestCloneRunsAsJob(t *testing.T) {
	b := &fakeBackend{}
	jm := jobs.New(context.Background())
	h := NewServer(Options{Backend: b, Live: &fakeLive{}, Jobs: jm, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Handler()
	rec := do(h, "POST", "/api/v1/domains/u1/clone", `{"name":"copy"}`, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("clone: %d %s", rec.Code, rec.Body)
	}
	var j jobs.Job
	_ = json.Unmarshal(rec.Body.Bytes(), &j)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rec = do(h, "GET", "/api/v1/jobs/"+j.ID, "", nil)
		_ = json.Unmarshal(rec.Body.Bytes(), &j)
		if j.State != jobs.Running {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if j.State != jobs.Succeeded || j.Result != "new-uuid" {
		t.Errorf("job = %+v", j)
	}
}

func TestBadJSONIs400(t *testing.T) {
	h := newTestServer(&fakeBackend{}, &fakeLive{}).Handler()
	for _, body := range []string{"{", `{"name":"x","bogus":1}`} {
		if rec := do(h, "POST", "/api/v1/domains", body, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", body, rec.Code)
		}
	}
	if rec := do(h, "POST", "/api/v1/domains", `{"name":"web","vcpus":2}`, nil); rec.Code != http.StatusCreated {
		t.Errorf("create: %d %s", rec.Code, rec.Body)
	}
}

func TestUploadNeedsLength(t *testing.T) {
	h := newTestServer(&fakeBackend{}, &fakeLive{}).Handler()
	req := httptest.NewRequest("POST", "/api/v1/storage/pools/default/upload?name=a.iso", strings.NewReader("x"))
	req.ContentLength = -1
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusLengthRequired {
		t.Errorf("status %d", rec.Code)
	}
}

func TestNetworksAndDisks(t *testing.T) {
	b := &fakeBackend{}
	h := newTestServer(b, &fakeLive{}).Handler()
	if rec := do(h, "GET", "/api/v1/networks", "", nil); !strings.Contains(rec.Body.String(), `"name":"default"`) {
		t.Errorf("networks: %s", rec.Body)
	}
	rec := do(h, "POST", "/api/v1/domains/u1/disks", `{"pool":"default","sizeGiB":10}`, nil)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), "vdb") {
		t.Errorf("attach: %d %s", rec.Code, rec.Body)
	}
}
