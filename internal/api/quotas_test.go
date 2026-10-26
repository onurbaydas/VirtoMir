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
	if rec := do(h, "POST", "/api/v1/domains?wait=true", `{"name":"web","vcpus":2,"memoryMiB":2048}`, op); rec.Code != http.StatusCreated {
		t.Errorf("create within quota: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/api/v1/domains?wait=true", `{"name":"big","vcpus":4,"memoryMiB":1024}`, op); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "vCPUs") {
		t.Errorf("over quota: %d %s", rec.Code, rec.Body)
	}
	// No rule, no self-service.
	if rec := do(h, "POST", "/api/v1/domains?wait=true", `{"name":"x","vcpus":1,"memoryMiB":512}`, op2); rec.Code != http.StatusForbidden {
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

// diskBackend adds disk sizes to the fake backend.
type diskBackend struct {
	*fakeBackend
	sizes map[string]uint64
}

func (d diskBackend) DiskBytes(uuids []string) (map[string]uint64, error) {
	out := map[string]uint64{}
	for _, u := range uuids {
		out[u] = d.sizes[u]
	}
	return out, nil
}

func TestDiskQuota(t *testing.T) {
	mine := virt.Domain{UUID: "11111111-1111-1111-1111-111111111111", Name: "mine", Owner: "op", VCPUs: 1, MaxMemoryKiB: 1024 << 10}
	m := auth.New(stubAuthn{}, func(u string) auth.Role { return auth.Operator })
	q, _ := quota.Open(filepath.Join(t.TempDir(), "quotas.json"))
	limit := uint64(50)
	if err := q.Set([]quota.Rule{{Kind: "user", Name: "op", Machines: 5, VCPUs: 8, MemoryMiB: 8192, DiskGiB: &limit}}); err != nil {
		t.Fatal(err)
	}
	b := diskBackend{&fakeBackend{domains: []virt.Domain{mine}}, map[string]uint64{mine.UUID: 30 << 30}}
	h := NewServer(Options{Backend: b, Live: &fakeLive{}, Auth: m, Quotas: q, GroupsOf: func(string) []string { return nil },
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Handler()
	op := signIn(t, h, "op")
	if rec := do(h, "POST", "/api/v1/domains?wait=true", `{"name":"a","vcpus":1,"memoryMiB":1024,"diskGiB":20}`, op); rec.Code != http.StatusCreated {
		t.Errorf("30 + 20 of 50 GiB: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/api/v1/domains?wait=true", `{"name":"b","vcpus":1,"memoryMiB":1024,"diskGiB":21}`, op); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "disk space (30 of 50 GiB used, this needs 21)") {
		t.Errorf("over the disk quota: %d %s", rec.Code, rec.Body)
	}
	// An existing disk (diskPath) adds nothing.
	if rec := do(h, "POST", "/api/v1/domains?wait=true", `{"name":"c","vcpus":1,"memoryMiB":1024,"diskPath":"/var/lib/libvirt/images/c.qcow2","diskGiB":99}`, op); rec.Code != http.StatusCreated {
		t.Errorf("existing disk: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "GET", "/api/v1/quotas/mine", "", op); !strings.Contains(rec.Body.String(), `"diskGiB":30`) || !strings.Contains(rec.Body.String(), `"diskGiB":50`) {
		t.Errorf("mine: %s", rec.Body)
	}
}
