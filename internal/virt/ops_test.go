package virt

import (
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"

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
}
