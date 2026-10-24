package ceph

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// FakeRunner records commands and answers them from a table: the longest key that the
// command line starts with wins; a missing command answers exec.ErrNotFound.
type FakeRunner struct {
	Calls   []string
	Answers map[string]string
	Errors  map[string]error
	Missing map[string]bool
}

func (f *FakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	f.Calls = append(f.Calls, line)
	if f.Missing[name] {
		return nil, exec.ErrNotFound
	}
	best, out, err := "", "", error(nil)
	for k, v := range f.Answers {
		if strings.HasPrefix(line, k) && len(k) > len(best) {
			best, out = k, v
		}
	}
	for k, e := range f.Errors {
		if strings.HasPrefix(line, k) && len(k) >= len(best) {
			best, err = k, e
		}
	}
	return []byte(out), err
}

func (f *FakeRunner) called(prefix string) bool {
	for _, c := range f.Calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// lsblkOf answers `lsblk … <path>` with that disk of lsblk_all.json, as lsblk would.
func lsblkOf(t *testing.T, f *FakeRunner, paths ...string) {
	t.Helper()
	var doc struct {
		BlockDevices []json.RawMessage `json:"blockdevices"`
	}
	if err := json.Unmarshal([]byte(fixture(t, "lsblk_all.json")), &doc); err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		for _, d := range doc.BlockDevices {
			if strings.Contains(string(d), `"path":"`+p+`"`) {
				f.Answers["lsblk "+strings.Join(lsblkArgs, " ")+" "+p] = `{"blockdevices":[` + string(d) + `]}`
			}
		}
	}
}

func newFake() *FakeRunner {
	return &FakeRunner{Answers: map[string]string{}, Errors: map[string]error{}}
}

func TestMain(m *testing.M) {
	Busy = func(path string) bool { return path == "/dev/held" } // the test host's disks do not exist
	os.Exit(m.Run())
}

