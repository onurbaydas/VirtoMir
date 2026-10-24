package storageeff

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRunner records commands and answers them from a table: the longest key that the
// command line starts with wins.
type fakeRunner struct {
	calls   []string
	answers map[string]string
	errs    map[string]error
	missing bool
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, line)
	if f.missing {
		return nil, exec.ErrNotFound
	}
	best, out := "", ""
	for k, v := range f.answers {
		if strings.HasPrefix(line, k) && len(k) > len(best) {
			best, out = k, v
		}
	}
	for k, e := range f.errs {
		if strings.HasPrefix(line, k) {
			return nil, e
		}
	}
	return []byte(out), nil
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestInspectZFS(t *testing.T) {
	f := &fakeRunner{answers: map[string]string{
		"zfs get": "compression\tlz4\ncompressratio\t1.48x\ndedup\toff\nused\t107374182400\nlogicalused\t158913789952\n",
		"zpool list -H -o dedupratio tank": "1.00x\n",
	}}
	e, err := InspectZFS(context.Background(), f, "tank/vms")
	if err != nil {
		t.Fatal(err)
	}
	if e.Compression != "lz4" || e.CompressRatio != 1.48 || e.Dedup != "off" || e.DedupRatio != 1 || e.SavedBytes != 158913789952-107374182400 {
		t.Errorf("%+v", e)
	}
	if f.calls[0] != "zfs get -H -p -o property,value compression,compressratio,dedup,used,logicalused tank/vms" {
		t.Errorf("calls %v", f.calls)
	}
	if _, err := InspectZFS(context.Background(), f, "tank; rm -rf /"); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad name: %v", err)
	}
	f.missing = true
	if _, err := InspectZFS(context.Background(), f, "tank"); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Errorf("no zfs: %v", err)
	}
}

func TestSetZFSNeedsDedupConfirmation(t *testing.T) {
	f := &fakeRunner{}
	ctx := context.Background()
	err := SetZFS(ctx, f, "tank", ZFSSettings{Dedup: "on"})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "1 to 5 GB") || len(f.calls) != 0 {
		t.Errorf("dedup without confirmation: %v %v", err, f.calls)
	}
	if err := SetZFS(ctx, f, "tank", ZFSSettings{Compression: "zstd", Dedup: "on", AcknowledgeDedupRAM: true}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.calls, "\n") != "zfs set compression=zstd tank\nzfs set dedup=on tank" {
		t.Errorf("calls %v", f.calls)
	}
	f.calls = nil
	if err := SetZFS(ctx, f, "tank", ZFSSettings{Dedup: "off"}); err != nil || f.calls[0] != "zfs set dedup=off tank" {
		t.Errorf("turning dedup off needs no confirmation: %v %v", err, f.calls)
	}
	for _, bad := range []ZFSSettings{{Compression: "brotli"}, {Dedup: "sometimes"}} {
		if err := SetZFS(ctx, f, "tank", bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
}

func TestTiers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "storageeff.json")
	s, _ := Open(path)
	if err := s.SetTier("fast-zfs", "nvme"); err != nil {
		t.Fatal(err)
	}
	_ = s.SetTier("nas", "hdd")
	_ = s.SetTier("nas", "")
	if err := s.SetTier("x", "tape"); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad tier: %v", err)
	}
	again, _ := Open(path)
	if tiers := again.Tiers(); len(tiers) != 1 || tiers["fast-zfs"] != "nvme" || again.Shared().Version != 3 {
		t.Errorf("reloaded %v v%d", tiers, again.Shared().Version)
	}
	other, _ := Open("")
	if ok, err := other.Adopt(again.Shared()); !ok || err != nil || other.Tiers()["fast-zfs"] != "nvme" {
		t.Errorf("adopt %v %v", ok, err)
	}
	if ok, _ := other.Adopt(TiersShared{Version: 1, Tiers: map[string]string{}}); ok {
		t.Error("an older version was adopted")
	}
	if _, err := other.Adopt(TiersShared{Version: 9, Tiers: map[string]string{"p": "tape"}}); err == nil {
		t.Error("a bad tier was adopted")
	}
}

