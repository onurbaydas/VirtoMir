package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/onurbaydas/virtomir/internal/virt"
)

// fakeBackend implements the calls the tests use; the embedded interface makes any other
// call panic, which flags an untested path.
type fakeBackend struct {
	Backend
	host         virt.HostInfo
	domains      []virt.Domain
	detail       virt.DomainDetail
	xml          string
	err          error
	lastAction   virt.Action
	lastInactive bool
	calls        []string
}

func (f *fakeBackend) call(s string) { f.calls = append(f.calls, s) }

func (f *fakeBackend) HostInfo() (virt.HostInfo, error)    { return f.host, f.err }
func (f *fakeBackend) ListDomains() ([]virt.Domain, error) { return f.domains, f.err }
func (f *fakeBackend) DomainDetail(string) (virt.DomainDetail, error) {
	return f.detail, f.err
}
func (f *fakeBackend) DomainXML(_ string, inactive bool) (string, error) {
	f.lastInactive = inactive
	return f.xml, f.err
}
func (f *fakeBackend) DomainAction(_ string, a virt.Action) error {
	f.lastAction = a
	return f.err
}

type fakeLive struct {
	backlog   [][]byte
	ch        chan []byte
	refreshed int
}

func (f *fakeLive) Subscribe() ([][]byte, <-chan []byte, func()) { return f.backlog, f.ch, func() {} }
func (f *fakeLive) Refresh()                                     { f.refreshed++ }

func newTestServer(b Backend, l Live) *Server {
	ui := fstest.MapFS{
		"index.html":    {Data: []byte("<html>app</html>")},
		"assets/app.js": {Data: []byte("console.log(1)")},
	}
	return NewServer(Options{Backend: b, Live: l, UI: ui, Version: "test", Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
}

func serve(t *testing.T, b Backend, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	newTestServer(b, &fakeLive{}).Handler().ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestListDomains(t *testing.T) {
	b := &fakeBackend{domains: []virt.Domain{{Name: "web", State: virt.StateRunning, VCPUs: 2}}}
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
	rec := serve(t, &fakeBackend{domains: []virt.Domain{}}, http.MethodGet, "/api/v1/domains")
	if got, want := rec.Body.String(), "{\"domains\":[]}\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{errors.New("libvirt down"), http.StatusBadGateway},
		{fmt.Errorf("%w: no domain", virt.ErrNotFound), http.StatusNotFound},
		{&virt.OperationError{Message: "domain is already running"}, http.StatusConflict},
	}
	for _, c := range cases {
		rec := serve(t, &fakeBackend{err: c.err}, http.MethodGet, "/api/v1/host")
		if rec.Code != c.want {
			t.Errorf("%v: status = %d, want %d", c.err, rec.Code, c.want)
		}
		var body map[string]string
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if body["error"] != c.err.Error() {
			t.Errorf("error = %q", body["error"])
		}
	}
}

func TestGetDomain(t *testing.T) {
	b := &fakeBackend{detail: virt.DomainDetail{
		Domain:  virt.Domain{UUID: "u1", Name: "web"},
		Devices: []virt.Device{{Kind: "disk", Title: "Disk vda", Props: []virt.Prop{{Label: "Bus", Value: "VirtIO", Tech: "target@bus"}}}},
	}}
	rec := serve(t, b, http.MethodGet, "/api/v1/domains/u1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Name    string
		Devices []struct{ Kind, Title string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Name != "web" || len(body.Devices) != 1 || body.Devices[0].Title != "Disk vda" {
		t.Errorf("unexpected body: %s", rec.Body)
	}

	rec = serve(t, &fakeBackend{err: fmt.Errorf("%w: gone", virt.ErrNotFound)}, http.MethodGet, "/api/v1/domains/u1")
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing domain: status = %d", rec.Code)
	}
}

func TestGetDomainXML(t *testing.T) {
	b := &fakeBackend{xml: "<domain type='kvm'/>"}
	rec := serve(t, b, http.MethodGet, "/api/v1/domains/u1/xml?inactive=true")
	if rec.Code != http.StatusOK || rec.Body.String() != b.xml || !b.lastInactive {
		t.Errorf("status=%d body=%q inactive=%v", rec.Code, rec.Body, b.lastInactive)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/xml") {
		t.Errorf("content type %q", ct)
	}
	serve(t, b, http.MethodGet, "/api/v1/domains/u1/xml")
	if b.lastInactive {
		t.Error("live XML expected without ?inactive=true")
	}
}

func TestDomainAction(t *testing.T) {
	b, l := &fakeBackend{}, &fakeLive{}
	rec := httptest.NewRecorder()
	newTestServer(b, l).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/domains/abc/actions/force-off", nil))
	if rec.Code != http.StatusAccepted || b.lastAction != virt.ActionForceOff || l.refreshed != 1 {
		t.Errorf("status=%d action=%q refreshed=%d", rec.Code, b.lastAction, l.refreshed)
	}

	rec = serve(t, b, http.MethodPost, "/api/v1/domains/abc/actions/explode")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown action: status = %d", rec.Code)
	}
}

func TestWrongMethodRejected(t *testing.T) {
	rec := serve(t, &fakeBackend{}, http.MethodPatch, "/api/v1/domains")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestUnknownAPIPathIsJSON404(t *testing.T) {
	rec := serve(t, &fakeBackend{}, http.MethodGet, "/api/v1/nope")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Header().Get("Content-Type"), "json") {
		t.Errorf("status=%d type=%q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestSPAFallback(t *testing.T) {
	for _, p := range []string{"/", "/machines/some-vm"} {
		rec := serve(t, &fakeBackend{}, http.MethodGet, p)
		if rec.Code != http.StatusOK || rec.Body.String() != "<html>app</html>" {
			t.Errorf("%s: status=%d body=%q", p, rec.Code, rec.Body)
		}
	}
	rec := serve(t, &fakeBackend{}, http.MethodGet, "/assets/app.js")
	if !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("assets must be cached immutably, got %q", rec.Header().Get("Cache-Control"))
	}
}

func TestStream(t *testing.T) {
	l := &fakeLive{backlog: [][]byte{[]byte(`{"n":1}`), []byte(`{"n":2}`)}, ch: make(chan []byte, 1)}
	srv := httptest.NewServer(newTestServer(&fakeBackend{}, l).Handler())
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}

	l.ch <- []byte(`{"n":3}`)
	sc := bufio.NewScanner(resp.Body)
	var data []string
	for len(data) < 3 && sc.Scan() {
		if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			data = append(data, d)
		}
	}
	if got := strings.Join(data, ","); got != `{"n":1},{"n":2},{"n":3}` {
		t.Errorf("stream data = %s, want backlog then live update", got)
	}
}
