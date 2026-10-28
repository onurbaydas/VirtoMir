package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onurbaydas/virtomir/internal/jobs"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// testDiskUsers is what the fake backend says machines use.
var testDiskUsers = map[string][]string{}

func (f *fakeBackend) DiskUsers() (map[string][]string, error) { return testDiskUsers, nil }
func (f *fakeBackend) PoolAction(name, action string) error {
	f.call("pool " + action + " " + name)
	return nil
}

func TestDatastoreBrowser(t *testing.T) {
	a := newTestNode(t, "alpha", nil)
	dir, other := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "web.qcow2"), []byte("QFI\xfbdisk"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.backend.pools = []virt.Pool{
		{Name: "images", Type: "dir", Path: dir, Active: true},
		{Name: "nas", Type: "netfs", Path: other, Active: true},
		{Name: "lun", Type: "iscsi", Path: "/dev/disk/by-path", Active: true},
	}
	testDiskUsers = map[string][]string{filepath.Join(dir, "web.qcow2"): {"web"}}
	t.Cleanup(func() { testDiskUsers = map[string][]string{} })
	admin, viewer := a.signIn(t, "onur"), a.signIn(t, "view")

	_, body := viewer.req("GET", "/api/v1/storage/pools/images/files", "")
	var got struct {
		Entries []struct {
			Name   string
			Format string
			UsedBy []string
		}
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil || len(got.Entries) != 1 || got.Entries[0].Format != "qcow2" || got.Entries[0].UsedBy[0] != "web" {
		t.Fatalf("list: %s", body)
	}
	if resp, _ := viewer.req("GET", "/api/v1/storage/pools/images/files/download?path=web.qcow2", ""); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a viewer downloaded a disk image: %d", resp.StatusCode)
	}
	if resp, body := admin.req("GET", "/api/v1/storage/pools/images/files/download?path=web.qcow2", ""); resp.StatusCode != http.StatusOK || body != "QFI\xfbdisk" ||
		!strings.Contains(resp.Header.Get("Content-Disposition"), "web.qcow2") {
		t.Errorf("download: %d %q", resp.StatusCode, body)
	}
	for p, want := range map[string]int{"../etc/passwd": http.StatusUnprocessableEntity, "nope.iso": http.StatusNotFound} {
		if resp, _ := admin.req("GET", "/api/v1/storage/pools/images/files/download?path="+p, ""); resp.StatusCode != want {
			t.Errorf("%s: %d, want %d", p, resp.StatusCode, want)
		}
	}
	if resp, _ := admin.req("GET", "/api/v1/storage/pools/lun/files", ""); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("browsed a LUN pool: %d", resp.StatusCode)
	}
	if resp, body := admin.req("DELETE", "/api/v1/storage/pools/images/files?path=web.qcow2", ""); resp.StatusCode != http.StatusConflict || !strings.Contains(body, "web") {
		t.Errorf("deleted a machine's disk: %d %s", resp.StatusCode, body)
	}
	if resp, _ := viewer.req("POST", "/api/v1/storage/pools/images/files/folder", `{"path":"isos"}`); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a viewer made a folder: %d", resp.StatusCode)
	}
	if resp, _ := admin.req("POST", "/api/v1/storage/pools/images/files/folder", `{"path":"isos"}`); resp.StatusCode != http.StatusNoContent {
		t.Errorf("folder: %d", resp.StatusCode)
	}
	testDiskUsers = map[string][]string{}
	resp, body := admin.req("POST", "/api/v1/storage/pools/images/files/copy", `{"path":"web.qcow2","toPool":"nas","toPath":"web.qcow2","move":true}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("move: %d %s", resp.StatusCode, body)
	}
	if j := waitJobDone(t, admin, body); j.State != jobs.Succeeded {
		t.Fatalf("move job %+v", j)
	}
	if _, err := os.Stat(filepath.Join(other, "web.qcow2")); err != nil {
		t.Error("not moved to the NAS pool")
	}
	if !strings.Contains(strings.Join(a.backend.calls, " "), "pool refresh nas") {
		t.Errorf("libvirt not told: %q", a.backend.calls)
	}
}
