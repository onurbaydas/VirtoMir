package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/onurbaydas/virtomir/internal/ceph"
	"github.com/onurbaydas/virtomir/internal/jobs"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// cephRecorder stands in for cephadm, ceph, rbd and lsblk: it records the commands and
// answers from a table (the longest matching command prefix wins).
type cephRecorder struct {
	mu      sync.Mutex
	calls   []string
	answers map[string]string
}

func (c *cephRecorder) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	line := name + " " + strings.Join(args, " ")
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, line)
	best, out := "", ""
	for k, v := range c.answers {
		if strings.HasPrefix(line, k) && len(k) > len(best) {
			best, out = k, v
		}
	}
	return []byte(out), nil
}

func (c *cephRecorder) called(part string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, l := range c.calls {
		if strings.Contains(l, part) {
			return true
		}
	}
	return false
}

func (f *fakeBackend) CreatePool(spec virt.PoolSpec) error {
	f.call("pool " + spec.Name + " " + spec.Type + " " + spec.CephPool + " " + spec.CephUser + " " + strings.Join(spec.Monitors, ","))
	return f.err
}

func cephFixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "ceph", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

const lsblkPrefix = "lsblk -J -b -o NAME,PATH,SIZE,ROTA,TYPE,MOUNTPOINT,FSTYPE,PTTYPE"

func cephNodes(t *testing.T) (*testNode, *testNode, *cephRecorder, *cephRecorder) {
	t.Helper()
	old, oldBusy := cephLookPath, ceph.Busy
	cephLookPath = func(string) bool { return true }
	ceph.Busy = func(string) bool { return false } // the disks are fakes; a CI runner has a real, mounted /dev/sdb
	t.Cleanup(func() { cephLookPath, ceph.Busy = old, oldBusy })
	a, b := newTestNode(t, "alpha", nil), newTestNode(t, "beta", nil)
	recA := &cephRecorder{answers: map[string]string{
		"ceph fsid":                         "4b3d7b8a-9e12-4c28-86d3-0fa4b2e8179c\n",
		"ceph cephadm get-pub-key":          "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5 ceph-4b3d\n",
		"ceph auth get-key client.virtomir": "AQBkey==\n",
		"ceph mon dump -f json":             cephFixture(t, "ceph_mon_dump.json"),
		"ceph status -f json":               cephFixture(t, "ceph_status_ok.json"),
		"ceph health detail -f json":        cephFixture(t, "ceph_health_ok.json"),
		"ceph df detail -f json":            cephFixture(t, "ceph_df.json"),
		"ceph osd tree -f json":             cephFixture(t, "ceph_osd_tree.json"),
		"ceph osd pool ls detail -f json":   cephFixture(t, "ceph_osd_pool_ls.json"),
		"ceph orch host ls -f json":         `[{"addr":"10.0.0.11","hostname":"alpha","labels":["_admin"],"status":""},{"addr":"127.0.0.1","hostname":"beta","labels":[],"status":""}]`,
		lsblkPrefix + " /dev/sdb":           `{"blockdevices":[{"name":"sdb","path":"/dev/sdb","size":107374182400,"rota":false,"type":"disk","mountpoint":null,"fstype":null,"pttype":null}]}`,
	}}
	recB := &cephRecorder{answers: map[string]string{
		lsblkPrefix + " /dev/sdb": `{"blockdevices":[{"name":"sdb","path":"/dev/sdb","size":107374182400,"rota":false,"type":"disk","mountpoint":null,"fstype":null,"pttype":null}]}`,
		lsblkPrefix + " /dev/sdc": `{"blockdevices":[{"name":"sdc","path":"/dev/sdc","size":107374182400,"rota":true,"type":"disk","mountpoint":null,"fstype":"LVM2_member","pttype":null}]}`,
	}}
	for n, rec := range map[*testNode]*cephRecorder{a: recA, b: recB} {
		st, _ := ceph.Open(filepath.Join(t.TempDir(), "ceph.json"))
		n.api.ceph, n.api.cephRunner = st, rec
	}
	joinNodes(t, a, b)
	return a, b, recA, recB
}