func TestInspectDisk(t *testing.T) {
	f := newFake()
	lsblkOf(t, f, "/dev/sda", "/dev/sdb", "/dev/sdc", "/dev/nvme0n1", "/dev/sdd")
	ctx := context.Background()
	for _, c := range []struct {
		path, kind, reason string
		empty              bool
	}{
		{"/dev/sdb", "ssd", "", true},
		{"/dev/nvme0n1", "nvme", "", true},
		{"/dev/sda", "hdd", "mounted at /boot/efi; has partitions sda1, sda2", false},
		{"/dev/sdc", "hdd", "is an LVM physical volume", false},
		{"/dev/sdd", "hdd", "has a gpt partition table", false},
	} {
		info, err := InspectDisk(ctx, f, c.path)
		if err != nil {
			t.Fatalf("%s: %v", c.path, err)
		}
		if info.Empty != c.empty || info.DeviceType != c.kind || info.RefusalReason != c.reason {
			t.Errorf("%s: %+v", c.path, info)
		}
	}
	if info, _ := InspectDisk(ctx, f, "/dev/sdb"); info.SizeBytes != 107374182400 {
		t.Errorf("size %d", info.SizeBytes)
	}
	for _, bad := range []string{"sdb", "/dev/../etc/passwd", "/etc/passwd", "/dev/sdb; rm -rf /"} {
		if _, err := InspectDisk(ctx, f, bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestOldLsblkAndZFS(t *testing.T) {
	f := newFake()
	f.Answers["lsblk"] = fixture(t, "lsblk_old.json")
	info, err := InspectDisk(context.Background(), f, "/dev/vdb")
	if err != nil {
		t.Fatal(err)
	}
	if info.Empty || !info.Rotational || info.SizeBytes != 21474836480 || info.RefusalReason != "belongs to a ZFS pool" {
		t.Errorf("%+v", info)
	}
}

func TestEnsureDiskEmpty(t *testing.T) {
	f := newFake()
	lsblkOf(t, f, "/dev/sdb", "/dev/sdc")
	ctx := context.Background()
	if err := EnsureDiskEmpty(ctx, f, "/dev/sdb"); err != nil {
		t.Errorf("empty disk refused: %v", err)
	}
	err := EnsureDiskEmpty(ctx, f, "/dev/sdc")
	if !errors.Is(err, ErrDiskNotEmpty) || !strings.Contains(err.Error(), "wipefs -a") {
		t.Errorf("full disk: %v", err)
	}
}

func TestListDisks(t *testing.T) {
	f := newFake()
	f.Answers["lsblk"] = fixture(t, "lsblk_all.json")
	disks, err := ListDisks(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range disks {
		names = append(names, d.Name)
	}
	if strings.Join(names, ",") != "sda,sdb,sdc,nvme0n1,sdd" {
		t.Errorf("disks %v (no rom, loop or rbd)", names)
	}
}

func clusterFake(t *testing.T) *FakeRunner {
	f := newFake()
	f.Answers["ceph fsid"] = "4b3d7b8a-9e12-4c28-86d3-0fa4b2e8179c\n"
	f.Answers["ceph cephadm get-pub-key"] = "ssh-rsa AAAAB3NzaC1yc2E ceph-4b3d\n"
	f.Answers["ceph auth get-key client.virtomir"] = "AQBkey==\n"
	f.Answers["ceph mon dump -f json"] = fixture(t, "ceph_mon_dump.json")
	f.Answers["ceph orch host ls -f json"] = fixture(t, "ceph_orch_host_ls.json")
	return f
}

func TestBootstrap(t *testing.T) {
	f := clusterFake(t)
	lsblkOf(t, f, "/dev/sdb")
	store, _ := Open(filepath.Join(t.TempDir(), "ceph.json"))
	cfg, err := Bootstrap(context.Background(), f, store, "hv-01", BootstrapSpec{MonIP: "10.0.0.11", ClusterNetwork: "10.10.0.0/24",
		InitialOSDDisk: "/dev/sdb", SingleHostDefaults: true})
	if err != nil {
		t.Fatal(err)
	}
	if !f.called("cephadm bootstrap --mon-ip 10.0.0.11 --skip-dashboard --skip-monitoring-stack --single-host-defaults --cluster-network 10.10.0.0/24") {
		t.Errorf("calls %v", f.Calls)
	}
	if !f.called("ceph auth get-or-create client.virtomir mon profile rbd osd profile rbd mgr profile rbd") ||
		!f.called("ceph orch daemon add osd hv-01:/dev/sdb") {
		t.Errorf("calls %v", f.Calls)
	}
	if cfg.FSID != "4b3d7b8a-9e12-4c28-86d3-0fa4b2e8179c" || cfg.ClientKey != "AQBkey==" || cfg.ClientUser != "virtomir" ||
		strings.Join(cfg.MonEndpoints, ",") != "10.0.0.11:6789,10.0.0.12:6789" || cfg.BootstrapHost != "hv-01" {
		t.Errorf("config %+v", cfg)
	}
	again, _ := Open(store.path)
	if c, ok := again.Config(); !ok || c.FSID != cfg.FSID {
		t.Errorf("not saved: %+v", c)
	}
	if _, err := Bootstrap(context.Background(), f, store, "hv-01", BootstrapSpec{MonIP: "10.0.0.11"}); !errors.Is(err, ErrExists) {
		t.Errorf("second bootstrap: %v", err)
	}
}

func TestBootstrapRefusesDiskWithData(t *testing.T) {
	f := clusterFake(t)
	lsblkOf(t, f, "/dev/sdc")
	store, _ := Open("")
	_, err := Bootstrap(context.Background(), f, store, "hv-01", BootstrapSpec{MonIP: "10.0.0.11", InitialOSDDisk: "/dev/sdc"})
	if !errors.Is(err, ErrDiskNotEmpty) || f.called("cephadm") {
		t.Errorf("err %v, calls %v", err, f.Calls)
	}
	if _, err := Bootstrap(context.Background(), f, store, "hv-01", BootstrapSpec{MonIP: "not-an-ip"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad address: %v", err)
	}
	f.Missing = map[string]bool{"cephadm": true}
	if _, err := Bootstrap(context.Background(), f, store, "hv-01", BootstrapSpec{MonIP: "10.0.0.11"}); err == nil || !strings.Contains(err.Error(), "cephadm is not installed") {
		t.Errorf("no cephadm: %v", err)
	}
}

func TestHostsAndOSDs(t *testing.T) {
	f := clusterFake(t)
	ctx := context.Background()
	hosts, err := Hosts(ctx, f)
	if err != nil || len(hosts) != 3 || hosts[0].Status != "online" || hosts[1].Status != "maintenance" || hosts[2].Status != "offline" {
		t.Fatalf("hosts %+v %v", hosts, err)
	}
	if err := AddHost(ctx, f, "hv-02", "10.0.0.12"); err != nil || !f.called("ceph orch host add hv-02 10.0.0.12") {
		t.Errorf("add host: %v %v", err, f.Calls)
	}
	if err := AddHost(ctx, f, "hv-09", "10.0.0.19"); err == nil {
		t.Error("a host ceph does not list afterwards was accepted")
	}
	if err := AddHost(ctx, f, "hv 02", "10.0.0.12"); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad name: %v", err)
	}
	if err := AddOSD(ctx, f, "hv-02", DiskInfo{Path: "/dev/sdb", Empty: true}); err != nil || !f.called("ceph orch daemon add osd hv-02:/dev/sdb") {
		t.Errorf("add OSD: %v", err)
	}
	f.Calls = nil
	err = AddOSD(ctx, f, "hv-02", DiskInfo{Path: "/dev/sdc", RefusalReason: "is an LVM physical volume"})
	if !errors.Is(err, ErrDiskNotEmpty) || len(f.Calls) != 0 {
		t.Errorf("full disk: %v %v", err, f.Calls)
	}
}

func TestAuthorizePeerKey(t *testing.T) {
	old := AuthorizedKeysFile
	AuthorizedKeysFile = filepath.Join(t.TempDir(), ".ssh", "authorized_keys")
	defer func() { AuthorizedKeysFile = old }()
	if err := os.MkdirAll(filepath.Dir(AuthorizedKeysFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(AuthorizedKeysFile, []byte("ssh-ed25519 AAAAC3Nza onur@laptop"), 0o600); err != nil {
		t.Fatal(err)
	}
	key := "ssh-rsa AAAAB3NzaC1yc2E ceph-4b3d"
	for range 2 {
		if err := AuthorizePeerKey(key); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := os.ReadFile(AuthorizedKeysFile)
	if string(raw) != "ssh-ed25519 AAAAC3Nza onur@laptop\n"+key+"\n" {
		t.Errorf("authorized_keys %q", raw)
	}
	for _, bad := range []string{"", "hello", "ssh-rsa AAAA\nssh-rsa BBBB evil", "command=\"rm -rf /\" ssh-rsa AAAA"} {
		if err := AuthorizePeerKey(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestMaintenanceNoout(t *testing.T) {
	f := clusterFake(t)
	store, _ := Open(filepath.Join(t.TempDir(), "ceph.json"))
	ctx := context.Background()
	if err := EnterHostMaintenance(ctx, f, store, "hv-02"); err == nil {
		t.Error("maintenance without a cluster")
	}
	_ = store.SaveConfig(ClusterConfig{FSID: "x"})
	f.Calls = nil
	for _, h := range []string{"hv-02", "hv-03"} {
		if err := EnterHostMaintenance(ctx, f, store, h); err != nil {
			t.Fatal(err)
		}
	}
	if !f.called("ceph osd set noout") || !f.called("ceph orch host maintenance enter hv-02") {
		t.Errorf("calls %v", f.Calls)
	}
	if c, _ := store.Config(); !c.Noout || strings.Join(c.MaintenanceHosts, ",") != "hv-02,hv-03" {
		t.Errorf("config %+v", c)
	}
	f.Calls = nil
	_ = ExitHostMaintenance(ctx, f, store, "hv-02")
	if f.called("ceph osd unset noout") {
		t.Error("noout cleared while hv-03 is still in maintenance")
	}
	_ = ExitHostMaintenance(ctx, f, store, "hv-03")
	if !f.called("ceph orch host maintenance exit hv-03") || !f.called("ceph osd unset noout") {
		t.Errorf("calls %v", f.Calls)
	}
	if c, _ := store.Config(); c.Noout || len(c.MaintenanceHosts) != 0 {
		t.Errorf("config %+v", c)
	}
	f.Errors["ceph orch host maintenance enter"] = errors.New("ceph orch host maintenance enter: unsafe to stop osd(s) at this time")
	if err := EnterHostMaintenance(ctx, f, store, "hv-01"); err == nil || !strings.Contains(err.Error(), "unsafe to stop") {
		t.Errorf("ceph's refusal lost: %v", err)
	}
	if c, _ := store.Config(); len(c.MaintenanceHosts) != 0 {
		t.Errorf("a refused host counted as in maintenance: %+v", c)
	}
}

func TestTelemetry(t *testing.T) {
	f := newFake()
	f.Answers["ceph status -f json"] = fixture(t, "ceph_status_warn.json")
	f.Answers["ceph health detail -f json"] = fixture(t, "ceph_health_warn.json")
	f.Answers["ceph df detail -f json"] = fixture(t, "ceph_df.json")
	f.Answers["ceph osd tree -f json"] = fixture(t, "ceph_osd_tree.json")
	tel, err := Collect(context.Background(), f, time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	h := tel.Health
	if h.Status != "HEALTH_WARN" || len(h.Checks) != 3 || h.Checks[0].Name != "OSDMAP_FLAGS" || h.Checks[1].Name != "OSD_DOWN" ||
		h.Checks[1].Detail[0] != "osd.1 (root=default,host=hv-02) is down" {
		t.Errorf("health %+v", h)
	}
	if !tel.Daemons.NooutIsSet || tel.Daemons.OSDsUp != 2 || tel.Daemons.Mons != 3 || len(tel.Daemons.Quorum) != 2 || !tel.Daemons.MgrUp {
		t.Errorf("daemons %+v", tel.Daemons)
	}
	if tel.PGs.Total != 128 || tel.PGs.States["active+undersized+degraded"] != 43 || tel.PGs.States["active+clean"] != 85 {
		t.Errorf("pgs %+v", tel.PGs)
	}
	c := tel.Capacity
	if c.TotalBytes != 322122547200 || c.UsedBytes != 64424509440 || c.PercentUsed < 19.9 || c.PercentUsed > 20.1 || len(c.Pools) != 4 ||
		c.Pools[1].Name != "rbd-replicated" || c.Pools[1].PercentUsed < 20.8 || c.Pools[1].PercentUsed > 20.9 {
		t.Errorf("capacity %+v", c)
	}
	var down, in int
	for _, n := range tel.OSDTree {
		if n.Type == "osd" && n.Status == "down" {
			down++
		}
		if n.Type == "osd" && n.In {
			in++
		}
	}
	if len(tel.OSDTree) != 7 || down != 1 || in != 2 || tel.OSDTree[0].Type != "root" || len(tel.OSDTree[0].Children) != 3 {
		t.Errorf("tree %+v", tel.OSDTree)
	}

	f.Answers["ceph health detail -f json"] = fixture(t, "ceph_health_err.json")
	h, _ = Health(context.Background(), f)
	if h.Status != "HEALTH_ERR" || h.Checks[0].Name != "PG_AVAILABILITY" {
		t.Errorf("errors first: %+v", h)
	}
	f.Answers["ceph health detail -f json"] = fixture(t, "ceph_health_ok.json")
	if h, _ = Health(context.Background(), f); h.Status != "HEALTH_OK" || len(h.Checks) != 0 {
		t.Errorf("ok %+v", h)
	}
}

func TestCreateReplicatedPool(t *testing.T) {
	f := newFake()
	if err := CreateRBDPool(context.Background(), f, PoolSpec{Name: "rbd-fast", DeviceClass: "ssd"}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"ceph osd pool create rbd-fast 32 32 replicated",
		"ceph osd pool set rbd-fast size 3",
		"ceph osd crush rule create-replicated replicated-ssd default host ssd",
		"ceph osd pool set rbd-fast crush_rule replicated-ssd",
		"ceph osd pool application enable rbd-fast rbd",
		"rbd pool init rbd-fast",
	}
	if strings.Join(f.Calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls\n%s", strings.Join(f.Calls, "\n"))
	}
	for _, bad := range []PoolSpec{{Name: "a b"}, {Name: "x", ReplicaSize: 7}, {Name: "x", PGNum: 100}, {Name: "x", Type: "mirror"},
		{Name: "x", DeviceClass: "tape"}, {Name: "x", Type: "erasure", ECK: 1}} {
		if err := CreateRBDPool(context.Background(), f, bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
}

func TestCreateErasureCodedPool(t *testing.T) {
	f := newFake()
	if err := CreateRBDPool(context.Background(), f, PoolSpec{Name: "cold", Type: "erasure", ECK: 4, ECM: 2}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"ceph osd erasure-code-profile set ec-cold k=4 m=2 crush-failure-domain=host",
		"ceph osd pool create cold-data 32 32 erasure ec-cold",
		"ceph osd pool set cold-data allow_ec_overwrites true",
		"ceph osd pool application enable cold-data rbd",
		"ceph osd pool create cold 32 32 replicated",
		"ceph osd pool set cold size 3",
		"ceph osd pool application enable cold rbd",
		"rbd pool init cold",
		"rbd config pool set cold rbd_default_data_pool cold-data",
	}
	if strings.Join(f.Calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls\n%s", strings.Join(f.Calls, "\n"))
	}
}

func TestListAndDeletePools(t *testing.T) {
	f := newFake()
	f.Answers["ceph osd pool ls detail -f json"] = fixture(t, "ceph_osd_pool_ls.json")
	f.Answers["ceph df detail -f json"] = fixture(t, "ceph_df.json")
	pools, err := ListRBDPools(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if len(pools) != 2 || pools[0].Name != "rbd-replicated" || pools[0].Type != "replicated" || pools[0].Size != 3 ||
		pools[0].StoredBytes != 21474836480 {
		t.Fatalf("pools %+v", pools)
	}
	if ec := pools[1]; ec.Name != "rbd-ec" || ec.Type != "erasure" || ec.DataPool != "rbd-ec-data" || ec.ECProfile != "ec-rbd-ec" ||
		ec.Size != 6 || ec.StoredBytes != 1073741824 {
		t.Errorf("ec pool %+v", ec)
	}
	f.Calls = nil
	if err := DeleteRBDPool(context.Background(), f, "rbd-ec", "rbd-ec-data"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"ceph config set mon mon_allow_pool_delete true",
		"ceph osd pool rm rbd-ec rbd-ec --yes-i-really-really-mean-it",
		"ceph osd pool rm rbd-ec-data rbd-ec-data --yes-i-really-really-mean-it",
		"ceph config set mon mon_allow_pool_delete false",
	}
	if strings.Join(f.Calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls\n%s", strings.Join(f.Calls, "\n"))
	}
}

func TestLibvirtPool(t *testing.T) {
	p := LibvirtPool(ClusterConfig{MonEndpoints: []string{"10.0.0.11:6789"}, ClientUser: "virtomir", ClientKey: "AQBkey=="}, "rbd-fast", "ceph-fast")
	if p.Name != "ceph-fast" || p.Type != "rbd" || p.CephPool != "rbd-fast" || p.CephUser != "virtomir" || p.CephKey != "AQBkey==" || p.Monitors[0] != "10.0.0.11:6789" {
		t.Errorf("%+v", p)
	}
}

func TestPolicies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ceph.json")
	s, _ := Open(path)
	if _, err := s.CreatePolicy(StoragePolicy{ID: "fast", Name: "Fast", Pool: "ceph-fast", Default: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreatePolicy(StoragePolicy{ID: "cold", Name: "Cold (erasure coded)", Pool: "ceph-cold", Default: true}); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.Policy("fast"); p.Default {
		t.Error("two default policies")
	}
	if _, err := s.CreatePolicy(StoragePolicy{ID: "fast", Name: "x", Pool: "p"}); !errors.Is(err, ErrExists) {
		t.Errorf("duplicate: %v", err)
	}
	for _, bad := range []StoragePolicy{{ID: "Fast", Name: "x", Pool: "p"}, {ID: "x1", Pool: "p"}, {ID: "x1", Name: "x", Pool: "a/b"}} {
		if _, err := s.CreatePolicy(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	if _, err := s.UpdatePolicy(StoragePolicy{ID: "fast", Name: "Fast SSD", Pool: "ceph-ssd"}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePolicy("cold"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePolicy("cold"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete twice: %v", err)
	}
	again, _ := Open(path)
	list := again.Policies()
	if len(list) != 1 || list[0].Name != "Fast SSD" || list[0].Pool != "ceph-ssd" || again.Shared().Version != 4 {
		t.Errorf("reloaded %+v v%d", list, again.Shared().Version)
	}

	other, _ := Open("")
	if ok, err := other.Adopt(again.Shared()); !ok || err != nil || len(other.Policies()) != 1 {
		t.Errorf("adopt: %v %v", ok, err)
	}
	if ok, _ := other.Adopt(PoliciesShared{Version: 1}); ok {
		t.Error("an older version was adopted")
	}
}

func TestCompression(t *testing.T) {
	f := newFake()
	f.Answers["ceph osd pool ls detail -f json"] = fixture(t, "ceph_osd_pool_ls.json")
	f.Answers["ceph df detail -f json"] = fixture(t, "ceph_df.json")
	pools, err := ListRBDPools(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if p := pools[0]; p.CompressionMode != "none" || p.Compressed != 0 {
		t.Errorf("replicated %+v", p)
	}
	if p := pools[1]; p.CompressionMode != "aggressive" || p.CompressionAlgorithm != "zstd" || p.Compressed != 805306368 || p.CompressedFrom != 1342177280 {
		t.Errorf("erasure-coded pool reports its data pool's compression: %+v", p)
	}
	f.Calls = nil
	if err := SetCompression(context.Background(), f, "rbd-ec", "rbd-ec-data", "aggressive", "zstd"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.Calls, "\n") != "ceph osd pool set rbd-ec-data compression_mode aggressive\nceph osd pool set rbd-ec-data compression_algorithm zstd" {
		t.Errorf("calls %v", f.Calls)
	}
	f.Calls = nil
	_ = SetCompression(context.Background(), f, "rbd-replicated", "", "none", "")
	if strings.Join(f.Calls, "\n") != "ceph osd pool set rbd-replicated compression_mode none" {
		t.Errorf("calls %v", f.Calls)
	}
	for _, bad := range [][2]string{{"always", "zstd"}, {"force", "brotli"}} {
		if err := SetCompression(context.Background(), f, "p", "", bad[0], bad[1]); !errors.Is(err, ErrInvalid) {
			t.Errorf("%v: %v", bad, err)
		}
	}
}

func TestHeldAndUsedDisks(t *testing.T) {
	d := diskInfo(lsblkDev{Name: "held", Path: "/dev/held", Type: "disk", Size: 1 << 30})
	if d.Empty || !strings.Contains(d.RefusalReason, "held open") {
		t.Errorf("held disk %+v", d)
	}
	disks := []DiskInfo{{Path: "/dev/sdb", Empty: true}, {Path: "/dev/sdc", Empty: true}, {Path: "/dev/sda", RefusalReason: "mounted at /"}}
	MarkUsed(disks, func(p string) string {
		if p == "/dev/sdb" || p == "/dev/sda" {
			return "is a volume of storage pool san"
		}
		return ""
	})
	if disks[0].Empty || disks[0].RefusalReason != "is a volume of storage pool san" || !disks[1].Empty || disks[2].RefusalReason != "mounted at /" {
		t.Errorf("%+v", disks)
	}
}
