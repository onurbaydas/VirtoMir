package virt

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"
)

func fixture(t *testing.T) *libvirtxml.Domain {
	t.Helper()
	d := loadDetail(t, "testdata/debian-uefi.xml") // validates the file parses
	_ = d
	var x libvirtxml.Domain
	raw := mustRead(t, "testdata/debian-uefi.xml")
	if err := x.Unmarshal(raw); err != nil {
		t.Fatal(err)
	}
	return &x
}

func isInvalid(err error) bool {
	var ie *InvalidError
	return errors.As(err, &ie)
}

func TestPrepareClone(t *testing.T) {
	x := fixture(t)
	prepareClone(x, "debian-copy")
	if x.Name != "debian-copy" || x.UUID != "" || x.ID != nil || x.Title != "debian-copy" {
		t.Errorf("identity not reset: name=%q uuid=%q title=%q", x.Name, x.UUID, x.Title)
	}
	if x.OS.NVRam.NVRam != "" || x.OS.NVRam.Template == "" {
		t.Errorf("nvram must keep the template only: %+v", x.OS.NVRam)
	}
	for _, n := range x.Devices.Interfaces {
		if n.MAC != nil || n.Target != nil {
			t.Errorf("interface keeps MAC/target: %+v", n)
		}
	}
	if g := x.Devices.Graphics[0].VNC; g.Port != -1 || g.AutoPort != "yes" {
		t.Errorf("vnc port must be automatic: %+v", g)
	}
	if _, err := x.Marshal(); err != nil {
		t.Fatal(err)
	}
}

func TestDeletableDisks(t *testing.T) {
	x := fixture(t)
	// The read-only CD-ROM must never be deleted with the machine.
	if got := deletableDisks(x); !slices.Equal(got, []string{"/var/lib/libvirt/images/debian-uefi.qcow2"}) {
		t.Errorf("deletable = %q", got)
	}
	// The cloud-init seed VirtoMir made for the machine goes with it.
	setSeed(x, "/var/lib/libvirt/images/web1-cidata.iso")
	want := []string{"/var/lib/libvirt/images/debian-uefi.qcow2", "/var/lib/libvirt/images/web1-cidata.iso"}
	if got := deletableDisks(x); !slices.Equal(got, want) {
		t.Errorf("deletable with seed = %q", got)
	}
}

func TestDiskNames(t *testing.T) {
	for i, want := range map[int]string{0: "a", 1: "b", 25: "z", 26: "aa", 27: "ab", 701: "zz", 702: "aaa"} {
		if got := diskLetters(i); got != want {
			t.Errorf("diskLetters(%d) = %q, want %q", i, got, want)
		}
	}
	x := fixture(t) // has vda and sda
	if got := nextTarget(x, "vd"); got != "vdb" {
		t.Errorf("next virtio = %q", got)
	}
	if got := nextTarget(x, "sd"); got != "sdb" {
		t.Errorf("next sata = %q", got)
	}
	if got := newVolName("web", "vdb", "qcow2"); got != "web-vdb.qcow2" {
		t.Errorf("vol name = %q", got)
	}
}

func TestBuildNIC(t *testing.T) {
	nic, err := buildNIC(NICSpec{Network: "default"})
	if err != nil || nic.Source.Network.Network != "default" || nic.Model.Type != "virtio" {
		t.Errorf("nic = %+v err = %v", nic, err)
	}
	if _, err := buildNIC(NICSpec{}); !isInvalid(err) {
		t.Errorf("missing source must be invalid, got %v", err)
	}
	if _, err := buildNIC(NICSpec{Network: "a", Bridge: "b"}); !isInvalid(err) {
		t.Errorf("two sources must be invalid, got %v", err)
	}
}

