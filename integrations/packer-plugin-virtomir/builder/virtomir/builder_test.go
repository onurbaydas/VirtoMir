package virtomir

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	packersdk "github.com/hashicorp/packer-plugin-sdk/packer"
)

// fakeVirtoMir is a host with one machine slot: it gets an address after a few looks and
// stops when told to shut down.
type fakeVirtoMir struct {
	mu           sync.Mutex
	created      map[string]any
	looks        int
	state        string
	calls        []string
	failTemplate bool
}

func (f *fakeVirtoMir) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer tok" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	op := r.Method + " " + r.URL.Path
	f.calls = append(f.calls, op)
	switch op {
	case "POST /api/v1/domains":
		_ = json.NewDecoder(r.Body).Decode(&f.created)
		f.state = "running"
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"uuid":"u1"}`))
	case "GET /api/v1/domains/u1":
		f.looks++
		addr := `[]`
		if f.looks > 2 {
			addr = `["fe80::1/64","192.168.122.50/24"]`
		}
		_, _ = w.Write([]byte(`{"uuid":"u1","state":"` + f.state + `","devices":[{"kind":"disk"},{"kind":"interface","addresses":` + addr + `}]}`))
	case "POST /api/v1/domains/u1/actions/shutdown", "POST /api/v1/domains/u1/actions/force-off":
		f.state = "shutoff"
		w.WriteHeader(http.StatusNoContent)
	case "PUT /api/v1/domains/u1/template":
		if f.failTemplate {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"busy"}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case "DELETE /api/v1/domains/u1":
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func build(t *testing.T, f *fakeVirtoMir) (packersdk.Artifact, error) {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	pollEvery = time.Millisecond
	var b Builder
	_, _, err := b.Prepare(map[string]interface{}{
		"url": srv.URL, "token": "tok", "template_name": "debian-13-base", "base_image": "/var/lib/libvirt/images/debian-13.qcow2",
		"storage_pool": "images", "communicator": "none",
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.Run(context.Background(), packersdk.TestUi(t), &packersdk.MockHook{})
}

func TestBuild(t *testing.T) {
	f := &fakeVirtoMir{}
	a, err := build(t, f)
	if err != nil {
		t.Fatal(err)
	}
	if a.Id() != "u1" || !strings.Contains(a.String(), "template debian-13-base") {
		t.Errorf("artifact %s %s", a.Id(), a.String())
	}
	c := f.created
	if c["name"] != "debian-13-base" || c["baseImage"] != "/var/lib/libvirt/images/debian-13.qcow2" || c["pool"] != "images" || c["start"] != true {
		t.Errorf("machine %v", c)
	}
	if ci := c["cloudInit"].(map[string]any); ci["user"] != "packer" || ci["hostname"] != "debian-13-base" {
		t.Errorf("cloud-init %v", ci)
	}
	if got := strings.Join(f.calls, ","); !strings.Contains(got, "POST /api/v1/domains/u1/actions/shutdown") || !strings.HasSuffix(got, "PUT /api/v1/domains/u1/template") {
		t.Errorf("calls %s", got)
	}
}

func TestBuildFailureRemovesTheMachine(t *testing.T) {
	f := &fakeVirtoMir{failTemplate: true}
	if _, err := build(t, f); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("err %v", err)
	}
	if last := f.calls[len(f.calls)-1]; last != "DELETE /api/v1/domains/u1" {
		t.Errorf("calls %v", f.calls)
	}
}

func TestPrepare(t *testing.T) {
	var b Builder
	_, _, err := b.Prepare(map[string]interface{}{"url": "https://hv1:8420", "communicator": "none"})
	for _, want := range []string{"token is required", "template_name", "base_image is required"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
	t.Setenv("VIRTOMIR_TOKEN", "tok")
	var ok Builder
	if _, _, err := ok.Prepare(map[string]interface{}{"url": "https://hv1:8420", "template_name": "t1", "base_image": "/img.qcow2"}); err != nil {
		t.Fatal(err)
	}
	if c := ok.config; c.VCPUs != 2 || c.MemoryMiB != 2048 || c.Network != "default" || c.Comm.SSHUsername != "packer" || c.Token != "tok" {
		t.Errorf("defaults %+v", c)
	}
}
