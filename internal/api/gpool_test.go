package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/onurbaydas/virtomir/internal/gpool"
	"github.com/onurbaydas/virtomir/internal/hostinfo"
)

// gpuReader is a host with one card at addr named name (nvidia-smi answers for it).
func gpuReader(t *testing.T, addr, name string) *hostinfo.Reader {
	t.Helper()
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