func TestBuildNetwork(t *testing.T) {
	x, err := buildNetwork(NetworkSpec{Name: "lab", Mode: "nat", Subnet: "192.168.100.77/24", DHCP: true})
	if err != nil {
		t.Fatal(err)
	}
	ip := x.IPs[0]
	if x.Forward.Mode != "nat" || ip.Address != "192.168.100.1" || ip.Prefix != 24 {
		t.Errorf("network = %+v ip = %+v", x.Forward, ip)
	}
	if r := ip.DHCP.Ranges[0]; r.Start != "192.168.100.2" || r.End != "192.168.100.254" {
		t.Errorf("dhcp = %+v", r)
	}
	iso, _ := buildNetwork(NetworkSpec{Name: "iso", Mode: "isolated", Subnet: "10.9.0.0/16"})
	if iso.Forward != nil || iso.IPs[0].DHCP != nil {
		t.Errorf("isolated without dhcp = %+v", iso)
	}
	customRange, err := buildNetwork(NetworkSpec{
		Name:       "custom-dhcp",
		Mode:       "nat",
		Subnet:     "192.168.50.0/24",
		DHCP:       true,
		DHCPStart:  "192.168.50.100",
		DHCPEnd:    "192.168.50.200",
		Subnet6:    "fd12:3456:789a::/64",
		DHCP6:      true,
		DHCP6Start: "fd12:3456:789a::50",
		DHCP6End:   "fd12:3456:789a::150",
	})
	if err != nil {
		t.Fatalf("custom dhcp range: %v", err)
	}
	if r := customRange.IPs[0].DHCP.Ranges[0]; r.Start != "192.168.50.100" || r.End != "192.168.50.200" {
		t.Errorf("custom ipv4 dhcp = %+v", r)
	}
	if r6 := customRange.IPs[1].DHCP.Ranges[0]; r6.Start != "fd12:3456:789a::50" || r6.End != "fd12:3456:789a::150" {
		t.Errorf("custom ipv6 dhcp = %+v", r6)
	}
	br, _ := buildNetwork(NetworkSpec{Name: "lan", Mode: "bridge", Bridge: "br0"})
	if br.Forward.Mode != "bridge" || br.Bridge.Name != "br0" || br.IPs != nil {
		t.Errorf("bridge = %+v", br)
	}

	for _, bad := range []NetworkSpec{
		{Name: "x", Mode: "nat", Subnet: "nonsense"},
		{Name: "x", Mode: "nat", Subnet: "10.0.0.0/8"},
		{Name: "x", Mode: "bridge"},
		{Name: "bad name", Mode: "isolated", Subnet: "10.1.0.0/24"},
		{Name: "x", Mode: "wormhole"},
	} {
		if _, err := buildNetwork(bad); !isInvalid(err) {
			t.Errorf("%+v: want invalid, got %v", bad, err)
		}
	}
}

func TestNetworkHelpers(t *testing.T) {
	if got := lastUsable(netip.MustParsePrefix("10.0.0.0/30")); got.String() != "10.0.0.2" {
		t.Errorf("lastUsable /30 = %s", got)
	}
	if maskBits("255.255.255.0") != 24 || maskBits("255.255.0.0") != 16 || maskBits("x") != 0 {
		t.Error("maskBits")
	}
	v, err := parseNetwork(`<network><name>default</name><forward mode='nat'/><bridge name='virbr0'/>
		<ip address='192.168.122.1' netmask='255.255.255.0'><dhcp><range start='192.168.122.2' end='192.168.122.254'/></dhcp></ip></network>`)
	if err != nil || v.Mode != "nat" || v.Bridge != "virbr0" || v.Subnet != "192.168.122.1/24" || v.DHCPEnd != "192.168.122.254" {
		t.Errorf("parsed = %+v err = %v", v, err)
	}
	iso, _ := parseNetwork(`<network><name>i</name><ip address='10.0.0.1' prefix='24'/></network>`)
	if iso.Mode != "isolated" || iso.Subnet != "10.0.0.1/24" {
		t.Errorf("isolated = %+v", iso)
	}
}

