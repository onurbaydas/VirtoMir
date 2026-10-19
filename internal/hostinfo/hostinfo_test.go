package hostinfo

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// tree writes files (path → content) below a temp root.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for p, c := range files {
		if runtime.GOOS == "windows" && strings.Contains(p, ":") {
			t.Skip("PCI addresses contain ':', which Windows file names cannot")
		}
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks not available here: %v", err)
	}
}

func TestChassis(t *testing.T) {
	root := tree(t, map[string]string{
		"sys/class/dmi/id/sys_vendor":     "Dell Inc.",
		"sys/class/dmi/id/product_name":   "PowerEdge R750",
		"sys/class/dmi/id/product_serial": "ABC1234",
		"sys/class/dmi/id/board_name":     "Default string",
		"sys/class/dmi/id/chassis_type":   "23",
		"proc/sys/kernel/osrelease":       "6.12.0-amd64",
		"proc/uptime":                     "3600.5 7000.0",
		"etc/os-release":                  "NAME=Debian\nPRETTY_NAME=\"Debian GNU/Linux 13 (trixie)\"",

		"sys/class/hwmon/hwmon0/name":           "coretemp",
		"sys/class/hwmon/hwmon0/temp1_input":    "45000",
		"sys/class/hwmon/hwmon0/temp1_label":    "Package id 0",
		"sys/class/hwmon/hwmon0/temp1_crit":     "100000",
		"sys/class/hwmon/hwmon0/temp10_input":   "41000",
		"sys/class/hwmon/hwmon0/temp2_input":    "40000",
		"sys/class/hwmon/hwmon1/name":           "nct6775",
		"sys/class/hwmon/hwmon1/fan1_input":     "1200",
		"sys/class/hwmon/hwmon1/fan2_input":     "0", // nothing connected
		"sys/class/hwmon/hwmon1/power1_average": "152000000",
	})
	c := New()
	c.Root = root
	ch := c.Chassis()
	if ch.Vendor != "Dell Inc." || ch.Product != "PowerEdge R750" || ch.Serial != "ABC1234" {
		t.Errorf("DMI = %+v", ch)
	}
	if ch.BoardName != "" {
		t.Errorf("placeholder board name kept: %q", ch.BoardName)
	}
	if ch.ChassisType != "Rack Mount Chassis" || ch.Virtual {
		t.Errorf("type = %q virtual = %v", ch.ChassisType, ch.Virtual)
	}
	if ch.OS != "Debian GNU/Linux 13 (trixie)" || ch.Kernel != "6.12.0-amd64" || ch.UptimeSec != 3600.5 {
		t.Errorf("os = %q kernel = %q uptime = %v", ch.OS, ch.Kernel, ch.UptimeSec)
	}
	want := []Sensor{
		{Chip: "coretemp", Label: "Package id 0", Kind: "temperature", Unit: "°C", Value: 45, Crit: 100},
		{Chip: "coretemp", Label: "temp2", Kind: "temperature", Unit: "°C", Value: 40},
		{Chip: "coretemp", Label: "temp10", Kind: "temperature", Unit: "°C", Value: 41},
		{Chip: "nct6775", Label: "fan1", Kind: "fan", Unit: "RPM", Value: 1200},
		{Chip: "nct6775", Label: "power1", Kind: "power", Unit: "W", Value: 152},
	}
	if len(ch.Sensors) != len(want) {
		t.Fatalf("sensors = %+v", ch.Sensors)
	}
	for i := range want {
		if ch.Sensors[i] != want[i] {
			t.Errorf("sensor %d = %+v, want %+v", i, ch.Sensors[i], want[i])
		}
	}
}

func TestChassisVirtualAndThermalFallback(t *testing.T) {
	root := tree(t, map[string]string{
		"sys/class/dmi/id/sys_vendor":          "QEMU",
		"sys/class/dmi/id/product_name":        "Standard PC (Q35 + ICH9, 2009)",
		"sys/class/thermal/thermal_zone0/type": "acpitz",
		"sys/class/thermal/thermal_zone0/temp": "27800",
	})
	r := &Reader{Root: root}
	ch := r.Chassis()
	if !ch.Virtual {
		t.Error("QEMU host not recognized as virtual")
	}
	if len(ch.Sensors) != 1 || ch.Sensors[0].Label != "acpitz" || ch.Sensors[0].Value != 27.8 {
		t.Errorf("sensors = %+v", ch.Sensors)
	}
}

