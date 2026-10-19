package dr

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kdomanski/iso9660"
	"libvirt.org/go/libvirtxml"
)

type fakeCall struct {
	name string
	args []string
}

func (c fakeCall) joined() string { return strings.Join(append([]string{c.name}, c.args...), " ") }

// fakeRunner records every command and answers from a handler.
type fakeRunner struct {
	calls   []fakeCall
	ctxs    []context.Context
	handler func(name string, args []string) ([]byte, error)
}

func (f *fakeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, fakeCall{name, append([]string(nil), args...)})
	f.ctxs = append(f.ctxs, ctx)
	if f.handler != nil {
		return f.handler(name, args)
	}
	return nil, nil
}

func (f *fakeRunner) find(sub string) (fakeCall, bool) {
	for _, c := range f.calls {
		if strings.Contains(c.joined(), sub) {
			return c, true
		}
	}
	return fakeCall{}, false
}

func TestFindLatestReplicaSnapshot(t *testing.T) {
	out := strings.Join([]string{
		"tank/vms/web-disk0@virtomir-rep-20261017-020000",
		"tank/vms/web-disk0@virtomir-rep-20261018-020000",
		"tank/vms/web-disk0@virtomir-rep-20261019-020000",
		"tank/vms/web-disk0@manual-backup",
		"",
	}, "\n")
	r := &fakeRunner{handler: func(string, []string) ([]byte, error) { return []byte(out), nil }}

	got, err := FindLatestReplicaSnapshot(context.Background(), r, "tank/vms/web-disk0", "")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if got != "virtomir-rep-20261019-020000" {
		t.Errorf("latest replica snapshot = %q", got)
	}
	if _, ok := r.find("zfs list -H -t snapshot -o name -s creation tank/vms/web-disk0"); !ok {
		t.Errorf("unexpected command: %v", r.calls)
	}

	// An explicit DR-specific prefix finds nothing in a replication-only output.
	if _, err := FindLatestReplicaSnapshot(context.Background(), r, "tank/vms/web-disk0", DRPrefix); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound for missing prefix, got %v", err)
	}

	// No snapshots at all is not found either.
	empty := &fakeRunner{handler: func(string, []string) ([]byte, error) { return []byte("\n"), nil }}
	if _, err := FindLatestReplicaSnapshot(context.Background(), empty, "tank/vms/none", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound for empty list, got %v", err)
	}
}

func TestCreateAndDestroyTestClones(t *testing.T) {
	disks := []ReplicatedDisk{
		{Dev: "vda", SourcePool: "tank/vms", SourceVolume: "web-disk0", TargetPool: "tank/dr", TargetVolume: "web-disk0"},
		{Dev: "vdb", SourcePool: "tank/vms", SourceVolume: "web-data0", TargetPool: "tank/dr", TargetVolume: "web-data0"},
	}
	r := &fakeRunner{handler: func(_ string, args []string) ([]byte, error) {
		if args[0] == "list" {
			dataset := args[len(args)-1]
			return []byte(dataset + "@virtomir-rep-20261019-020000\n"), nil
		}
		return nil, nil
	}}

	clones, err := CreateTestClones(context.Background(), r, "tank/dr", disks, "run1")
	if err != nil {
		t.Fatalf("create clones: %v", err)
	}
	if len(clones) != 2 {
		t.Fatalf("clones = %+v", clones)
	}
	if clones[0].SourceDataset != "tank/dr/web-disk0" || clones[0].Snapshot != "virtomir-rep-20261019-020000" ||
		clones[0].CloneDataset != "tank/dr/virtomir-test-run1-web-disk0" || clones[0].TargetDev != "vda" {
		t.Errorf("clone[0] = %+v", clones[0])
	}
	if _, ok := r.find("zfs clone tank/dr/web-disk0@virtomir-rep-20261019-020000 tank/dr/virtomir-test-run1-web-disk0"); !ok {
		t.Errorf("clone command missing: %v", r.calls)
	}
	if _, ok := r.find("zfs clone tank/dr/web-data0@virtomir-rep-20261019-020000 tank/dr/virtomir-test-run1-web-data0"); !ok {
		t.Errorf("second clone command missing: %v", r.calls)
	}

	r.calls = nil
	if err := DestroyTestClones(context.Background(), r, clones); err != nil {
		t.Fatalf("destroy clones: %v", err)
	}
	if _, ok := r.find("zfs destroy -r tank/dr/virtomir-test-run1-web-disk0"); !ok {
		t.Errorf("destroy command missing: %v", r.calls)
	}
	if _, ok := r.find("zfs destroy -r tank/dr/virtomir-test-run1-web-data0"); !ok {
		t.Errorf("second destroy command missing: %v", r.calls)
	}
}