func TestParseSnapshot(t *testing.T) {
	s, err := parseSnapshot(`<domainsnapshot><name>before-upgrade</name><description>kernel 6.12</description>
		<state>running</state><parent><name>clean</name></parent><creationTime>1760000000</creationTime>
		<memory snapshot='internal'/><disks><disk name='vda' snapshot='internal'/></disks></domainsnapshot>`)
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "before-upgrade" || s.Parent != "clean" || s.Created != 1760000000 || !s.Memory || s.Location != "internal" || s.State != "running" {
		t.Errorf("snapshot = %+v", s)
	}
	ext, _ := parseSnapshot(`<domainsnapshot><name>e</name><state>shutoff</state><memory snapshot='no'/><disks><disk name='vda' snapshot='external'/></disks></domainsnapshot>`)
	if ext.Memory || ext.Location != "external" {
		t.Errorf("external = %+v", ext)
	}
}

func TestSnapshotDoc(t *testing.T) {
	parse := func(raw string) *libvirtxml.Domain {
		var x libvirtxml.Domain
		if err := x.Unmarshal(raw); err != nil {
			t.Fatal(err)
		}
		return &x
	}
	qcow := parse(`<domain type='kvm'><name>web</name><devices>
		<disk type='file' device='disk'><driver name='qemu' type='qcow2'/><source file='/p/web.qcow2'/><target dev='vda'/></disk>
		<disk type='file' device='cdrom'><driver name='qemu' type='raw'/><source file='/p/x.iso'/><target dev='sda'/><readonly/></disk>
		</devices></domain>`)
	if s := snapshotDoc(qcow, "s1", "d", false); s.Disks != nil || s.Memory != nil || s.Name != "s1" || s.Description != "d" {
		t.Errorf("all-qcow2 machine should keep libvirt's internal default, got %+v", s)
	}
	if s := snapshotDoc(qcow, "s2", "", true); s.Disks == nil || s.Disks.Disks[0].Snapshot != "external" {
		t.Errorf("after an external snapshot the next one must be external too, got %+v", s.Disks)
	}

	mixed := parse(`<domain type='kvm'><name>db</name><devices>
		<disk type='file' device='disk'><driver name='qemu' type='raw'/><source file='/p/db.img'/><target dev='vda'/></disk>
		<disk type='block' device='disk'><driver name='qemu' type='raw'/><source dev='/dev/vg/db-data'/><target dev='vdb'/></disk>
		<disk type='file' device='cdrom'><driver name='qemu' type='raw'/><source file='/p/x.iso'/><target dev='sda'/><readonly/></disk>
		</devices></domain>`)
	s := snapshotDoc(mixed, "s1", "", false)
	if s.Memory == nil || s.Memory.Snapshot != "external" || s.Memory.File != "/p/db.s1.mem" {
		t.Errorf("memory = %+v", s.Memory)
	}
	if s.Disks == nil || len(s.Disks.Disks) != 3 {
		t.Fatalf("disks = %+v", s.Disks)
	}
	vda, vdb, sda := s.Disks.Disks[0], s.Disks.Disks[1], s.Disks.Disks[2]
	if vda.Name != "vda" || vda.Snapshot != "external" || vda.Source != nil {
		t.Errorf("file disk should use libvirt's own overlay name: %+v", vda)
	}
	if vdb.Snapshot != "external" || vdb.Source == nil || vdb.Source.File == nil || vdb.Source.File.File != "/p/db-vdb.s1.qcow2" ||
		vdb.Driver == nil || vdb.Driver.Type != "qcow2" {
		t.Errorf("block disk needs an explicit qcow2 overlay file: %+v", vdb)
	}
	if sda.Snapshot != "no" {
		t.Errorf("read-only CD-ROM must not be snapshotted: %+v", sda)
	}

	block := parse(`<domain type='kvm'><name>b</name><devices>
		<disk type='block' device='disk'><driver name='qemu' type='raw'/><source dev='/dev/zvol/tank/b'/><target dev='vda'/></disk>
		</devices></domain>`)
	if s := snapshotDoc(block, "s", "", false); s.Memory.File != "/var/lib/libvirt/images/b.s.mem" ||
		s.Disks.Disks[0].Source.File.File != "/var/lib/libvirt/images/b-vda.s.qcow2" {
		t.Errorf("no file disk: overlays go to the default image directory, got %+v / %+v", s.Memory, s.Disks.Disks[0].Source.File)
	}
}

