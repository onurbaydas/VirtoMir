package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/onurbaydas/virtomir/internal/history"
	"github.com/onurbaydas/virtomir/internal/virt"
)

func (f *fakeBackend) SaveSettings(uuid string, s virt.Settings) (virt.SaveResult, error) {
	f.call("settings " + uuid)
	return virt.SaveResult{AppliedLive: []string{"vCPUs"}, LiveErrors: []string{}}, f.err
}

func TestHistoryEndpoint(t *testing.T) {
	store := history.New()
	store.Add("host.cpu", time.Now(), 12.5)
	h := NewServer(Options{Backend: &fakeBackend{}, Live: &fakeLive{}, History: store, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Handler()

	rec := do(h, "GET", "/api/v1/history?series=host.cpu&range=1h", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	var res history.Result
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	v := res.Series["host.cpu"]
	if res.Step != 10 || len(v) != 360 || v[len(v)-1] == nil || *v[len(v)-1] != 12.5 {
		t.Errorf("result step=%d len=%d", res.Step, len(v))
	}
	for _, bad := range []string{"?series=host.cpu&range=2y", "?range=1h", ""} {
		if rec := do(h, "GET", "/api/v1/history"+bad, "", nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%q: status %d", bad, rec.Code)
		}
	}
	// Without a store the endpoint says so.
	h2 := newTestServer(&fakeBackend{}, &fakeLive{}).Handler()
	if rec := do(h2, "GET", "/api/v1/history?series=host.cpu&range=1h", "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("no store: %d", rec.Code)
	}
}

func TestSaveSettings(t *testing.T) {
	b := &fakeBackend{}
	h := newTestServer(b, &fakeLive{}).Handler()
	rec := do(h, "PUT", "/api/v1/domains/u1/settings", `{"vcpus":2,"maxVcpus":2}`, nil)
	if rec.Code != http.StatusOK || b.calls[0] != "settings u1" {
		t.Errorf("status %d calls %v body %s", rec.Code, b.calls, rec.Body)
	}
}