func waitJobDone(t *testing.T, br *browser, body string) jobs.Job {
	t.Helper()
	var j jobs.Job
	_ = json.Unmarshal([]byte(body), &j)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, raw := br.req("GET", "/api/v1/jobs/"+j.ID, "")
		_ = json.Unmarshal([]byte(raw), &j)
		if j.State != jobs.Running {
			return j
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job still running: %+v", j)
	return j
}

func TestCephBootstrapAndStatus(t *testing.T) {
	a, b, recA, _ := cephNodes(t)
	admin, viewer := a.signIn(t, "onur"), a.signIn(t, "view")

	if resp, _ := viewer.req("POST", "/api/v1/ceph/bootstrap", `{"monIp":"10.0.0.11"}`); resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer bootstrapped: %d", resp.StatusCode)
	}
	if resp, _ := admin.req("POST", "/api/v1/ceph/bootstrap", `{"monIp":"not-an-ip"}`); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("bad address: %d", resp.StatusCode)
	}
	resp, body := admin.req("POST", "/api/v1/ceph/bootstrap", `{"monIp":"10.0.0.11","initialOsdDisk":"/dev/sdb"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("bootstrap: %d %s", resp.StatusCode, body)
	}
	if j := waitJobDone(t, admin, body); j.State != jobs.Succeeded || j.Result != "4b3d7b8a-9e12-4c28-86d3-0fa4b2e8179c" {
		t.Fatalf("job %+v", j)
	}
	if !recA.called("cephadm bootstrap --mon-ip 10.0.0.11") || !recA.called("ceph orch daemon add osd alpha:/dev/sdb") {
		t.Errorf("calls %v", recA.calls)
	}

	_, body = viewer.req("GET", "/api/v1/ceph/status", "")
	var st CephStatus
	if err := json.Unmarshal([]byte(body), &st); err != nil || !st.Managed || st.Config == nil || st.Telemetry == nil {
		t.Fatalf("status %s", body)
	}
	if strings.Contains(body, "AQBkey") || st.Telemetry.Health.Status != "HEALTH_OK" || st.Telemetry.Daemons.OSDs != 3 {
		t.Errorf("status leaks the key or misses telemetry: %s", body)
	}
	_, body = b.signIn(t, "view").req("GET", "/api/v1/ceph/status", "")
	if err := json.Unmarshal([]byte(body), &st); err != nil || st.Managed || st.ManagedBy == nil || st.ManagedBy.Name != "alpha" {
		t.Errorf("beta does not point at alpha: %s", body)
	}
	if resp, _ := b.signIn(t, "onur").req("GET", "/api/v1/ceph/pools", ""); resp.StatusCode != http.StatusConflict {
		t.Errorf("pools on a member that manages nothing: %d", resp.StatusCode)
	}
	if resp, _ := admin.req("POST", "/api/v1/ceph/bootstrap", `{"monIp":"10.0.0.11"}`); resp.StatusCode != http.StatusConflict {
		t.Errorf("second bootstrap: %d", resp.StatusCode)
	}
}

func TestCephHostsAndOSDs(t *testing.T) {
	a, b, recA, recB := cephNodes(t)
	_ = a.api.ceph.SaveConfig(ceph.ClusterConfig{FSID: "x", SSHPublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5 ceph-4b3d", ClientUser: "virtomir"})
	old := ceph.AuthorizedKeysFile
	ceph.AuthorizedKeysFile = filepath.Join(t.TempDir(), "authorized_keys")
	defer func() { ceph.AuthorizedKeysFile = old }()
	admin := a.signIn(t, "onur")

	resp, body := admin.req("POST", "/api/v1/ceph/hosts", `{"member":"`+b.store.SelfID()+`"}`)
	if resp.StatusCode != http.StatusOK || !recA.called("ceph orch host add beta 127.0.0.1") {
		t.Fatalf("add host: %d %s %v", resp.StatusCode, body, recA.calls)
	}
	if raw, _ := os.ReadFile(ceph.AuthorizedKeysFile); !strings.Contains(string(raw), "ceph-4b3d") {
		t.Errorf("beta's root does not trust the cluster key: %q", raw)
	}
	_, body = admin.req("GET", "/api/v1/ceph/hosts", "")
	if !strings.Contains(body, `"memberId":"`+b.store.SelfID()+`"`) {
		t.Errorf("hosts %s", body)
	}

	resp, body = admin.req("POST", "/api/v1/ceph/osds", `{"hostname":"beta","disk":"/dev/sdc"}`)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "wipefs") || recA.called("add osd beta:/dev/sdc") {
		t.Errorf("disk with data: %d %s", resp.StatusCode, body)
	}
	if !recB.called(lsblkPrefix + " /dev/sdc") {
		t.Error("beta did not inspect its own disk")
	}
	resp, body = admin.req("POST", "/api/v1/ceph/osds", `{"hostname":"beta","disk":"/dev/sdb"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("empty disk: %d %s", resp.StatusCode, body)
	}
	if j := waitJobDone(t, admin, body); j.State != jobs.Succeeded || !recA.called("ceph orch daemon add osd beta:/dev/sdb") {
		t.Errorf("job %+v %v", j, recA.calls)
	}
	if resp, _ := admin.req("POST", "/api/v1/ceph/osds", `{"hostname":"gamma","disk":"/dev/sdb"}`); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("a host outside the cluster: %d", resp.StatusCode)
	}

	if resp, _ := admin.req("POST", "/api/v1/ceph/maintenance/enter", `{"hostname":"beta"}`); resp.StatusCode != http.StatusNoContent ||
		!recA.called("ceph osd set noout") || !recA.called("ceph orch host maintenance enter beta") {
		t.Errorf("maintenance: %d %v", resp.StatusCode, recA.calls)
	}
	if c, _ := a.api.ceph.Config(); !c.Noout || len(c.MaintenanceHosts) != 1 {
		t.Errorf("config %+v", c)
	}
}