func TestGuestInfo(t *testing.T) {
	if !agentConnected(fixture(t)) {
		t.Error("fixture has a connected agent channel")
	}
	if agentConnected(&libvirtxml.Domain{}) {
		t.Error("no devices: no agent")
	}
	s := func(f, v string) libvirt.TypedParam {
		return libvirt.TypedParam{Field: f, Value: *libvirt.NewTypedParamValueString(v)}
	}
	u := func(f string, v uint64) libvirt.TypedParam {
		return libvirt.TypedParam{Field: f, Value: *libvirt.NewTypedParamValueUllong(v)}
	}
	g := parseGuestInfo([]libvirt.TypedParam{
		s("os.name", "Debian GNU/Linux"), s("os.version", "13 (trixie)"), s("os.kernel-release", "6.12.48-amd64"),
		s("hostname", "web1"), s("timezone.name", "UTC"),
		{Field: "user.count", Value: *libvirt.NewTypedParamValueUint(1)}, s("user.0.name", "debian"),
		{Field: "fs.count", Value: *libvirt.NewTypedParamValueUint(2)},
		s("fs.0.mountpoint", "/"), s("fs.0.fstype", "ext4"), u("fs.0.total-bytes", 20<<30), u("fs.0.used-bytes", 5<<30),
		s("fs.1.mountpoint", "/sys/fs/cgroup"), s("fs.1.fstype", "cgroup2"),
	})
	if !g.Connected || g.OS != "Debian GNU/Linux 13 (trixie)" || g.Hostname != "web1" || g.Kernel != "6.12.48-amd64" || g.Timezone != "UTC" {
		t.Errorf("info = %+v", g)
	}
	if len(g.Users) != 1 || g.Users[0] != "debian" {
		t.Errorf("users = %v", g.Users)
	}
	if len(g.Filesystems) != 1 || g.Filesystems[0] != (GuestFilesystem{"/", "ext4", 20 << 30, 5 << 30}) {
		t.Errorf("file systems = %+v (sizeless pseudo file systems are left out)", g.Filesystems)
	}
}

func TestInterfaceBandwidth(t *testing.T) {
	in, out := mbpsToKiBps(100), mbpsToKiBps(1000)
	dev := interfaceDevice(libvirtxml.DomainInterface{
		MAC: &libvirtxml.DomainInterfaceMAC{Address: "52:54:00:AA:BB:CC"},
		Bandwidth: &libvirtxml.DomainInterfaceBandwidth{
			Inbound:  &libvirtxml.DomainInterfaceBandwidthParams{Average: &in},
			Outbound: &libvirtxml.DomainInterfaceBandwidthParams{Average: &out},
		},
	})
	if dev.Bandwidth == nil || dev.Bandwidth.InMbps != 100 || dev.Bandwidth.OutMbps != 1000 {
		t.Fatalf("bandwidth = %+v (in %d, out %d KiB/s)", dev.Bandwidth, in, out)
	}
	if !slices.ContainsFunc(dev.Props, func(p Prop) bool { return p.Value == "100 Mbit/s" }) {
		t.Errorf("props = %+v", dev.Props)
	}
	if none := interfaceDevice(libvirtxml.DomainInterface{}); none.Bandwidth != nil {
		t.Errorf("no limits = %+v", none.Bandwidth)
	}
}

