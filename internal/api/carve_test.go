package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onurbaydas/virtomir/internal/carve"
	"github.com/onurbaydas/virtomir/internal/ceph"
	"github.com/onurbaydas/virtomir/internal/jobs"
	"github.com/onurbaydas/virtomir/internal/virt"
)

func (f *fakeBackend) DeletePool(name string) error {
	f.call("delete pool " + name)
	return f.err
}

func TestCarvePool(t *testing.T) {
	oldBusy, oldBase, oldUnit := ceph.Busy, carve.Base, carve.UnitDir
	ceph.Busy = func(string) bool { return false }
	carve.Base, carve.UnitDir = filepath.Join(t.TempDir(), "srv"), t.TempDir()
	t.Cleanup(func() { ceph.Busy, carve.Base, carve.UnitDir = oldBusy, oldBase, oldUnit })

	a := newTestNode(t, "alpha", nil)
	sdb := `{"name":"sdb","path":"/dev/sdb","size":107374182400,"rota":false,"type":"disk","mountpoint":null,"fstype":null,"pttype":null}`
	sda := `{"name":"sda","path":"/dev/sda","size":512110190592,"rota":false,"type":"disk","mountpoint":null,"fstype":null,"pttype":"gpt","children":[{"name":"sda1","path":"/dev/sda1","size":512110190592,"type":"part","mountpoint":"/","fstype":"ext4"}]}`
	rec := &cephRecorder{answers: map[string]string{
		lsblkPrefix:               `{"blockdevices":[` + sda + `,` + sdb + `]}`,
		lsblkPrefix + " /dev/sdb": `{"blockdevices":[` + sdb + `]}`,
		"vgs":                     `{"report":[{"vg":[{"vg_name":"data","vg_size":"500000000000","vg_free":"200000000000"}]}]}`,
		"blkid":                   "1234-abcd\n",
		"systemd-escape":          "srv-virtomir-fast.mount\n",
	}}
	a.api.cephRunner = rec
	admin, viewer := a.signIn(t, "onur"), a.signIn(t, "view")

	_, body := viewer.req("GET", "/api/v1/storage/sources", "")
	var got struct{ Sources []carve.Source }
	if err := json.Unmarshal([]byte(body), &got); err != nil || len(got.Sources) != 2 ||
		got.Sources[0].ID != "/dev/sdb" || got.Sources[1].ID != "data" {
		t.Fatalf("sources (the system disk must not be one): %s", body)
	}

	if resp, _ := viewer.req("POST", "/api/v1/storage/pools/carve", `{"name":"fast","kind":"vg","source":"data","sizeBytes":10737418240}`); resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer carved a pool: %d", resp.StatusCode)
	}
	for req, why := range map[string]string{
		`{"name":"fast","kind":"vg","source":"data","sizeBytes":300000000000}`:      "more than is free",
		`{"name":"fast","kind":"disk","source":"/dev/sda","sizeBytes":10737418240}`: "the system disk",
	} {
		if resp, body := admin.req("POST", "/api/v1/storage/pools/carve", req); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s", why, resp.StatusCode, body)
		}
	}
	if rec.called("lvcreate") || rec.called("pvcreate") {
		t.Fatal("touched LVM for a refused request")
	}

	resp, body := admin.req("POST", "/api/v1/storage/pools/carve", `{"name":"fast","kind":"vg","source":"data","sizeBytes":10737418240}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("carve: %d %s", resp.StatusCode, body)
	}
	if j := waitJobDone(t, admin, body); j.State != jobs.Succeeded || j.Result != "fast" {
		t.Fatalf("job %+v", j)
	}
	if !rec.called("lvcreate -y -W y -n fast -L 10737418240b data") || !rec.called("mkfs.xfs -q /dev/data/fast") ||
		!strings.Contains(strings.Join(a.backend.calls, " "), "pool fast ") {
		t.Errorf("runner %q, backend %q", rec.calls, a.backend.calls)
	}

	// Giving the space back: only for a pool VirtoMir carved, and only an empty one.
	a.backend.pools = []virt.Pool{{Name: "fast", Path: filepath.Join(carve.Base, "fast")}, {Name: "default", Path: "/var/lib/libvirt/images"}}
	if resp, body := admin.req("DELETE", "/api/v1/storage/pools/default?storage=remove", ""); resp.StatusCode != http.StatusUnprocessableEntity ||
		strings.Contains(strings.Join(a.backend.calls, " "), "delete pool default") {
		t.Errorf("not carved: %d %s %q", resp.StatusCode, body, a.backend.calls)
	}
	if resp, body := admin.req("DELETE", "/api/v1/storage/pools/fast?storage=remove", ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("give back: %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(strings.Join(a.backend.calls, " "), "delete pool fast") || !rec.called("lvremove -y /dev/data/fast") {
		t.Errorf("runner %q, backend %q", rec.calls, a.backend.calls)
	}
}