func TestCephPoolOnEveryHost(t *testing.T) {
	a, b, recA, _ := cephNodes(t)
	_ = a.api.ceph.SaveConfig(ceph.ClusterConfig{FSID: "x", MonEndpoints: []string{"10.0.0.11:6789"}, ClientUser: "virtomir", ClientKey: "AQBkey=="})
	admin := a.signIn(t, "onur")
	resp, body := admin.req("POST", "/api/v1/ceph/pools", `{"name":"rbd-fast","type":"replicated","replicaSize":2,"libvirtPool":"ceph-fast"}`)
	if resp.StatusCode != http.StatusCreated || !recA.called("rbd pool init rbd-fast") || !recA.called("ceph osd pool set rbd-fast size 2") {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	want := "pool ceph-fast rbd rbd-fast virtomir 10.0.0.11:6789"
	for _, n := range []*testNode{a, b} {
		if !strings.Contains(strings.Join(n.backend.calls, "\n"), want) {
			t.Errorf("libvirt pool missing on a member: %v", n.backend.calls)
		}
	}
	if resp, _ := admin.req("POST", "/api/v1/ceph/pools", `{"name":"x","replicaSize":9}`); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("bad spec: %d", resp.StatusCode)
	}
	_, body = admin.req("GET", "/api/v1/ceph/pools", "")
	if !strings.Contains(body, `"name":"rbd-ec","type":"erasure"`) {
		t.Errorf("pools %s", body)
	}
	if resp, body := admin.req("POST", "/api/v1/ceph/pools/rbd-ec/libvirt", `{"libvirtPool":"ceph-cold"}`); resp.StatusCode != http.StatusOK ||
		!strings.Contains(strings.Join(b.backend.calls, "\n"), "pool ceph-cold rbd rbd-ec virtomir") {
		t.Errorf("register later: %d %s %v", resp.StatusCode, body, b.backend.calls)
	}
	if resp, _ := admin.req("DELETE", "/api/v1/ceph/pools/rbd-replicated", ""); resp.StatusCode != http.StatusNoContent ||
		!recA.called("ceph osd pool rm rbd-replicated rbd-replicated --yes-i-really-really-mean-it") {
		t.Errorf("delete: %d", resp.StatusCode)
	}
	if resp, _ := admin.req("DELETE", "/api/v1/ceph/pools/nope", ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("delete unknown: %d", resp.StatusCode)
	}
}

func TestStoragePoliciesAcrossTheCluster(t *testing.T) {
	a, b, _, _ := cephNodes(t)
	admin := a.signIn(t, "onur")
	if resp, _ := a.signIn(t, "op").req("POST", "/api/v1/storage/policies", `{"id":"fast","name":"Fast","pool":"ceph-fast"}`); resp.StatusCode != http.StatusForbidden {
		t.Errorf("operator created a policy: %d", resp.StatusCode)
	}
	resp, body := admin.req("POST", "/api/v1/storage/policies", `{"id":"fast","name":"Fast","pool":"ceph-fast","default":true}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	if p, ok := b.api.ceph.Policy("fast"); !ok || p.Pool != "ceph-fast" {
		t.Errorf("not copied to beta: %+v", p)
	}
	if resp, _ := admin.req("POST", "/api/v1/domains?wait=true", `{"name":"web","vcpus":1,"memoryMiB":512,"diskGiB":10}`); resp.StatusCode >= 300 || a.backend.lastCreate.Pool != "ceph-fast" {
		t.Errorf("the default policy did not pick the pool: %d %q", resp.StatusCode, a.backend.lastCreate.Pool)
	}
	if resp, _ := admin.req("POST", "/api/v1/domains?wait=true", `{"name":"db","vcpus":1,"memoryMiB":512,"diskGiB":10,"storagePolicy":"nope"}`); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("unknown policy: %d", resp.StatusCode)
	}
	if resp, _ := admin.req("PUT", "/api/v1/storage/policies/fast", `{"name":"Fast SSD","pool":"ceph-ssd"}`); resp.StatusCode != http.StatusOK {
		t.Errorf("update: %d", resp.StatusCode)
	}
	if resp, _ := admin.req("DELETE", "/api/v1/storage/policies/fast", ""); resp.StatusCode != http.StatusNoContent {
		t.Errorf("delete: %d", resp.StatusCode)
	}
	if _, ok := b.api.ceph.Policy("fast"); ok {
		t.Error("still on beta")
	}
}