func TestGPUsNvidiaSMI(t *testing.T) {
	root := tree(t, map[string]string{
		"sys/bus/pci/devices/0000:41:00.0/class":  "0x030200",
		"sys/bus/pci/devices/0000:41:00.0/vendor": "0x10de",
		"sys/bus/pci/devices/0000:41:00.0/device": "0x2330",
		"sys/bus/pci/devices/0000:00:02.0/class":  "0x030000",
		"sys/bus/pci/devices/0000:00:02.0/vendor": "0x8086",
		"sys/bus/pci/devices/0000:00:02.0/device": "0x46a6",
		"sys/bus/pci/devices/0000:00:1f.0/class":  "0x060100", // not a GPU
	})
	// The driver symlink's base name is the driver name.
	if err := os.MkdirAll(filepath.Join(root, "sys/bus/pci/drivers/nvidia"), 0o755); err != nil {
		t.Fatal(err)
	}
	symlink(t, filepath.Join(root, "sys/bus/pci/drivers/nvidia"), filepath.Join(root, "sys/bus/pci/devices/0000:41:00.0/driver"))

	var asked []string
	r := &Reader{Root: root, Command: func(_ context.Context, name string, args ...string) ([]byte, error) {
		asked = append([]string{name}, args...)
		return []byte("00000000:41:00.0, NVIDIA H100 80GB HBM3, 37, 81559, 20480, 48, 341.20, 700.00, Enabled\n"), nil
	}}
	gpus := r.GPUs(context.Background())
	if len(gpus) != 2 {
		t.Fatalf("gpus = %+v", gpus)
	}
	intel, nv := gpus[0], gpus[1]
	if intel.Vendor != "Intel" || intel.Telemetry != "" || intel.UtilPercent != nil {
		t.Errorf("intel = %+v", intel)
	}
	if len(asked) == 0 || asked[0] != "nvidia-smi" {
		t.Fatalf("nvidia-smi not asked: %v", asked)
	}
	if nv.Telemetry != "nvidia-smi" || nv.Name != "NVIDIA H100 80GB HBM3" || nv.Driver != "nvidia" || nv.MIG != "enabled" {
		t.Errorf("nvidia = %+v", nv)
	}
	if *nv.UtilPercent != 37 || *nv.MemUsedMiB != 20480 || *nv.TempC != 48 || *nv.PowerW != 341.2 || *nv.PowerLimitW != 700 {
		t.Errorf("nvidia numbers = %v %v %v %v %v", *nv.UtilPercent, *nv.MemUsedMiB, *nv.TempC, *nv.PowerW, *nv.PowerLimitW)
	}
}

func TestNumNA(t *testing.T) {
	if num("[N/A]") != nil || num("[Not Supported]") != nil {
		t.Error("N/A parsed as a number")
	}
	if normalizeBus("00000000:41:00.0") != "0000:41:00.0" {
		t.Error(normalizeBus("00000000:41:00.0"))
	}
}

func TestInterfaces(t *testing.T) {
	root := tree(t, map[string]string{
		"sys/class/net/lo/operstate":             "unknown",
		"sys/class/net/eno1/operstate":           "up",
		"sys/class/net/eno1/address":             "aa:bb:cc:00:00:01",
		"sys/class/net/eno1/mtu":                 "9000",
		"sys/class/net/eno1/speed":               "10000",
		"sys/class/net/eno1/statistics/rx_bytes": "1000",
		"sys/class/net/eno1/device/vendor":       "0x8086",
		"sys/class/net/bond0/operstate":          "up",
		"sys/class/net/bond0/bonding/slaves":     "eno2 eno1",
		"sys/class/net/bond0/bonding/mode":       "802.3ad 4",
		"sys/class/net/bond0.100/operstate":      "up",
		"sys/class/net/vmbr0/operstate":          "up",
		"sys/class/net/vmbr0/bridge/stp_state":   "0",
		"sys/class/net/vmbr0/brif/bond0.100/x":   "",
		"sys/class/net/vmbr0/brif/vnet3/x":       "",
		"sys/class/net/vnet3/operstate":          "unknown",
		"sys/class/net/vnet3/tun_flags":          "0x1002",
		"proc/net/vlan/config":                   "VLAN Dev name | VLAN ID\nName-Type: VLAN_NAME_TYPE_RAW_PLUS_VID_NO_PAD\nbond0.100      | 100  | bond0",
	})
	r := &Reader{Root: root}
	got := map[string]Interface{}
	for _, it := range r.Interfaces(func(string) []string { return nil }) {
		got[it.Name] = it
	}
	check := func(name, kind string) Interface {
		t.Helper()
		it, ok := got[name]
		if !ok || it.Kind != kind {
			t.Errorf("%s = %+v, want kind %s", name, it, kind)
		}
		return it
	}
	check("lo", "loopback")
	e := check("eno1", "physical")
	if e.MTU != 9000 || e.SpeedMbps != 10000 || e.RxBytes != 1000 || e.MAC != "aa:bb:cc:00:00:01" {
		t.Errorf("eno1 = %+v", e)
	}
	b := check("bond0", "bond")
	if b.BondMode != "802.3ad" || len(b.Members) != 2 || b.Members[0] != "eno1" {
		t.Errorf("bond0 = %+v", b)
	}
	v := check("bond0.100", "vlan")
	if v.VLANID != 100 || v.Parent != "bond0" {
		t.Errorf("vlan = %+v", v)
	}
	br := check("vmbr0", "bridge")
	if len(br.Members) != 2 || br.Members[0] != "bond0.100" || br.Members[1] != "vnet3" {
		t.Errorf("bridge = %+v", br)
	}
	check("vnet3", "tap")
}