func TestSetDiskImage(t *testing.T) {
	block := libvirtxml.DomainDisk{
		Driver:       &libvirtxml.DomainDiskDriver{Name: "qemu", Type: "raw", Cache: "none"},
		Source:       &libvirtxml.DomainDiskSource{Block: &libvirtxml.DomainDiskSourceBlock{Dev: "/dev/vg/web"}},
		BackingStore: &libvirtxml.DomainDiskBackingStore{},
	}
	if !setDiskImage(&block, "/nfs/web-vda.qcow2") {
		t.Fatal("block disk should change")
	}
	if src, _ := diskSource(block.Source); src != "/nfs/web-vda.qcow2" || block.Source.Block != nil ||
		block.Driver.Type != "qcow2" || block.Driver.Cache != "none" || block.BackingStore != nil {
		t.Errorf("moved disk = %+v / %+v", block.Source, block.Driver)
	}
	if setDiskImage(&block, "/nfs/web-vda.qcow2") {
		t.Error("already pointing at the file: nothing to change")
	}
}

func TestCreateSpecValidation(t *testing.T) {
	ok := CreateSpec{Name: "web", VCPUs: 2, MemoryMiB: 2048, DiskGiB: 20}
	if err := ok.normalize(); err != nil {
		t.Fatal(err)
	}
	if ok.OS != "linux" || ok.Firmware != "uefi" || ok.Pool != "default" {
		t.Errorf("defaults = %+v", ok)
	}
	for _, bad := range []CreateSpec{
		{Name: "", VCPUs: 1, MemoryMiB: 512, DiskGiB: 1},
		{Name: "a", VCPUs: 0, MemoryMiB: 512, DiskGiB: 1},
		{Name: "a", VCPUs: 1, MemoryMiB: 8, DiskGiB: 1},
		{Name: "a", VCPUs: 1, MemoryMiB: 512},
		{Name: "a", VCPUs: 1, MemoryMiB: 512, DiskGiB: 1, Firmware: "bios", SecureBoot: true},
		{Name: "a", VCPUs: 1, MemoryMiB: 512, DiskGiB: 1, OS: "beos"},
	} {
		if err := bad.normalize(); !isInvalid(err) {
			t.Errorf("%+v: want invalid, got %v", bad, err)
		}
	}
}

func TestBuildDomainLinux(t *testing.T) {
	s := CreateSpec{Name: "web", VCPUs: 4, MemoryMiB: 4096, DiskGiB: 20, ISO: "/isos/debian.iso", Network: &NICSpec{Network: "default"}}
	if err := s.normalize(); err != nil {
		t.Fatal(err)
	}
	doc, err := buildDomain(s, "/pool/web.qcow2", "qcow2", "").Marshal()
	if err != nil {
		t.Fatal(err)
	}
	// Round trip through the parser the machine page uses.
	var x libvirtxml.Domain
	if err := x.Unmarshal(doc); err != nil {
		t.Fatal(err)
	}
	d := buildDetail(Domain{}, &x)
	if d.System.Firmware != "uefi" || d.System.SecureBoot || d.CPU.MaxVCPUs != 4 || d.Memory.MaxKiB != 4<<20 {
		t.Errorf("detail = %+v %+v %+v", d.System, d.CPU, d.Memory)
	}
	if want := []string{"CD/DVD drive sda", "Disk vda"}; !slices.Equal(d.System.BootOrder, want) {
		t.Errorf("boot order = %q", d.System.BootOrder)
	}
	for _, must := range []string{"machine=\"q35\"", "type=\"qcow2\"", "bus=\"virtio\"", "org.qemu.guest_agent.0", "pcie-root-port", "listen=\"127.0.0.1\""} {
		if !strings.Contains(doc, must) {
			t.Errorf("XML lacks %s", must)
		}
	}
	if strings.Contains(doc, "<tpm") || strings.Contains(doc, "<smm") {
		t.Error("linux default must not add TPM/SMM")
	}
}

