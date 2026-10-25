package driver

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeVirtoMir is two members: the volumes live in pool "ssd" of the first, the node's
// machine runs on the second ("b").
type fakeVirtoMir struct {
	mu      sync.Mutex
	vols    map[string]int64 // name → bytes
	disks   []device         // of machine node-1 on member b
	calls   []string
	formats []string
}

func (f *fakeVirtoMir) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer tok" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	op := r.Method + " " + r.URL.Path
	f.calls = append(f.calls, op)
	reply := func(code int, v any) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	switch {
	case op == "GET /api/v1/cluster":
		reply(200, map[string]any{"enabled": true, "members": []map[string]any{{"id": "a", "self": true}, {"id": "b"}}})
	case op == "GET /api/v1/storage/pools/ssd/volumes":
		var out []Volume
		for n, b := range f.vols {
			out = append(out, Volume{Name: n, CapacityBytes: b, UsedBy: []string{}})
		}
		reply(200, map[string]any{"volumes": out})
	case op == "POST /api/v1/storage/pools/ssd/volumes":
		f.vols[body["name"].(string)] = int64(body["sizeGiB"].(float64)) << 30
		f.formats = append(f.formats, body["format"].(string))
		reply(201, map[string]any{})
	case strings.HasPrefix(op, "DELETE /api/v1/storage/pools/ssd/volumes/"):
		name := strings.TrimPrefix(r.URL.Path, "/api/v1/storage/pools/ssd/volumes/")
		if _, ok := f.vols[name]; !ok {
			reply(404, map[string]string{"error": "no such volume"})
			return
		}
		delete(f.vols, name)
		w.WriteHeader(204)
	case op == "GET /api/v1/hosts/b/domains/node-1":
		reply(200, map[string]any{"uuid": "node-1", "devices": f.disks})
	case strings.HasSuffix(op, "/domains/node-1"), strings.Contains(op, "/domains/gone"):
		reply(404, map[string]string{"error": "no such machine"})
	case op == "POST /api/v1/hosts/b/domains/node-1/disks":
		if body["pool"] != "ssd" || body["volume"] == "" {
			reply(422, map[string]string{"error": "bad disk"})
			return
		}
		f.disks = append(f.disks, device{Kind: "disk", Target: "vdb", Serial: body["serial"].(string)})
		reply(201, map[string]string{"target": "vdb"})
	case op == "POST /api/v1/hosts/b/domains/node-1/devices/detach":
		f.disks = nil
		reply(200, map[string]any{})
	default:
		reply(404, map[string]string{"error": "no such API endpoint " + op})
	}
}

