package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onurbaydas/virtomir/internal/ha"
	"github.com/onurbaydas/virtomir/internal/hostinfo"
	"github.com/onurbaydas/virtomir/internal/live"
	"github.com/onurbaydas/virtomir/internal/reports"
	"github.com/onurbaydas/virtomir/internal/respool"
	"github.com/onurbaydas/virtomir/internal/virt"
	"github.com/onurbaydas/virtomir/internal/zfsrep"
)

// poolBackend adds storage pools and volumes to the fake.
type poolBackend struct {
	fakeBackend
	pools   []virt.Pool
	volumes map[string][]virt.Volume
	created []virt.CreateSpec
}

func (p *poolBackend) ListPools() ([]virt.Pool, error) { return p.pools, nil }
func (p *poolBackend) ListVolumes(pool string) ([]virt.Volume, error) {
	return p.volumes[pool], nil
}
func (p *poolBackend) CreateDomain(spec virt.CreateSpec) (virt.CreateResult, error) {
	p.created = append(p.created, spec)
	return virt.CreateResult{UUID: "new-uuid"}, nil
}

func enterpriseServer(t *testing.T, b Backend) (*Server, http.Handler) {
	t.Helper()
	dir := t.TempDir()
	rp, err := respool.Open(filepath.Join(dir, "respools.json"))
	if err != nil {
		t.Fatal(err)
	}
	rs, err := reports.Open(filepath.Join(dir, "reports"))
	if err != nil {
		t.Fatal(err)
	}
	hs, err := ha.Open(filepath.Join(dir, "ha.json"))
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zfsrep.Open(filepath.Join(dir, "zfs-replication.json"), noZFS{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(Options{Backend: b, Live: &fakeLive{}, Version: "t", HostName: "hv1",
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Respools: rp, Reports: rs, HA: hs, HostInfo: &hostinfo.Reader{Root: dir}, ZFSRep: zr})
	return s, s.Handler()
}

// noZFS answers like a host without the zfs command.
type noZFS struct{}

func (noZFS) Run(context.Context, string, ...string) ([]byte, error) {
	return nil, errors.New("zfs: command not found")
}
func (noZFS) Pipe(context.Context, []string, []string) (uint64, error) {
	return 0, errors.New("zfs: command not found")
}

func TestResourcePoolLimitsAreEnforced(t *testing.T) {
	b := &poolBackend{
		fakeBackend: fakeBackend{domains: []virt.Domain{
			{UUID: "db", Name: "db", VCPUs: 4, MaxMemoryKiB: 8 << 20},
			{UUID: "web", Name: "web", VCPUs: 4, MaxMemoryKiB: 4 << 20},
		}},
		pools:   []virt.Pool{{Name: "default", Active: true}},
		volumes: map[string][]virt.Volume{"default": {{Name: "db.qcow2", CapacityBytes: 40 << 30, UsedBy: []string{"db"}}}},
	}
	_, h := enterpriseServer(t, b)
	rec := do(h, "POST", "/api/v1/respools", `{"name":"Team A","cpuLimit":6,"memoryLimitMiB":16384,"storageLimits":{"default":53687091200}}`, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create pool: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "PUT", "/api/v1/domains/db/respool", `{"pool":"team-a"}`, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("assign db: %d %s", rec.Code, rec.Body)
	}
	// web's 4 vCPUs do not fit next to db's 4 under a limit of 6.
	if rec := do(h, "PUT", "/api/v1/domains/web/respool", `{"pool":"team-a"}`, nil); rec.Code != http.StatusConflict {
		t.Errorf("over-limit assignment: %d %s", rec.Code, rec.Body)
	}
	// A new machine in the pool: 2 vCPUs fit, but its 20 GiB disk does not (40 + 20 > 50).
	rec = do(h, "POST", "/api/v1/domains?respool=team-a&wait=true", `{"name":"x","vcpus":2,"memoryMiB":1024,"pool":"default","diskGiB":20}`, nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "storage pool default") {
		t.Errorf("disk over limit: %d %s", rec.Code, rec.Body)
	}
	rec = do(h, "POST", "/api/v1/domains?respool=team-a&wait=true", `{"name":"x","vcpus":2,"memoryMiB":1024,"pool":"default","diskGiB":5}`, nil)
	if rec.Code != http.StatusCreated || len(b.created) != 1 {
		t.Fatalf("create within limits: %d %s", rec.Code, rec.Body)
	}
	var list struct{ Pools []PoolView }
	rec = do(h, "GET", "/api/v1/respools", "", nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Pools) != 1 {
		t.Fatalf("list: %s", rec.Body)
	}
	u := list.Pools[0].Usage
	// The new machine does not exist on this fake host, so listing drops it from the pool
	// (as for a machine deleted with virsh).
	if u.VCPUs != 4 || u.MemoryMiB != 8192 || u.StorageBytes["default"] != 40<<30 || len(list.Pools[0].Machines) != 1 {
		t.Errorf("usage = %+v machines %v", u, list.Pools[0].Machines)
	}
}

func TestReportsFromRealFacts(t *testing.T) {
	b := &poolBackend{
		fakeBackend: fakeBackend{host: virt.HostInfo{Hostname: "hv1", CPUs: 8, MemoryKiB: 16 << 20},
			domains: []virt.Domain{{UUID: "db", Name: "db", State: virt.StateRunning, VCPUs: 2, MaxMemoryKiB: 2 << 20}},
			detail:  virt.DomainDetail{Devices: []virt.Device{{Kind: "disk", CapacityBytes: 10 << 30}, {Kind: "interface", Addresses: []string{"10.0.0.5/24"}}}}},
		pools: []virt.Pool{{Name: "default", Type: "dir", Active: true, CapacityBytes: 100 << 30, AllocationBytes: 10 << 30}},
	}
	_, h := enterpriseServer(t, b)
	rec := do(h, "POST", "/api/v1/reports", `{"kind":"inventory","format":"csv"}`, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("generate: %d %s", rec.Code, rec.Body)
	}
	var m reports.Meta
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	rec = do(h, "GET", "/api/v1/reports/"+m.ID, "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "db,db,hv1,running,2,2048,10.0,1,10.0.0.5/24") {
		t.Errorf("report: %d %s", rec.Code, rec.Body)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("CSP = %q", csp)
	}
	if rec := do(h, "PUT", "/api/v1/report-schedules", `{"name":"w","kind":"capacity","format":"html","every":"weekly","at":"06:00","enabled":true}`, nil); rec.Code != http.StatusOK {
		t.Errorf("schedule: %d %s", rec.Code, rec.Body)
	}
}

func TestHAStatusAndPeerOnlyRoutes(t *testing.T) {
	s, h := enterpriseServer(t, &fakeBackend{})
	if rec := do(h, "GET", "/api/v1/ha/status", "", nil); rec.Code != http.StatusNotImplemented {
		t.Errorf("status without manager: %d", rec.Code)
	}
	s.SetHA(ha.NewManager(s.ha, s.HATransport(), nil, t.TempDir(), s.log))
	rec := do(h, "GET", "/api/v1/ha/status", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"selfId":"local"`) {
		t.Errorf("status: %d %s", rec.Code, rec.Body)
	}
	// Heartbeats and restart requests are for members only.
	for _, p := range []string{"/api/v1/cluster/peer/ha/heartbeat", "/api/v1/cluster/peer/ha/recover"} {
		if rec := do(h, "POST", p, `{}`, nil); rec.Code < 400 {
			t.Errorf("%s without a peer credential: %d", p, rec.Code)
		}
	}
	if rec := do(h, "PUT", "/api/v1/ha/settings", `{"failoverTimeoutSec":3}`, nil); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("bad settings: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "PUT", "/api/v1/ha/vms/AA", `{"enabled":true,"priority":"high"}`, nil); rec.Code != http.StatusOK {
		t.Errorf("policy: %d %s", rec.Code, rec.Body)
	}
	if _, ok := s.ha.Policy("aa"); !ok {
		t.Error("policy not stored")
	}
}

func TestCaptureIsAdminOnly(t *testing.T) {
	if requiredRole(http.MethodGet, "/api/v1/host/capture").String() != "admin" {
		t.Error("packet capture readable by non-admins")
	}
	if args, err := captureArgs(&hostinfo.Reader{Root: t.TempDir()}, "-w", "", "", false); err == nil {
		t.Errorf("option injected as interface: %v", args)
	}
}

func TestDownloadValidation(t *testing.T) {
	_, h := enterpriseServer(t, &fakeBackend{})
	for _, body := range []string{
		`{"url":"file:///etc/shadow"}`,
		`{"url":"https://example.com/x.iso","sha256":"abc"}`,
	} {
		if rec := do(h, "POST", "/api/v1/storage/pools/default/download", body, nil); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d", body, rec.Code)
		}
	}
}

func TestZFSReplicationAPI(t *testing.T) {
	s, h := enterpriseServer(t, &fakeBackend{})

	// 1. Initially empty
	rec := do(h, "GET", "/api/v1/storage/zfs/replications", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}

	// 2. Create replication task; bad names are refused
	rec = do(h, "POST", "/api/v1/storage/zfs/replications", `{"sourcePool":"tank","sourceVolume":"x;reboot","targetHost":"10.0.0.2","targetPool":"backup","intervalMinutes":5}`, nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", rec.Code, rec.Body)
	}
	body := `{"sourcePool":"tank","sourceVolume":"vm100-disk0","targetHost":"10.0.0.2","targetPool":"backup","intervalMinutes":5,"enabled":true}`
	rec = do(h, "POST", "/api/v1/storage/zfs/replications", body, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body)
	}
	var st zfsrep.Status
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	id := st.Spec.ID

	// 3. Sync now: started in the background
	rec = do(h, "POST", "/api/v1/storage/zfs/replications/"+id+"/sync", "", nil)
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"syncing"`) {
		t.Fatalf("expected 202 syncing on sync, got %d: %s", rec.Code, rec.Body)
	}
	s.zfsRep.Wait()

	// 4. Delete task
	rec = do(h, "DELETE", "/api/v1/storage/zfs/replications/"+id, "", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 on delete, got %d: %s", rec.Code, rec.Body)
	}
}

// DRS reads the same snapshot the live stream sends; field names guessed wrong once
// made every host look idle.
func TestDRSPlacementsFromLiveSnapshot(t *testing.T) {
	raw := []byte(`{"host":{"cpuPercent":42.5,"memTotalKiB":1000,"memUsedKiB":250},
		"domains":[{"uuid":"u1","name":"web","state":"running","memoryKiB":512,"cpuPercent":80}]}`)
	var snap live.Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	h, doms := placements("h1", "hv-01", snap)
	if h.CPUPct != 42.5 || h.MemPct != 25 || h.MemTotalB != 1000<<10 || h.VMCount != 1 {
		t.Errorf("host = %+v", h)
	}
	if len(doms) != 1 || doms[0].State != "running" || doms[0].CPUPct != 80 || doms[0].MemUsedB != 512<<10 || doms[0].HostID != "h1" {
		t.Errorf("domains = %+v", doms)
	}
}
