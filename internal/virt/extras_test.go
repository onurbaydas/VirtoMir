package virt

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/kdomanski/iso9660"
	"libvirt.org/go/libvirtxml"
)

func TestSettingsRoundTrip(t *testing.T) {
	x := fixture(t)
	v := settingsFromXML(x)
	if v.VCPUs != 2 || v.MaxVCPUs != 4 || v.Sockets != 1 || v.Cores != 2 || v.Threads != 2 {
		t.Errorf("cpu = %+v", v.Settings)
	}
	if v.MemoryMiB != 4096 || v.CurrentMemoryMiB != 2048 || v.Firmware != "uefi" || !v.SecureBoot || !v.BootMenu {
		t.Errorf("memory/firmware = %+v", v.Settings)
	}
	if want := []string{"disk:sda", "disk:vda"}; !slices.Equal(v.BootOrder, want) {
		t.Errorf("boot order = %v", v.BootOrder)
	}
	if len(v.BootDevices) != 3 { // vda, sda, NIC
		t.Errorf("boot devices = %+v", v.BootDevices)
	}
	// Unchanged settings must not change the XML meaningfully.
	if err := applySettings(x, v.Settings); err != nil {
		t.Fatal(err)
	}
	again := settingsFromXML(x)
	if !slices.Equal(again.BootOrder, v.BootOrder) || again.MemoryMiB != v.MemoryMiB || x.OS.Loader == nil {
		t.Errorf("idempotent apply changed things: %+v loader=%v", again.Settings, x.OS.Loader)
	}
}

func TestApplySettings(t *testing.T) {
	x := fixture(t)
	s := settingsFromXML(x).Settings
	s.MaxVCPUs, s.VCPUs, s.Sockets, s.Cores, s.Threads = 8, 6, 2, 2, 2
	s.MemoryMiB, s.CurrentMemoryMiB = 8192, 8192
	s.CPUMode, s.CPUModel = "custom", "Skylake-Client"
	s.BootOrder = []string{"disk:vda", "net:52:54:00:ab:cd:ef"}
	s.BootMenu = false
	if err := applySettings(x, s); err != nil {
		t.Fatal(err)
	}
	if x.VCPU.Value != 8 || x.VCPU.Current != 6 || x.CPU.Topology.Sockets != 2 || x.CPU.Model.Value != "Skylake-Client" {
		t.Errorf("cpu xml = %+v %+v", x.VCPU, x.CPU)
	}
	if toKiB(x.Memory.Value, x.Memory.Unit) != 8<<20 || toKiB(x.CurrentMemory.Value, x.CurrentMemory.Unit) != 8<<20 {
		t.Errorf("memory xml = %+v %+v", x.Memory, x.CurrentMemory)
	}
	if x.OS.BootMenu != nil || len(x.OS.BootDevices) != 0 {
		t.Error("boot menu / legacy boot devices not cleared")
	}
	vda, cd := findDisk(x, "vda"), findDisk(x, "sda")
	if vda.Boot == nil || vda.Boot.Order != 1 || cd.Boot != nil || x.Devices.Interfaces[0].Boot.Order != 2 {
		t.Errorf("per-device boot order wrong")
	}
	// Firmware untouched: explicit loader stays.
	if x.OS.Loader == nil || x.OS.Loader.Path == "" {
		t.Error("loader must not be touched when firmware is unchanged")
	}
	if _, err := x.Marshal(); err != nil {
		t.Fatal(err)
	}
}

func TestApplyFirmwareChange(t *testing.T) {
	x := fixture(t)
	s := settingsFromXML(x).Settings
	s.Firmware, s.SecureBoot = "bios", false
	if err := applySettings(x, s); err != nil {
		t.Fatal(err)
	}
	if x.OS.Firmware != "" || x.OS.Loader != nil || x.OS.NVRam != nil || x.OS.FirmwareInfo != nil {
		t.Errorf("bios: os = %+v", x.OS)
	}
	s.Firmware, s.SecureBoot = "uefi", true
	if err := applySettings(x, s); err != nil {
		t.Fatal(err)
	}
	if x.OS.Firmware != "efi" || x.Features.SMM == nil || x.Features.SMM.State != "on" {
		t.Errorf("uefi+sb: os = %+v features = %+v", x.OS, x.Features)
	}
}

func TestSettingsValidation(t *testing.T) {
	base := settingsFromXML(fixture(t)).Settings
	cases := map[string]func(*Settings){
		"topology":     func(s *Settings) { s.Sockets = 3 },
		"active>max":   func(s *Settings) { s.VCPUs = 9 },
		"custom model": func(s *Settings) { s.CPUMode = "custom"; s.CPUModel = "" },
		"mode":         func(s *Settings) { s.CPUMode = "turbo" },
		"current>max":  func(s *Settings) { s.CurrentMemoryMiB = s.MemoryMiB + 1 },
		"tiny memory":  func(s *Settings) { s.MemoryMiB = 32 },
		"bios+sb":      func(s *Settings) { s.Firmware = "bios"; s.SecureBoot = true },
		"boot dup":     func(s *Settings) { s.BootOrder = []string{"disk:vda", "disk:vda"} },
		"boot unknown": func(s *Settings) { s.BootOrder = []string{"disk:vdz"} },
	}
	for name, mutate := range cases {
		s := base
		s.BootOrder = slices.Clone(base.BootOrder)
		mutate(&s)
		if err := applySettings(fixture(t), s); !isInvalid(err) {
			t.Errorf("%s: want invalid, got %v", name, err)
		}
	}
}