func TestNVMeDiscoverConnectList(t *testing.T) {
	f := &fakeRunner{answers: map[string]string{
		"nvme discover":    fixture(t, "nvme_discover.json"),
		"nvme list-subsys": fixture(t, "nvme_list_subsys.json"),
		"nvme list -o":     fixture(t, "nvme_list.json"),
	}}
	ctx := context.Background()
	found, err := Discover(ctx, f, "192.168.77.50", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 || found[0].NQN != "nqn.2026-01.io.spdk:cnode1" || found[0].Port != 4420 || found[1].Portal != "192.168.77.50" {
		t.Errorf("discovered %+v (no discovery controller, no RDMA)", found)
	}
	if f.calls[0] != "nvme discover -t tcp -a 192.168.77.50 -s 4420 -o json" {
		t.Errorf("calls %v", f.calls)
	}

	store, _ := Open(filepath.Join(t.TempDir(), "s.json"))
	if err := Connect(ctx, f, store, Target{Portal: "192.168.77.50", NQN: "nqn.2026-01.io.spdk:cnode1"}); err != nil {
		t.Fatal(err)
	}
	if f.calls[len(f.calls)-1] != "nvme connect -t tcp -a 192.168.77.50 -s 4420 -n nqn.2026-01.io.spdk:cnode1" || len(store.NVMeTargets()) != 1 {
		t.Errorf("connect %v %v", f.calls, store.NVMeTargets())
	}
	subs, err := List(ctx, f, store)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 2 || len(subs[0].Namespaces) != 1 || subs[0].Transport != "pcie" || subs[0].Managed {
		t.Errorf("local drive %+v", subs[0])
	}
	tcp := subs[1]
	if !tcp.Managed || tcp.State != "live" || !strings.Contains(tcp.Address, "192.168.77.50") || len(tcp.Namespaces) != 2 ||
		tcp.Namespaces[1].Device != "/dev/nvme1n2" || tcp.Namespaces[1].SizeBytes != 214748364800 || tcp.Namespaces[0].Serial != "SPDK00000000000001" {
		t.Errorf("tcp subsystem %+v", tcp)
	}

	f.calls = nil
	if errs := Reconnect(ctx, f, store); len(errs) != 0 || strings.Contains(strings.Join(f.calls, "\n"), "nvme connect") {
		t.Errorf("reconnected a live subsystem: %v %v", errs, f.calls)
	}
	if err := Disconnect(ctx, f, store, "nqn.2026-01.io.spdk:cnode1"); err != nil || len(store.NVMeTargets()) != 0 {
		t.Errorf("disconnect %v", err)
	}
	for _, bad := range []Target{{Portal: "storage.example", NQN: "nqn.2026-01.io.spdk:c"}, {Portal: "10.0.0.1", Port: 70000, NQN: "nqn.2026-01.io.spdk:c"}, {Portal: "10.0.0.1", NQN: "-n evil"}} {
		if err := Connect(ctx, f, store, bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
}

func TestReconnectAfterRestart(t *testing.T) {
	store, _ := Open("")
	_ = store.setNVMe(func([]Target) []Target {
		return []Target{{Portal: "192.168.77.50", Port: 4420, NQN: "nqn.2026-01.io.spdk:cnode2"}}
	})
	f := &fakeRunner{answers: map[string]string{"nvme list-subsys": "[]", "nvme list -o": `{"Devices":[]}`}}
	if errs := Reconnect(context.Background(), f, store); len(errs) != 0 {
		t.Fatal(errs)
	}
	if f.calls[len(f.calls)-1] != "nvme connect -t tcp -a 192.168.77.50 -s 4420 -n nqn.2026-01.io.spdk:cnode2" {
		t.Errorf("calls %v", f.calls)
	}
}