func TestBuildDomainOnABlockDevice(t *testing.T) {
	s := CreateSpec{Name: "db", VCPUs: 1, MemoryMiB: 1024, DiskPath: "/dev/disk/by-path/ip-10.0.0.5:3260-iscsi-iqn.2026-01.lab:vms-lun-0"}
	if err := s.normalize(); err != nil {
		t.Fatal(err)
	}
	var x libvirtxml.Domain
	doc, _ := buildDomain(s, s.DiskPath, "", "").Marshal()
	if err := x.Unmarshal(doc); err != nil {
		t.Fatal(err)
	}
	d := x.Devices.Disks[0]
	if d.Source == nil || d.Source.Block == nil || d.Source.Block.Dev != s.DiskPath || d.Source.File != nil || d.Driver.Type != "raw" {
		t.Errorf("an iSCSI LUN must be a block disk, not a file: %s", doc)
	}
}

func TestBuildDomainWindows(t *testing.T) {
	s := CreateSpec{Name: "win11", OS: "windows", VCPUs: 4, MemoryMiB: 8192, DiskGiB: 64, SecureBoot: true, TPM: true, Network: &NICSpec{Network: "default"}}
	if err := s.normalize(); err != nil {
		t.Fatal(err)
	}
	doc, _ := buildDomain(s, "/pool/win11.qcow2", "qcow2", "").Marshal()
	for _, must := range []string{"bus=\"sata\"", "e1000e", "<smm state=\"on\"", "secure-boot", "tpm-crb", "localtime", "<hyperv"} {
		if !strings.Contains(doc, must) {
			t.Errorf("windows XML lacks %s", must)
		}
	}

	s.ISO, s.DriverISO = "/iso/win11.iso", "/iso/virtio-win.iso"
	x := buildDomain(s, "/pool/win11.qcow2", "qcow2", "")
	var cds []string
	for _, d := range x.Devices.Disks {
		if d.Device == "cdrom" {
			cds = append(cds, d.Target.Dev+"="+d.Source.File.File)
			if d.Boot != nil && d.Source.File.File == s.DriverISO {
				t.Error("the drivers CD must not be a boot device")
			}
		}
	}
	if !slices.Equal(cds, []string{"sda=/iso/win11.iso", "sdc=/iso/virtio-win.iso"}) {
		t.Errorf("CD-ROMs = %v", cds)
	}
}

// Regression: libvirt reads a missing controller index as 0, so every PCI controller must
// carry its own, with pcie-root at 0.
func TestBuildDomainControllerIndexes(t *testing.T) {
	s := CreateSpec{Name: "web", VCPUs: 1, MemoryMiB: 512, DiskGiB: 1}
	if err := s.normalize(); err != nil {
		t.Fatal(err)
	}
	x := buildDomain(s, "/pool/web.qcow2", "qcow2", "")
	seen := map[string]bool{}
	for _, c := range x.Devices.Controllers {
		if c.Index == nil {
			t.Fatalf("%s controller %s without index", c.Type, c.Model)
		}
		key := fmt.Sprintf("%s/%d", c.Type, *c.Index)
		if seen[key] {
			t.Errorf("duplicate controller %s", key)
		}
		seen[key] = true
		if c.Type == "pci" && *c.Index == 0 && c.Model != "pcie-root" {
			t.Errorf("pci index 0 is %s, want pcie-root", c.Model)
		}
	}
	if !seen["pci/0"] || !seen["pci/8"] {
		t.Errorf("controllers: %v", seen)
	}
}

func TestRootfsSum(t *testing.T) {
	sums, err := os.ReadFile("../signature/testdata/lxc-SHA256SUMS")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := rootfsSum(sums); err != nil || got != "c261d23220dcdc5e402f319a127524a8fafdad251b20cb7218b0513e1b5e5b98" {
		t.Errorf("rootfsSum = %q, %v", got, err)
	}
	if _, err := rootfsSum([]byte("abc  disk.qcow2\n")); err == nil {
		t.Error("a list without rootfs.tar.xz was accepted")
	}
}

func TestPoolFills(t *testing.T) {
	for typ, fills := range map[string]bool{"dir": true, "netfs": true, "logical": true, "zfs": true, "rbd": true, "iscsi": false, "scsi": false, "mpath": false} {
		if (Pool{Type: typ}).Fills() != fills {
			t.Errorf("%s pool fills = %v", typ, !fills)
		}
	}
}