func TestLegacyBootOrderMapped(t *testing.T) {
	var x libvirtxml.Domain
	_ = x.Unmarshal(`<domain type='kvm'><name>l</name><memory>1048576</memory><vcpu>1</vcpu>
		<os><type>hvm</type><boot dev='cdrom'/><boot dev='hd'/></os>
		<devices><disk type='file' device='disk'><target dev='vda' bus='virtio'/></disk>
		<disk type='file' device='cdrom'><target dev='sda' bus='sata'/></disk></devices></domain>`)
	if got := settingsFromXML(&x).BootOrder; !slices.Equal(got, []string{"disk:sda", "disk:vda"}) {
		t.Errorf("legacy boot order = %v", got)
	}
}

func TestCloudInitUserData(t *testing.T) {
	ci := CloudInit{User: "onur", Password: `pa"ss`, SSHKeys: []string{" ssh-ed25519 AAAA test@host ", ""}}
	if err := ci.normalize("web.server_1"); err != nil {
		t.Fatal(err)
	}
	if ci.Hostname != "web-server-1" || len(ci.SSHKeys) != 1 {
		t.Errorf("normalized = %+v", ci)
	}
	ud := ci.userData()
	for _, must := range []string{"#cloud-config", `name: "onur"`, `plain_text_passwd: "pa\"ss"`, `"ssh-ed25519 AAAA test@host"`, "ssh_pwauth: true", "qemu-guest-agent"} {
		if !strings.Contains(ud, must) {
			t.Errorf("user-data lacks %s:\n%s", must, ud)
		}
	}
	for _, bad := range []CloudInit{
		{User: "Onur", Password: "x"},
		{User: "onur"},
		{User: "onur", SSHKeys: []string{"not a key"}},
		{User: "onur", Password: "x", Packages: []string{"rm -rf"}},
		{UserData: "hello"},
	} {
		if err := bad.normalize("m"); !isInvalid(err) {
			t.Errorf("%+v: want invalid, got %v", bad, err)
		}
	}
}

func TestSeedISO(t *testing.T) {
	ci := CloudInit{User: "onur", Password: "pw"}
	if err := ci.normalize("vm1"); err != nil {
		t.Fatal(err)
	}
	data, err := ci.seedISO()
	if err != nil {
		t.Fatal(err)
	}
	img, err := iso9660.OpenImage(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	label, _ := img.Label()
	if !strings.EqualFold(label, "cidata") {
		t.Errorf("volume label = %q, cloud-init needs cidata", label)
	}
	root, _ := img.RootDir()
	kids, _ := root.GetChildren()
	var names []string
	for _, k := range kids {
		names = append(names, k.Name())
	}
	// ISO 9660 names carry a ";1" version; Linux strips it when mounting.
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, "user-data") || !strings.Contains(joined, "meta-data") {
		t.Errorf("files = %v", names)
	}
}

func TestBuildDomainWithSeed(t *testing.T) {
	s := CreateSpec{Name: "c", VCPUs: 1, MemoryMiB: 512, BaseImage: "/pool/cloud.qcow2", DiskGiB: 10}
	if err := s.normalize(); err != nil {
		t.Fatal(err)
	}
	x := buildDomain(s, "/pool/c.qcow2", "qcow2", "/pool/c-cidata.iso")
	seed := findDisk(x, "sda")
	if seed == nil || seed.Device != "cdrom" || seed.Boot != nil || seed.Source.File.File != "/pool/c-cidata.iso" {
		t.Errorf("seed drive = %+v", seed)
	}
	if d := findDisk(x, "vda"); d == nil || d.Boot == nil || d.Boot.Order != 1 {
		t.Error("system disk must boot first")
	}
	both := CreateSpec{Name: "c", VCPUs: 1, MemoryMiB: 512, BaseImage: "/a", DiskPath: "/b"}
	if err := both.normalize(); !isInvalid(err) {
		t.Errorf("base image + disk path: %v", err)
	}
}

func TestReservationChecks(t *testing.T) {
	var x libvirtxml.Network
	_ = x.Unmarshal(`<network><name>default</name><ip address='192.168.122.1' netmask='255.255.255.0'>
		<dhcp><range start='192.168.122.2' end='192.168.122.254'/><host mac='52:54:00:00:00:01' ip='192.168.122.10'/></dhcp></ip></network>`)
	if hosts := dhcpHostsFromXML(&x); len(hosts) != 1 || hosts[0].IP != "192.168.122.10" {
		t.Errorf("hosts = %+v", hosts)
	}
	if err := checkReservation(&x, DHCPHost{MAC: "52:54:00:00:00:02", IP: "192.168.122.20"}); err != nil {
		t.Errorf("valid reservation refused: %v", err)
	}
	bad := []DHCPHost{
		{MAC: "nope", IP: "192.168.122.20"},
		{MAC: "52:54:00:00:00:02", IP: "10.0.0.5"},        // outside subnet
		{MAC: "52:54:00:00:00:02", IP: "192.168.122.1"},   // gateway
		{MAC: "52:54:00:00:00:02", IP: "192.168.122.255"}, // broadcast
		{MAC: "52:54:00:00:00:01", IP: "192.168.122.30"},  // MAC already reserved
		{MAC: "52:54:00:00:00:03", IP: "192.168.122.10"},  // IP taken
	}
	for _, h := range bad {
		if err := checkReservation(&x, h); err == nil {
			t.Errorf("%+v must be refused", h)
		}
	}
}
