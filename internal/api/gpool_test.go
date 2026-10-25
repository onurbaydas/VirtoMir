package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/onurbaydas/virtomir/internal/gpool"
	"github.com/onurbaydas/virtomir/internal/hostinfo"
	"github.com/onurbaydas/virtomir/internal/project"
)

// gpuReader is a host with one card at addr named name (nvidia-smi answers for it).
func gpuReader(t *testing.T, addr, name string) *hostinfo.Reader {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("sysfs has colons and symlinks")
	}
	root := t.TempDir()
	dev := filepath.Join(root, "sys/bus/pci/devices", addr)
	for f, v := range map[string]string{"class": "0x030200", "vendor": "0x10de", "device": "0x26b9"} {
		if err := os.MkdirAll(dev, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dev, f), []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []string{"sys/bus/pci/drivers/nvidia", "sys/kernel/iommu_groups/7"} {
		_ = os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	_ = os.Symlink(filepath.Join(root, "sys/bus/pci/drivers/nvidia"), filepath.Join(dev, "driver"))
	_ = os.Symlink(filepath.Join(root, "sys/kernel/iommu_groups/7"), filepath.Join(dev, "iommu_group"))
	return &hostinfo.Reader{Root: root, Command: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("0000" + addr + ", " + name + ", 3, 46068, 512, 40, 80, 350, [N/A], 0\n"), nil
	}}
}

func gpoolNodes(t *testing.T) (*testNode, *testNode) {
	t.Helper()
	a := newTestNode(t, "alpha", nil)
	b := newTestNode(t, "beta", nil)
	for i, n := range []*testNode{a, b} {
		st, _ := gpool.Open("")
		n.api.gpools = st
		n.api.hostinfo = gpuReader(t, []string{"0000:41:00.0", "0000:81:00.0"}[i], []string{"NVIDIA L40S", "NVIDIA A100"}[i])
	}
	joinNodes(t, a, b)
	return a, b
}

func TestClusterGPUs(t *testing.T) {
	a, b := gpoolNodes(t)
	for _, n := range []*testNode{a, b} {
		if _, err := n.api.gpools.Create(gpool.Pool{ID: "l40s", Name: "L40S", Model: "L40S", Kind: gpool.KindWhole}); err != nil {
			t.Fatal(err)
		}
	}
	_, body := a.signIn(t, "view").req("GET", "/api/v1/cluster/gpus", "")
	var res struct{ GPUs []ClusterGPU }
	if err := json.Unmarshal([]byte(body), &res); err != nil || len(res.GPUs) != 2 {
		t.Fatalf("%s", body)
	}
	byHost := map[string]ClusterGPU{}
	for _, g := range res.GPUs {
		byHost[g.HostName] = g
	}
	if g := byHost["alpha"]; g.Name != "NVIDIA L40S" || !g.PassThroughReady || len(g.Pools) != 1 || g.Pools[0] != "l40s" || g.HostID != a.store.SelfID() {
		t.Errorf("alpha's card %+v", g)
	}
	if g := byHost["beta"]; g.Name != "NVIDIA A100" || len(g.Pools) != 0 {
		t.Errorf("beta's card (over the peer protocol) %+v", g)
	}
}

func TestGPUPoolCRUD(t *testing.T) {
	a, b := gpoolNodes(t)
	admin, viewer := a.signIn(t, "onur"), a.signIn(t, "view")
	pool := `{"id":"l40s","name":"L40S render","model":"L40S","kind":"whole"}`
	if resp, _ := viewer.req("POST", "/api/v1/gpu/pools", pool); resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer created a pool: %d", resp.StatusCode)
	}
	if resp, body := admin.req("POST", "/api/v1/gpu/pools", pool); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	if resp, _ := admin.req("POST", "/api/v1/gpu/pools", pool); resp.StatusCode != http.StatusConflict {
		t.Errorf("duplicate: %d", resp.StatusCode)
	}
	if resp, _ := admin.req("POST", "/api/v1/gpu/pools", `{"id":"x1","name":"x","kind":"mig"}`); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("MIG pool without a profile: %d", resp.StatusCode)
	}
	if _, ok := b.api.gpools.Get("l40s"); !ok {
		t.Error("the pool did not reach the other member")
	}

	resp, body := viewer.req("GET", "/api/v1/gpu/pools", "")
	var list struct{ Pools []GPUPoolView }
	if err := json.Unmarshal([]byte(body), &list); err != nil || resp.StatusCode != 200 || len(list.Pools) != 1 {
		t.Fatalf("list: %d %s", resp.StatusCode, body)
	}
	if p := list.Pools[0]; p.Total != 1 || p.Free != 1 || len(p.Hosts) != 1 || p.Hosts[0].HostName != "alpha" {
		t.Errorf("availability %+v", p)
	}
	doc := loadOpenAPI(t)
	c := contractChecker{schemas: doc.Components.Schemas}
	var v any
	_ = json.Unmarshal([]byte(body), &v)
	if errs := c.validate(v, asMap(asMap(asMap(asMap(doc.Paths["/api/v1/gpu/pools"]["get"]["responses"])["2XX"])["content"])["application/json"])["schema"].(map[string]any), ""); len(errs) > 0 {
		t.Errorf("answer does not follow the OpenAPI document: %v", errs)
	}

	if resp, body := admin.req("PUT", "/api/v1/gpu/pools/l40s", `{"name":"L40S","model":"L40S","kind":"whole","perHost":2}`); resp.StatusCode != 200 || !strings.Contains(body, `"perHost":2`) {
		t.Errorf("update: %d %s", resp.StatusCode, body)
	}
	if p, _ := b.api.gpools.Get("l40s"); p.PerHost != 2 {
		t.Error("the update did not reach the other member")
	}

	if _, err := a.projects.Create(project.Project{ID: "render", Name: "Render", Limits: project.Limits{GPUs: map[string]int{"l40s": 2}}}); err != nil {
		t.Fatal(err)
	}
	if resp, body := admin.req("DELETE", "/api/v1/gpu/pools/l40s", ""); resp.StatusCode != http.StatusConflict || !strings.Contains(body, "GPU quota") {
		t.Errorf("deleted a pool a project quota names: %d %s", resp.StatusCode, body)
	}
	_ = a.projects.Delete("render")
	if resp, _ := admin.req("DELETE", "/api/v1/gpu/pools/l40s", ""); resp.StatusCode != http.StatusNoContent {
		t.Errorf("delete: %d", resp.StatusCode)
	}
	if _, ok := b.api.gpools.Get("l40s"); ok {
		t.Error("the delete did not reach the other member")
	}
}

// A pool made while a member was down reaches it with the next member sync (#37).
func TestGPUPoolsCatchUp(t *testing.T) {
	a, b := gpoolNodes(t)
	if _, err := a.api.gpools.Create(gpool.Pool{ID: "t4-2q", Name: "T4 2Q", Kind: gpool.KindMDev, Profile: "nvidia-222"}); err != nil {
		t.Fatal(err)
	}
	b.api.SyncMembers(t.Context())
	if _, ok := b.api.gpools.Get("t4-2q"); !ok {
		t.Error("not caught up")
	}
}
