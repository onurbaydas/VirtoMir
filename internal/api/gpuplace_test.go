package api

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/onurbaydas/virtomir/internal/gpool"
	"github.com/onurbaydas/virtomir/internal/hostinfo"
	"github.com/onurbaydas/virtomir/internal/project"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// gpuFake is a host whose libvirt side hands out GPUs.
type gpuFake struct {
	*fakeBackend
	hw        virt.HostHardware
	failMDev  bool
	attached  []string
	gpuPool   string
	gpuCount  int
	deletedMD []string
}

func (g *gpuFake) HostHardware() (virt.HostHardware, error) { return g.hw, nil }
func (g *gpuFake) AttachHostdev(uuid string, spec virt.HostdevSpec) (string, error) {
	g.attached = append(g.attached, "pci "+spec.Address)
	return "", nil
}
func (g *gpuFake) CreateMDev(spec virt.MDevSpec) (string, error) {
	if g.failMDev {
		return "", errors.New("the card has no room")
	}
	return "new-mdev", nil
}
func (g *gpuFake) DeleteMDev(uuid string) error { g.deletedMD = append(g.deletedMD, uuid); return nil }
func (g *gpuFake) AddDevice(uuid string, spec virt.DeviceSpec) (virt.DeviceResult, error) {
	g.attached = append(g.attached, "mdev "+spec.UUID)
	return virt.DeviceResult{}, nil
}
func (g *gpuFake) SetGPUPool(uuid, pool string, count int) error {
	g.gpuPool, g.gpuCount = pool, count
	return nil
}

func gpuNode(t *testing.T) (*testNode, *gpuFake) {
	t.Helper()
	n := newTestNode(t, "alpha", nil)
	g := &gpuFake{fakeBackend: n.backend, hw: virt.HostHardware{PCI: []virt.HostPCI{{Address: "0000:41:00.0", Product: "AD102GL [L40S]", UsedBy: []string{}}}}}
	n.api.backend = g
	st, _ := gpool.Open("")
	n.api.gpools = st
	n.api.hostinfo = gpuReader(t, "0000:41:00.0", "NVIDIA L40S")
	if _, err := st.Create(gpool.Pool{ID: "l40s", Name: "L40S", Model: "L40S", Kind: gpool.KindWhole}); err != nil {
		t.Fatal(err)
	}
	return n, g
}

func TestCreateWithGPUs(t *testing.T) {
	n, g := gpuNode(t)
	admin := n.signIn(t, "onur")
	resp, body := admin.req("POST", "/api/v1/domains?wait=true", `{"name":"render-01","vcpus":4,"memoryMiB":8192,"start":true,"gpu":{"pool":"l40s","count":1}}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if !slices.Equal(g.attached, []string{"pci 0000:41:00.0"}) || g.gpuPool != "l40s" || g.gpuCount != 1 || g.lastAction != virt.ActionStart {
		t.Errorf("attached %v, recorded %s/%d, started %q", g.attached, g.gpuPool, g.gpuCount, g.lastAction)
	}
	if !slices.Contains(g.calls, "create render-01") || n.api.gpools.Reserved("l40s", n.store.SelfID()) != 0 {
		t.Errorf("calls %v / reservation left behind", g.calls)
	}

	// The card is taken now: a second machine does not get it.
	g.hw.PCI[0].UsedBy = []string{"render-01"}
	if resp, body := admin.req("POST", "/api/v1/domains?wait=true", `{"name":"render-02","gpu":{"pool":"l40s","count":1}}`); resp.StatusCode != http.StatusConflict || !strings.Contains(body, "0 free GPUs") {
		t.Errorf("second machine: %d %s", resp.StatusCode, body)
	}
	if resp, _ := admin.req("POST", "/api/v1/domains?wait=true", `{"name":"x","gpu":{"pool":"nope","count":1}}`); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown pool: %d", resp.StatusCode)
	}
}

func TestCreateWithGPUsRollsBack(t *testing.T) {
	n, g := gpuNode(t)
	if _, err := n.api.gpools.Create(gpool.Pool{ID: "vgpu", Name: "vGPU", Kind: gpool.KindMDev, Profile: "nvidia-222"}); err != nil {
		t.Fatal(err)
	}
	n.api.hostinfo = gpuReaderMDev(t)
	g.failMDev = true
	resp, body := n.signIn(t, "onur").req("POST", "/api/v1/domains?wait=true", `{"name":"vdi-01","gpu":{"pool":"vgpu","count":1}}`)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(body, "removed again") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if !slices.Contains(g.calls, "delete+storage u-new") {
		t.Errorf("machine not removed: %v", g.calls)
	}
}

func TestGPUPlacementAndMoveGuard(t *testing.T) {
	n, g := gpuNode(t)
	admin := n.signIn(t, "onur")
	resp, body := admin.req("POST", "/api/v1/gpu/placement", `{"poolId":"l40s","count":1}`)
	if resp.StatusCode != 200 || !strings.Contains(body, `"hostName":"alpha"`) {
		t.Fatalf("placement: %d %s", resp.StatusCode, body)
	}
	if resp, _ := admin.req("POST", "/api/v1/gpu/placement", `{"poolId":"l40s","count":2}`); resp.StatusCode != http.StatusNoContent {
		t.Errorf("two cards from one: %d", resp.StatusCode)
	}
	g.domains = []virt.Domain{{UUID: "u-gpu", Name: "render-01", GPUPool: "l40s", GPUCount: 1}}
	if !n.api.gpuMoveRefused(discardWriter{}, "u-gpu") || n.api.gpuMoveRefused(discardWriter{}, "u-other") {
		t.Error("move guard")
	}
}

type discardWriter struct{ discard }

func TestGPUQuotaInProjects(t *testing.T) {
	n, g := gpuNode(t)
	if _, err := n.projects.Create(project.Project{ID: "render", Name: "Render", Limits: project.Limits{GPUs: map[string]int{"l40s": 1}}}); err != nil {
		t.Fatal(err)
	}
	g.domains = []virt.Domain{{UUID: "u-1", Name: "render-01", Project: "render", GPUPool: "l40s", GPUCount: 1}}
	resp, body := n.signIn(t, "onur").req("POST", "/api/v1/domains?wait=true", `{"name":"render-02","project":"render","gpu":{"pool":"l40s","count":1}}`)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(body, "GPU quota of 1 for pool l40s") {
		t.Errorf("%d %s", resp.StatusCode, body)
	}
}

// gpuReaderMDev is a host with a Tesla T4 offering 4 slices of vGPU type nvidia-222.
func gpuReaderMDev(t *testing.T) *hostinfo.Reader {
	t.Helper()
	r := gpuReader(t, "0000:81:00.0", "Tesla T4")
	dir := filepath.Join(r.Root, "sys/bus/pci/devices/0000:81:00.0/mdev_supported_types/nvidia-222")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for f, v := range map[string]string{"name": "GRID T4-2Q", "device_api": "vfio-pci", "available_instances": "4"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return r
}
