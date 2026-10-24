package api

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onurbaydas/virtomir/internal/ceph"
	"github.com/onurbaydas/virtomir/internal/jobs"
	"github.com/onurbaydas/virtomir/internal/storageeff"
	"github.com/onurbaydas/virtomir/internal/virt"
)

func (f *fakeBackend) MoveDisk(_ context.Context, uuid, target, pool string, _ func(string)) error {
	f.call("move-disk " + uuid + " " + target + " " + pool)
	return f.err
}

func (f *fakeBackend) SparsifyDisk(_ context.Context, uuid, target string, _ func(string)) (uint64, error) {
	f.call("sparsify " + uuid + " " + target)
	return 4 << 30, f.err
}

func effNodes(t *testing.T) (*testNode, *testNode, *cephRecorder) {
	t.Helper()
	a, b := newTestNode(t, "alpha", nil), newTestNode(t, "beta", nil)
	rec := &cephRecorder{answers: map[string]string{
		"zfs get":                 "compression\tlz4\ncompressratio\t1.48x\ndedup\toff\nused\t100\nlogicalused\t148\n",
		"zpool list":              "1.00x\n",
		"ceph osd pool ls detail": cephFixture(t, "ceph_osd_pool_ls.json"),
		"ceph df detail -f json":  cephFixture(t, "ceph_df.json"),
	}}
	for _, n := range []*testNode{a, b} {
		st, _ := storageeff.Open(filepath.Join(t.TempDir(), "storageeff.json"))
		cs, _ := ceph.Open("")
		n.api.storageEff, n.api.effRunner, n.api.ceph, n.api.cephRunner = st, rec, cs, rec
		n.backend.pools = []virt.Pool{
			{Name: "default", Type: "dir", Active: true, AvailableBytes: 100 << 30},
			{Name: "fast", Type: "dir", Active: true, AvailableBytes: 50 << 30},
			{Name: "fast-big", Type: "dir", Active: true, AvailableBytes: 900 << 30},
			{Name: "tank", Type: "zfs", Source: "tank/vms", Active: true},
			{Name: "ceph-cold", Type: "rbd", Source: "rbd-ec", Active: true},
		}
	}
	joinNodes(t, a, b)
	return a, b, rec
}

func TestPoolEfficiency(t *testing.T) {
	a, _, rec := effNodes(t)
	admin, viewer := a.signIn(t, "onur"), a.signIn(t, "view")
	_, body := viewer.req("GET", "/api/v1/storage/pools/tank/efficiency", "")
	var e PoolEfficiency
	if err := json.Unmarshal([]byte(body), &e); err != nil || e.Kind != "zfs" || e.ZFS == nil || e.ZFS.CompressRatio != 1.48 || e.ZFS.SavedBytes != 48 {
		t.Fatalf("zfs %s", body)
	}
	if !rec.called("compression,compressratio,dedup,used,logicalused tank/vms") {
		t.Error("asked the wrong dataset")
	}
	if resp, _ := viewer.req("PUT", "/api/v1/storage/pools/tank/zfs", `{"compression":"zstd"}`); resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer changed compression: %d", resp.StatusCode)
	}
	resp, body := admin.req("PUT", "/api/v1/storage/pools/tank/zfs", `{"dedup":"on"}`)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "RAM") {
		t.Errorf("dedup without confirmation: %d %s", resp.StatusCode, body)
	}
	if resp, _ := admin.req("PUT", "/api/v1/storage/pools/tank/zfs", `{"dedup":"on","acknowledgeDedupRam":true}`); resp.StatusCode != http.StatusOK || !rec.called("zfs set dedup=on tank/vms") {
		t.Errorf("dedup confirmed: %d", resp.StatusCode)
	}
	if resp, _ := admin.req("PUT", "/api/v1/storage/pools/default/zfs", `{"compression":"lz4"}`); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("zfs settings on a directory pool: %d", resp.StatusCode)
	}
	_, body = viewer.req("GET", "/api/v1/storage/pools/ceph-cold/efficiency", "")
	if !strings.Contains(body, `"kind":"ceph"`) || !strings.Contains(body, "manages the cluster") {
		t.Errorf("ceph pool on a member that does not manage it: %s", body)
	}
	_ = a.api.ceph.SaveConfig(ceph.ClusterConfig{FSID: "x"})
	_, body = viewer.req("GET", "/api/v1/storage/pools/ceph-cold/efficiency", "")
	if !strings.Contains(body, `"compressionMode":"aggressive"`) || !strings.Contains(body, `"compressedBytes":805306368`) {
		t.Errorf("ceph efficiency %s", body)
	}
	if resp, _ := admin.req("PUT", "/api/v1/ceph/pools/rbd-ec/compression", `{"mode":"force","algorithm":"lz4"}`); resp.StatusCode != http.StatusNoContent ||
		!rec.called("ceph osd pool set rbd-ec-data compression_mode force") {
		t.Errorf("ceph compression: %d", resp.StatusCode)
	}
	if resp, _ := viewer.req("GET", "/api/v1/storage/pools/nope/efficiency", ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown pool: %d", resp.StatusCode)
	}
}

