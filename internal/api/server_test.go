package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/onurbaydas/virtomir/internal/virt"
)

type fakeBackend struct {
	host    virt.HostInfo
	domains []virt.Domain
	err     error
}

func (f fakeBackend) HostInfo() (virt.HostInfo, error)    { return f.host, f.err }
func (f fakeBackend) ListDomains() ([]virt.Domain, error) { return f.domains, f.err }

func serve(t *testing.T, b Backend, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	srv := NewServer(b, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestListDomains(t *testing.T) {
	b := fakeBackend{domains: []virt.Domain{{Name: "web", State: virt.StateRunning, VCPUs: 2}}}
	rec := serve(t, b, http.MethodGet, "/api/v1/domains")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct{ Domains []virt.Domain }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Domains) != 1 || body.Domains[0].Name != "web" || body.Domains[0].State != virt.StateRunning {
		t.Errorf("unexpected body: %s", rec.Body)
	}
}

func TestListDomainsEmptyIsArray(t *testing.T) {
	rec := serve(t, fakeBackend{domains: []virt.Domain{}}, http.MethodGet, "/api/v1/domains")
	if got, want := rec.Body.String(), "{\"domains\":[]}\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestBackendErrorIs502(t *testing.T) {
	rec := serve(t, fakeBackend{err: errors.New("libvirt down")}, http.MethodGet, "/api/v1/host")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d", rec.Code)
	}
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["error"] != "libvirt down" {
		t.Errorf("error = %q", body["error"])
	}
}

func TestWrongMethodRejected(t *testing.T) {
	rec := serve(t, fakeBackend{}, http.MethodPost, "/api/v1/domains")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}