func controller(t *testing.T) (*Driver, *fakeVirtoMir) {
	t.Helper()
	f := &fakeVirtoMir{vols: map[string]int64{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	api, err := NewAPI(srv.URL, "tok", false)
	if err != nil {
		t.Fatal(err)
	}
	return &Driver{API: api, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, f
}

var rwo = []*csi.VolumeCapability{{
	AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
	AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER},
}}

func TestCreateDeleteVolume(t *testing.T) {
	d, f := controller(t)
	ctx := context.Background()
	req := &csi.CreateVolumeRequest{Name: "pvc-1", VolumeCapabilities: rwo, Parameters: map[string]string{"pool": "ssd"},
		CapacityRange: &csi.CapacityRange{RequiredBytes: 1<<30 + 1}}
	res, err := d.CreateVolume(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Volume.VolumeId != "-/ssd/pvc-1" || res.Volume.CapacityBytes != 2<<30 || f.formats[0] != "qcow2" {
		t.Errorf("volume %+v, formats %v", res.Volume, f.formats)
	}
	if _, err := d.CreateVolume(ctx, req); err != nil || len(f.formats) != 1 {
		t.Errorf("again: %v, created %d times", err, len(f.formats))
	}
	req.CapacityRange.RequiredBytes = 5 << 30
	if _, err := d.CreateVolume(ctx, req); status.Code(err) != codes.AlreadyExists {
		t.Errorf("bigger: %v", err)
	}
	rwx := []*csi.VolumeCapability{{AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER}}}
	if _, err := d.CreateVolume(ctx, &csi.CreateVolumeRequest{Name: "pvc-2", VolumeCapabilities: rwx, Parameters: map[string]string{"pool": "ssd"}}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("RWX: %v", err)
	}
	if _, err := d.CreateVolume(ctx, &csi.CreateVolumeRequest{Name: "pvc-3", VolumeCapabilities: rwo}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("no pool: %v", err)
	}
	for range 2 {
		if _, err := d.DeleteVolume(ctx, &csi.DeleteVolumeRequest{VolumeId: "-/ssd/pvc-1"}); err != nil {
			t.Errorf("delete: %v", err)
		}
	}
	if len(f.vols) != 0 {
		t.Errorf("left %v", f.vols)
	}
}

func TestPublish(t *testing.T) {
	d, f := controller(t)
	ctx := context.Background()
	req := &csi.ControllerPublishVolumeRequest{VolumeId: "-/ssd/pvc-1", NodeId: "node-1", VolumeCapability: rwo[0]}
	res, err := d.ControllerPublishVolume(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	ser := res.PublishContext["serial"]
	if ser != serial("-/ssd/pvc-1") || len(ser) > 20 || len(f.disks) != 1 || f.disks[0].Serial != ser {
		t.Fatalf("serial %q, disks %+v", ser, f.disks)
	}
	if _, err := d.ControllerPublishVolume(ctx, req); err != nil || len(f.disks) != 1 {
		t.Errorf("again: %v, %d disks", err, len(f.disks))
	}
	if _, err := d.ControllerUnpublishVolume(ctx, &csi.ControllerUnpublishVolumeRequest{VolumeId: "-/ssd/pvc-1", NodeId: "node-1"}); err != nil || len(f.disks) != 0 {
		t.Errorf("unpublish: %v %+v", err, f.disks)
	}
	if _, err := d.ControllerUnpublishVolume(ctx, &csi.ControllerUnpublishVolumeRequest{VolumeId: "-/ssd/pvc-1", NodeId: "gone"}); err != nil {
		t.Errorf("machine gone: %v", err)
	}
	if _, err := d.ControllerPublishVolume(ctx, &csi.ControllerPublishVolumeRequest{VolumeId: "pvc-1", NodeId: "node-1", VolumeCapability: rwo[0]}); status.Code(err) != codes.NotFound {
		t.Errorf("foreign ID: %v", err)
	}
}

type fakeMounter struct {
	mounted map[string]string
	formats []string
}

func (m *fakeMounter) Format(_ context.Context, dev, fs string) error {
	m.formats = append(m.formats, fs+" "+dev)
	return nil
}
func (m *fakeMounter) Mount(_ context.Context, src, dst, _ string, _ []string) error {
	m.mounted[dst] = src
	return nil
}
func (m *fakeMounter) Unmount(_ context.Context, p string) error {
	delete(m.mounted, p)
	return nil
}
func (m *fakeMounter) IsMounted(p string) (bool, error) {
	_, ok := m.mounted[p]
	return ok, nil
}

func TestNode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	dir := t.TempDir()
	byID := filepath.Join(dir, "by-id")
	_ = os.MkdirAll(byID, 0o755)
	dev := filepath.Join(dir, "vdb")
	_ = os.WriteFile(dev, nil, 0o644)
	ser := serial("-/ssd/pvc-1")
	if err := os.Symlink(dev, filepath.Join(byID, "virtio-"+ser)); err != nil {
		t.Fatal(err)
	}
	m := &fakeMounter{mounted: map[string]string{}}
	d := &Driver{NodeID: "node-1", Mounter: m, DevDir: byID}
	ctx := context.Background()
	staging, target := filepath.Join(dir, "staging"), filepath.Join(dir, "pod", "vol")
	pc := map[string]string{"serial": ser}
	if _, err := d.NodeStageVolume(ctx, &csi.NodeStageVolumeRequest{VolumeId: "-/ssd/pvc-1", PublishContext: pc, StagingTargetPath: staging, VolumeCapability: rwo[0]}); err != nil {
		t.Fatal(err)
	}
	if m.mounted[staging] != dev || m.formats[0] != "ext4 "+dev {
		t.Errorf("staged %v formats %v", m.mounted, m.formats)
	}
	if _, err := d.NodePublishVolume(ctx, &csi.NodePublishVolumeRequest{VolumeId: "-/ssd/pvc-1", PublishContext: pc, StagingTargetPath: staging, TargetPath: target, VolumeCapability: rwo[0]}); err != nil {
		t.Fatal(err)
	}
	if m.mounted[target] != staging {
		t.Errorf("published %v", m.mounted)
	}
	if _, err := d.NodeUnpublishVolume(ctx, &csi.NodeUnpublishVolumeRequest{VolumeId: "-/ssd/pvc-1", TargetPath: target}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.NodeUnstageVolume(ctx, &csi.NodeUnstageVolumeRequest{VolumeId: "-/ssd/pvc-1", StagingTargetPath: staging}); err != nil {
		t.Fatal(err)
	}
	if len(m.mounted) != 0 {
		t.Errorf("still mounted %v", m.mounted)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("target left: %v", err)
	}
	info, _ := d.NodeGetInfo(ctx, nil)
	if info.NodeId != "node-1" {
		t.Error(info)
	}
}

func TestMountedIn(t *testing.T) {
	p := filepath.Join(t.TempDir(), "mountinfo")
	_ = os.WriteFile(p, []byte("36 35 98:0 / /mnt/a\\040b rw,noatime - ext4 /dev/vdb rw\n37 35 98:0 / /var/lib/kubelet/x rw - ext4 /dev/vdc rw\n"), 0o644)
	for path, want := range map[string]bool{"/mnt/a b": true, "/var/lib/kubelet/x/": true, "/var/lib": false} {
		if got, err := mountedIn(p, path); err != nil || got != want {
			t.Errorf("%s: %v %v", path, got, err)
		}
	}
}

func TestVolumeID(t *testing.T) {
	for _, s := range []string{"-/ssd/pvc-1", "hv2/ceph/pvc-2"} {
		id, err := parseVolumeID(s)
		if err != nil || id.String() != s {
			t.Errorf("%s: %v %v", s, id, err)
		}
	}
	if _, err := parseVolumeID("ssd/pvc"); err == nil {
		t.Error("two parts accepted")
	}
}