func TestSparsifyOnlyStoppedMachines(t *testing.T) {
	a, _, _ := effNodes(t)
	admin := a.signIn(t, "onur")
	a.backend.detail = virt.DomainDetail{Domain: virt.Domain{UUID: "u1", Name: "web", State: virt.StateRunning}}
	if resp, body := admin.req("POST", "/api/v1/domains/u1/disks/vda/sparsify", ""); resp.StatusCode != http.StatusConflict || !strings.Contains(body, "shut it down") {
		t.Errorf("running machine: %d %s", resp.StatusCode, body)
	}
	if strings.Contains(strings.Join(a.backend.calls, " "), "sparsify") {
		t.Error("sparsified a running machine")
	}
	if resp, _ := a.signIn(t, "op").req("POST", "/api/v1/domains/u1/disks/vda/sparsify", ""); resp.StatusCode != http.StatusForbidden {
		t.Errorf("operator compacted a disk: %d", resp.StatusCode)
	}
	a.backend.detail.State = virt.StateShutoff
	resp, body := admin.req("POST", "/api/v1/domains/u1/disks/vda/sparsify", "")
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("stopped machine: %d %s", resp.StatusCode, body)
	}
	if j := waitJobDone(t, admin, body); j.State != jobs.Succeeded || j.Result != "4294967296" {
		t.Errorf("job %+v", j)
	}
}

func TestTiersAndMoveToTier(t *testing.T) {
	a, b, _ := effNodes(t)
	admin := a.signIn(t, "onur")
	for pool, tier := range map[string]string{"fast": "nvme", "fast-big": "nvme", "default": "hdd"} {
		if resp, _ := admin.req("PUT", "/api/v1/storage/tiers/"+pool, `{"tier":"`+tier+`"}`); resp.StatusCode != http.StatusNoContent {
			t.Fatalf("set tier: %d", resp.StatusCode)
		}
	}
	if resp, _ := admin.req("PUT", "/api/v1/storage/tiers/default", `{"tier":"tape"}`); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("bad tier: %d", resp.StatusCode)
	}
	if b.api.storageEff.Tiers()["fast"] != "nvme" {
		t.Errorf("tiers not on beta: %v", b.api.storageEff.Tiers())
	}
	a.backend.detail = virt.DomainDetail{Domain: virt.Domain{UUID: "u1", Name: "db"}, Devices: []virt.Device{{Kind: "disk", Target: "vda", CapacityBytes: 80 << 30}}}
	resp, body := admin.req("POST", "/api/v1/domains/u1/disks/vda/tier", `{"tier":"nvme"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("move: %d %s", resp.StatusCode, body)
	}
	if j := waitJobDone(t, admin, body); j.Result != "fast-big" || !strings.Contains(strings.Join(a.backend.calls, "\n"), "move-disk u1 vda fast-big") {
		t.Errorf("the tier's pool with room: %+v %v", j, a.backend.calls)
	}
	a.backend.detail.Devices[0].CapacityBytes = 2 << 40
	if resp, body := admin.req("POST", "/api/v1/domains/u1/disks/vda/tier", `{"tier":"nvme"}`); resp.StatusCode != http.StatusConflict || !strings.Contains(body, "room") {
		t.Errorf("no room: %d %s", resp.StatusCode, body)
	}
	if resp, _ := admin.req("POST", "/api/v1/domains/u1/disks/vda/tier", `{"tier":"ssd"}`); resp.StatusCode != http.StatusConflict {
		t.Errorf("tier without pools: %d", resp.StatusCode)
	}
}

func TestNVMeEndpoints(t *testing.T) {
	a, _, rec := effNodes(t)
	rec.answers["nvme discover"] = `{"records":[{"trtype":"tcp","subtype":"nvme subsystem","trsvcid":"4420","subnqn":"nqn.2026-01.io.spdk:cnode1","traddr":"192.168.77.50"}]}`
	rec.answers["nvme list-subsys"] = `[{"Subsystems":[{"Name":"nvme-subsys1","NQN":"nqn.2026-01.io.spdk:cnode1","Paths":[{"Name":"nvme1","Transport":"tcp","Address":"traddr=192.168.77.50,trsvcid=4420","State":"live"}]}]}]`
	rec.answers["nvme list -o json"] = `{"Devices":[{"NameSpace":1,"DevicePath":"/dev/nvme1n1","ModelNumber":"SPDK bdev Controller","SerialNumber":"S1","PhysicalSize":107374182400}]}`
	admin := a.signIn(t, "onur")
	_, body := admin.req("POST", "/api/v1/storage/nvme/discover", `{"portal":"192.168.77.50"}`)
	if !strings.Contains(body, "cnode1") {
		t.Errorf("discover %s", body)
	}
	if resp, _ := a.signIn(t, "op").req("POST", "/api/v1/storage/nvme/connect", `{"portal":"192.168.77.50","nqn":"nqn.2026-01.io.spdk:cnode1"}`); resp.StatusCode != http.StatusForbidden {
		t.Errorf("operator connected storage: %d", resp.StatusCode)
	}
	if resp, _ := admin.req("POST", "/api/v1/storage/nvme/connect", `{"portal":"192.168.77.50","nqn":"nqn.2026-01.io.spdk:cnode1"}`); resp.StatusCode != http.StatusNoContent ||
		!rec.called("nvme connect -t tcp -a 192.168.77.50 -s 4420 -n nqn.2026-01.io.spdk:cnode1") {
		t.Errorf("connect %d", resp.StatusCode)
	}
	_, body = a.signIn(t, "view").req("GET", "/api/v1/storage/nvme", "")
	if !strings.Contains(body, `"device":"/dev/nvme1n1"`) || !strings.Contains(body, `"managed":true`) {
		t.Errorf("list %s", body)
	}
	if resp, _ := admin.req("POST", "/api/v1/storage/nvme/disconnect", `{"nqn":"nqn.2026-01.io.spdk:cnode1"}`); resp.StatusCode != http.StatusNoContent {
		t.Errorf("disconnect %d", resp.StatusCode)
	}
	if resp, _ := admin.req("POST", "/api/v1/storage/nvme/connect", `{"portal":"nas.local","nqn":"nqn.2026-01.io.spdk:cnode1"}`); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("bad portal: %d", resp.StatusCode)
	}
}