// The replica may land under a different volume name than the source used;
// the clone has to read the target volume, the same one PromoteReplica uses.
func TestCreateTestClonesUsesTargetVolume(t *testing.T) {
	disks := []ReplicatedDisk{
		{Dev: "vda", SourcePool: "tank/vms", SourceVolume: "web-disk0", TargetPool: "tank/dr", TargetVolume: "web-rep-disk0"},
	}
	r := &fakeRunner{handler: func(_ string, args []string) ([]byte, error) {
		if args[0] == "list" {
			return []byte(args[len(args)-1] + "@virtomir-rep-20261019-020000\n"), nil
		}
		return nil, nil
	}}

	clones, err := CreateTestClones(context.Background(), r, "tank/dr", disks, "run1")
	if err != nil {
		t.Fatalf("create clones: %v", err)
	}
	if len(clones) != 1 || clones[0].SourceDataset != "tank/dr/web-rep-disk0" {
		t.Fatalf("clone source = %+v, want the target volume", clones)
	}
	if _, ok := r.find("zfs clone tank/dr/web-rep-disk0@virtomir-rep-20261019-020000"); !ok {
		t.Errorf("clone read the wrong replica dataset: %v", r.calls)
	}
}

func TestDestroyTestClonesRunsDespiteCancelledContext(t *testing.T) {
	clones := []CloneInfo{{CloneDataset: "tank/dr/virtomir-test-run1-web-disk0"}}
	r := &fakeRunner{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := DestroyTestClones(ctx, r, clones); err != nil {
		t.Fatalf("destroy clones: %v", err)
	}
	if _, ok := r.find("zfs destroy -r tank/dr/virtomir-test-run1-web-disk0"); !ok {
		t.Fatalf("destroy command missing: %v", r.calls)
	}
	for _, c := range r.ctxs {
		if c.Err() != nil {
			t.Fatalf("cleanup ran with a cancelled context; the clone would leak")
		}
	}
}

func TestCreateTestClonesRollsBackOnFailure(t *testing.T) {
	disks := []ReplicatedDisk{
		{Dev: "vda", SourceVolume: "d0", TargetPool: "tank/dr", TargetVolume: "d0"},
		{Dev: "vdb", SourceVolume: "d1", TargetPool: "tank/dr", TargetVolume: "d1"},
		{Dev: "vdc", SourceVolume: "d2", TargetPool: "tank/dr", TargetVolume: "d2"},
	}
	clones := 0
	r := &fakeRunner{handler: func(_ string, args []string) ([]byte, error) {
		switch args[0] {
		case "list":
			return []byte(args[len(args)-1] + "@virtomir-rep-20261019-020000\n"), nil
		case "clone":
			clones++
			if clones == 3 {
				return nil, errors.New("pool is full")
			}
		}
		return nil, nil
	}}

	if _, err := CreateTestClones(context.Background(), r, "tank/dr", disks, "run2"); err == nil {
		t.Fatal("expected the third clone to fail")
	}

	var destroys []string
	for _, c := range r.calls {
		if c.name == "zfs" && len(c.args) > 0 && c.args[0] == "destroy" {
			destroys = append(destroys, c.args[len(c.args)-1])
		}
	}
	want := []string{"tank/dr/virtomir-test-run2-d1", "tank/dr/virtomir-test-run2-d0"}
	if len(destroys) != len(want) {
		t.Fatalf("rollback destroyed %v, want %v", destroys, want)
	}
	for i := range want {
		if destroys[i] != want[i] {
			t.Errorf("rollback order = %v, want %v (newest first)", destroys, want)
		}
	}
}

func TestPromoteReplica(t *testing.T) {
	disks := []ReplicatedDisk{
		{Dev: "vda", TargetPool: "tank/dr", TargetVolume: "web-disk0"},
		{Dev: "vdb", TargetPool: "tank/dr", TargetVolume: "web-data0"},
	}
	r := &fakeRunner{handler: func(_ string, args []string) ([]byte, error) {
		switch {
		case args[0] == "get" && strings.Contains(args[len(args)-1], "web-disk0"):
			return []byte("on\n"), nil
		case args[0] == "get":
			return []byte("off\n"), nil
		}
		return nil, nil
	}}

	if err := PromoteReplica(context.Background(), r, disks); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if _, ok := r.find("zfs get -H -o value readonly tank/dr/web-disk0"); !ok {
		t.Errorf("readonly query missing: %v", r.calls)
	}
	if _, ok := r.find("zfs set readonly=off tank/dr/web-disk0"); !ok {
		t.Errorf("promote command missing: %v", r.calls)
	}
	if _, ok := r.find("zfs set readonly=off tank/dr/web-data0"); ok {
		t.Errorf("a writable dataset must not be promoted: %v", r.calls)
	}
}

const drFixtureXML = `<domain type='kvm'>
  <name>web1</name>
  <uuid>7c1f3e2a-55b4-4a8e-9d0c-1b2a3c4d5e6f</uuid>
  <memory unit='KiB'>1048576</memory>
  <os firmware='efi'>
    <type arch='x86_64' machine='pc-q35-10.0'>hvm</type>
    <nvram template='/usr/share/OVMF/OVMF_VARS.fd'>/var/lib/libvirt/qemu/nvram/web1_VARS.fd</nvram>
  </os>
  <devices>
    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2'/>
      <source file='/var/lib/libvirt/images/web1.qcow2'/>
      <target dev='vda' bus='virtio'/>
    </disk>
    <disk type='file' device='cdrom'>
      <driver name='qemu' type='raw'/>
      <source file='/var/lib/libvirt/images/install.iso'/>
      <target dev='sda' bus='sata'/>
      <readonly/>
    </disk>
    <interface type='network'>
      <mac address='52:54:00:aa:bb:cc'/>
      <source network='default'/>
      <model type='virtio'/>
    </interface>
    <tpm model='tpm-crb'>
      <backend type='emulator' version='2.0' persistent_state='yes'/>
    </tpm>
  </devices>
</domain>`

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestBuildDRDomainXML(t *testing.T) {
	seed := "/var/lib/libvirt/images/web1-dr-cidata.iso"
	out, err := BuildDRDomainXML(drFixtureXML, "dr-web1",
		map[string]string{"vda": "/dev/zvol/tank/dr/virtomir-test-run1-web-disk0"},
		map[string]string{"default": "dr-isolated"}, seed)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	var x libvirtxml.Domain
	if err := x.Unmarshal(out); err != nil {
		t.Fatalf("result is not valid XML: %v", err)
	}

	if x.Name != "dr-web1" {
		t.Errorf("name = %q", x.Name)
	}
	if x.UUID == "7c1f3e2a-55b4-4a8e-9d0c-1b2a3c4d5e6f" || !uuidRE.MatchString(x.UUID) {
		t.Errorf("uuid not regenerated: %q", x.UUID)
	}

	disk := diskByTarget(t, &x, "vda")
	if disk.Source == nil || disk.Source.Block == nil ||
		disk.Source.Block.Dev != "/dev/zvol/tank/dr/virtomir-test-run1-web-disk0" || disk.Source.File != nil {
		t.Errorf("disk vda not remapped: %+v", disk)
	}

	cd := diskByTarget(t, &x, "sda")
	if !ejected(cd) {
		t.Errorf("installation media not ejected: %+v", cd.Source)
	}
	if !hasSeed(&x, seed) {
		t.Errorf("cloud-init seed %q not attached", seed)
	}

	if x.Devices.Interfaces[0].Source.Network.Network != "dr-isolated" {
		t.Errorf("network not remapped: %+v", x.Devices.Interfaces[0].Source.Network)
	}
	if x.OS == nil || x.OS.NVRam == nil || x.OS.NVRam.NVRam != "" || x.OS.NVRam.Template == "" {
		t.Errorf("nvram not reset: %+v", x.OS)
	}
	if len(x.Devices.TPMs) != 1 || x.Devices.TPMs[0].Backend.Emulator.PersistentState != "" {
		t.Errorf("tpm state not cleared: %+v", x.Devices.TPMs)
	}
}

func TestBuildDRDomainXMLDoesNotDuplicateSeed(t *testing.T) {
	seed := "/var/lib/libvirt/images/web1-dr-cidata.iso"
	doc := strings.Replace(drFixtureXML, "/var/lib/libvirt/images/install.iso", seed, 1)
	out, err := BuildDRDomainXML(doc, "dr-web1", nil, nil, seed)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	var x libvirtxml.Domain
	if err := x.Unmarshal(out); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, d := range x.Devices.Disks {
		if d.Source != nil && d.Source.File != nil && d.Source.File.File == seed {
			n++
		}
	}
	if n != 1 {
		t.Errorf("seed appears %d times, want 1", n)
	}
}

func diskByTarget(t *testing.T, x *libvirtxml.Domain, dev string) *libvirtxml.DomainDisk {
	t.Helper()
	for i := range x.Devices.Disks {
		if x.Devices.Disks[i].Target != nil && x.Devices.Disks[i].Target.Dev == dev {
			return &x.Devices.Disks[i]
		}
	}
	t.Fatalf("no disk with target %q", dev)
	return nil
}

// ejected reports whether a disk has no media: libvirtxml always allocates a
// Source on unmarshal, so an empty source counts as ejected too.
func ejected(d *libvirtxml.DomainDisk) bool {
	return d.Source == nil || (d.Source.File == nil || d.Source.File.File == "") &&
		d.Source.Block == nil && d.Source.Volume == nil && d.Source.Network == nil
}

func hasSeed(x *libvirtxml.Domain, path string) bool {
	for _, d := range x.Devices.Disks {
		if d.Device == "cdrom" && d.Source != nil && d.Source.File != nil && d.Source.File.File == path {
			return true
		}
	}
	return false
}

func TestGenerateCloudInitNetworkISO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web1-dr-cidata.iso")
	ip := IPConfig{
		InterfaceMAC: "52:54:00:aa:bb:cc",
		IPv4Address:  "10.0.0.5/24",
		IPv4Gateway:  "10.0.0.1",
		Nameservers:  []string{"10.0.0.53", "1.1.1.1"},
	}
	if err := GenerateCloudInitNetworkISO("web1-dr", ip, path); err != nil {
		t.Fatalf("generate: %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := iso9660.OpenImage(f)
	if err != nil {
		t.Fatal(err)
	}
	if label, _ := img.Label(); !strings.EqualFold(label, "cidata") {
		t.Errorf("volume label = %q, cloud-init needs cidata", label)
	}
	root, err := img.RootDir()
	if err != nil {
		t.Fatal(err)
	}
	kids, err := root.GetChildren()
	if err != nil {
		t.Fatal(err)
	}
	contents := map[string]string{}
	for _, k := range kids {
		data, err := io.ReadAll(k.Reader())
		if err != nil {
			t.Fatal(err)
		}
		contents[strings.TrimSuffix(k.Name(), ";1")] = string(data)
	}

	userData := contents["user-data"]
	if !strings.Contains(userData, "#cloud-config") ||
		!strings.Contains(userData, "manage_etc_hosts: true") ||
		!strings.Contains(userData, "package_update: false") {
		t.Errorf("user-data = %q", userData)
	}
	metaData := contents["meta-data"]
	if !strings.Contains(metaData, "instance-id: virtomir-dr-") || !strings.Contains(metaData, "local-hostname: web1-dr") {
		t.Errorf("meta-data = %q", metaData)
	}
	network := contents["network-config"]
	for _, want := range []string{"version: 2", "macaddress:", "52:54:00:aa:bb:cc", "10.0.0.5/24", "gateway4:", "10.0.0.1", "1.1.1.1"} {
		if !strings.Contains(network, want) {
			t.Errorf("network-config missing %q:\n%s", want, network)
		}
	}
}
