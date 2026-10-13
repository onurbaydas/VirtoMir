package api

import (
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onurbaydas/virtomir/internal/auth"
	"github.com/onurbaydas/virtomir/internal/quota"
	"github.com/onurbaydas/virtomir/internal/virt"
)

func quotaServer(t *testing.T, doms []virt.Domain) (http.Handler, *fakeBackend) {
	t.Helper()
	m := auth.New(stubAuthn{}, func(u string) auth.Role {
		return map[string]auth.Role{"onur": auth.Admin, "op": auth.Operator, "view": auth.Operator}[u] // view: an operator without a quota
	})
	q, _ := quota.Open(filepath.Join(t.TempDir(), "quotas.json"))
	if err := q.Set([]quota.Rule{{Kind: "group", Name: "students", Machines: 2, VCPUs: 4, MemoryMiB: 4096}}); err != nil {
		t.Fatal(err)
	}
	b := &fakeBackend{domains: doms}
	h := NewServer(Options{Backend: b, Live: &fakeLive{}, Auth: m, Quotas: q,
		GroupsOf: func(u string) []string {
			if u == "op" {
				return []string{"students"}
			}
			return nil
		},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Handler()
	return h, b
}

func TestQuotaSelfService(t *testing.T) {
	mine := virt.Domain{UUID: "11111111-1111-1111-1111-111111111111", Name: "mine", Owner: "op", VCPUs: 2, MaxMemoryKiB: 2048 << 10}
	theirs := virt.Domain{UUID: "22222222-2222-2222-2222-222222222222", Name: "theirs", Owner: "onur", VCPUs: 8, MaxMemoryKiB: 8192 << 10}
	h, b := quotaServer(t, []virt.Domain{mine, theirs})
	op, op2 := signIn(t, h, "op"), signIn(t, h, "view")

	// Within the quota: 1 of 2 machines and 2 of 4 vCPUs used.
	if rec := do(h, "POST", "/api/v1/domains", `{"name":"web","vcpus":2,"memoryMiB":2048}`, op); rec.Code != http.StatusCreated {
		t.Errorf("create within quota: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/api/v1/domains", `{"name":"big","vcpus":4,"memoryMiB":1024}`, op); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "vCPUs") {
		t.Errorf("over quota: %d %s", rec.Code, rec.Body)
	}
	// No rule, no self-service.
	if rec := do(h, "POST", "/api/v1/domains", `{"name":"x","vcpus":1,"memoryMiB":512}`, op2); rec.Code != http.StatusForbidden {
		t.Errorf("operator without quota: %d", rec.Code)
	}
	// Own machine: delete and settings yes; XML and host devices no.
	if rec := do(h, "DELETE", "/api/v1/domains/"+mine.UUID, "", op); rec.Code != http.StatusOK {
		t.Errorf("delete own: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "PUT", "/api/v1/domains/"+mine.UUID+"/xml", "<domain/>", op); rec.Code != http.StatusForbidden {
		t.Errorf("xml own: %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/v1/domains/"+mine.UUID+"/hostdevs", `{"type":"pci","address":"0000:01:00.0"}`, op); rec.Code != http.StatusForbidden {
		t.Errorf("hostdev own: %d", rec.Code)
	}
	// Growing the own machine past the quota is refused.
	if rec := do(h, "PUT", "/api/v1/domains/"+mine.UUID+"/settings", `{"maxVcpus":8,"vcpus":8,"memoryMiB":2048}`, op); rec.Code != http.StatusForbidden {
		t.Errorf("grow over quota: %d %s", rec.Code, rec.Body)
	}
	// Somebody else's machine stays out of reach.
	if rec := do(h, "DELETE", "/api/v1/domains/"+theirs.UUID, "", op); rec.Code != http.StatusForbidden {
		t.Errorf("delete other's: %d", rec.Code)
	}
	if !strings.Contains(strings.Join(b.calls, ","), "create web") {
		t.Errorf("calls: %v", b.calls)
	}
	// Quota status for the UI.
	if rec := do(h, "GET", "/api/v1/quotas/mine", "", op); !strings.Contains(rec.Body.String(), `"machines":2`) || !strings.Contains(rec.Body.String(), `"used"`) {
		t.Errorf("mine: %s", rec.Body)
	}
}