func TestValidInterface(t *testing.T) {
	root := tree(t, map[string]string{"sys/class/net/eno1/operstate": "up"})
	r := &Reader{Root: root}
	for name, want := range map[string]bool{"eno1": true, "eno2": false, "-i": false, "eno1;rm": false, "": false, "../eno1": false} {
		if got := r.ValidInterface(name); got != want {
			t.Errorf("ValidInterface(%q) = %v, want %v", name, got, want)
		}
	}
}

// D14: MIG profiles from "nvidia-smi mig -lgip", vGPU types from sysfs, passthrough readiness.
func TestGPUsNvidiaMIG(t *testing.T) {
	root := tree(t, map[string]string{
		"sys/bus/pci/devices/0000:41:00.0/class":                                               "0x030200",
		"sys/bus/pci/devices/0000:41:00.0/vendor":                                              "0x10de",
		"sys/bus/pci/devices/0000:41:00.0/device":                                              "0x20b0",
		"sys/bus/pci/devices/0000:81:00.0/class":                                               "0x030000",
		"sys/bus/pci/devices/0000:81:00.0/vendor":                                              "0x10de",
		"sys/bus/pci/devices/0000:81:00.0/device":                                              "0x1eb8",
		"sys/bus/pci/devices/0000:81:00.0/mdev_supported_types/nvidia-222/name":                "GRID T4-2Q",
		"sys/bus/pci/devices/0000:81:00.0/mdev_supported_types/nvidia-222/device_api":          "vfio-pci",
		"sys/bus/pci/devices/0000:81:00.0/mdev_supported_types/nvidia-222/available_instances": "8",
		"sys/bus/pci/devices/0000:00:02.0/class":                                               "0x030000",
		"sys/bus/pci/devices/0000:00:02.0/vendor":                                              "0x8086",
		"sys/bus/pci/devices/0000:00:02.0/device":                                              "0x46a6",
	})
	for _, d := range []string{"sys/bus/pci/drivers/nvidia", "sys/kernel/iommu_groups/12"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range []string{"0000:41:00.0", "0000:81:00.0"} {
		symlink(t, filepath.Join(root, "sys/bus/pci/drivers/nvidia"), filepath.Join(root, "sys/bus/pci/devices", a, "driver"))
	}
	symlink(t, filepath.Join(root, "sys/kernel/iommu_groups/12"), filepath.Join(root, "sys/bus/pci/devices/0000:41:00.0/iommu_group"))

	const lgip = `+-----------------------------------------------------------------------------+
| GPU instance profiles:                                                      |
| GPU   Name             ID    Instances   Memory     P2P    SM    DEC   ENC  |
|                              Free/Total   GiB              CE    JPEG  OFA  |
|=============================================================================|
|   0  MIG 1g.5gb        19     5/7        4.75       No     14     0     0   |
|                                                             1     0     0   |
+-----------------------------------------------------------------------------+
|   0  MIG 3g.20gb        9     1/2        19.62      No     42     2     0   |
|                                                             3     0     0   |
+-----------------------------------------------------------------------------+
|   1  MIG 1g.5gb        19     7/7        4.75       No     14     0     0   |
+-----------------------------------------------------------------------------+
`
	r := &Reader{Root: root, Command: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "mig" {
			return []byte(lgip), nil
		}
		return []byte("00000000:41:00.0, NVIDIA A100-PCIE-40GB, 0, 40960, 0, 31, 35.0, 250.00, Enabled, 0\n" +
			"00000000:81:00.0, Tesla T4, 0, 15360, 0, 30, 20.0, 70.00, [N/A], 1\n"), nil
	}}
	gpus := r.GPUs(context.Background())
	if len(gpus) != 3 {
		t.Fatalf("gpus = %+v", gpus)
	}
	intel, a100, t4 := gpus[0], gpus[1], gpus[2]
	if intel.Profile != "whole" || intel.PassThroughReady || intel.MIGProfiles != nil {
		t.Errorf("intel = %+v", intel)
	}
	want := []MIGProfile{
		{ID: "1g.5gb", Name: "MIG 1g.5gb", GIProfileID: 19, MemoryMiB: 4864, Count: 7, Free: 5},
		{ID: "3g.20gb", Name: "MIG 3g.20gb", GIProfileID: 9, MemoryMiB: 20090, Count: 2, Free: 1},
	}
	if a100.Profile != "mig" || !a100.PassThroughReady || !reflect.DeepEqual(a100.MIGProfiles, want) {
		t.Errorf("a100 = %+v profiles %+v", a100, a100.MIGProfiles)
	}
	if t4.Profile != "mdev" || t4.PassThroughReady || t4.MIGProfiles != nil ||
		!reflect.DeepEqual(t4.MDevTypes, []MDevTypeInfo{{ID: "nvidia-222", Name: "GRID T4-2Q", API: "vfio-pci", Available: 8}}) {
		t.Errorf("t4 (not in MIG mode, the index-1 row must not attach) = %+v", t4)
	}
	raw, _ := json.Marshal(intel)
	if strings.Contains(string(raw), "migProfiles") || strings.Contains(string(raw), "index") {
		t.Errorf("JSON of a plain card: %s", raw)
	}
}
