// Package hostinfo reads what libvirt does not tell about the host itself: who built it
// (DMI), its sensors (hwmon), its GPUs and their load, and how its network interfaces are
// wired (bonds, bridges, VLANs). Everything comes from /sys and /proc; Root lets tests point
// it at a fixture tree. Missing files mean "not reported", never an error: a VM host without
// fan sensors is normal.
package hostinfo

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Reader reads host facts below Root ("/" on a real host).
type Reader struct {
	Root string
	// Command runs a helper program (nvidia-smi); nil: exec.CommandContext. Tests replace it.
	Command func(ctx context.Context, name string, args ...string) ([]byte, error)
}

// New reads the real host.
func New() *Reader { return &Reader{Root: "/"} }

func (r *Reader) path(p string) string { return filepath.Join(r.Root, p) }

// read returns the trimmed content of a small sysfs/procfs file ("" when missing).
func (r *Reader) read(p string) string {
	b, err := os.ReadFile(r.path(p))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (r *Reader) readInt(p string) (int64, bool) {
	v, err := strconv.ParseInt(r.read(p), 10, 64)
	return v, err == nil
}

func (r *Reader) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if r.Command != nil {
		return r.Command(ctx, name, args...)
	}
	if _, err := exec.LookPath(name); err != nil {
		return nil, err
	}
	return exec.CommandContext(ctx, name, args...).Output()
}

// ---- Chassis: DMI, OS, sensors -------------------------------------------------------------

// Chassis is the machine VirtoMir runs on.
type Chassis struct {
	Vendor      string `json:"vendor,omitempty"`
	Product     string `json:"product,omitempty"`
	Version     string `json:"version,omitempty"`
	Serial      string `json:"serial,omitempty"`
	BoardVendor string `json:"boardVendor,omitempty"`
	BoardName   string `json:"boardName,omitempty"`
	BIOSVendor  string `json:"biosVendor,omitempty"`
	BIOSVersion string `json:"biosVersion,omitempty"`
	BIOSDate    string `json:"biosDate,omitempty"`
	// ChassisType is the SMBIOS enclosure type in words (Rack Mount Chassis, Desktop, …).
	ChassisType string `json:"chassisType,omitempty"`
	// Virtual is set when the host itself is a virtual machine (nested virtualization).
	Virtual bool `json:"virtual"`

	OS        string  `json:"os,omitempty"`
	Kernel    string  `json:"kernel,omitempty"`
	UptimeSec float64 `json:"uptimeSec,omitempty"`
	BootTime  string  `json:"bootTime,omitempty"`

	Sensors []Sensor `json:"sensors"`
}

// Sensor is one hwmon or thermal-zone reading.
type Sensor struct {
	Chip  string  `json:"chip"`  // driver name: coretemp, k10temp, nct6775, nvme, …
	Label string  `json:"label"` // "Package id 0", "fan1", …
	Kind  string  `json:"kind"`  // temperature, fan, power, voltage
	Value float64 `json:"value"` // °C, RPM, W, V
	Unit  string  `json:"unit"`
	// Max and Crit are the chip's own limits where it reports them (0: unknown).
	Max  float64 `json:"max,omitempty"`
	Crit float64 `json:"crit,omitempty"`
}

// SMBIOS 3.x enclosure types (DMI type 3), as far as anyone puts VMs on them.
var chassisTypes = map[string]string{
	"1": "Other", "2": "Unknown", "3": "Desktop", "4": "Low Profile Desktop", "6": "Mini Tower",
	"7": "Tower", "8": "Portable", "9": "Laptop", "10": "Notebook", "13": "All in One",
	"17": "Main Server Chassis", "23": "Rack Mount Chassis", "24": "Sealed-case PC",
	"25": "Multi-system chassis", "28": "Blade", "29": "Blade Enclosure", "30": "Tablet",
	"31": "Convertible", "32": "Detachable", "33": "IoT Gateway", "34": "Embedded PC",
	"35": "Mini PC", "36": "Stick PC",
}

// Vendors whose DMI says the "hardware" is a hypervisor.
var virtualVendors = []string{"qemu", "microsoft corporation", "vmware", "innotek", "xen", "red hat", "bochs", "parallels", "amazon ec2", "google"}

func (r *Reader) Chassis() Chassis {
	dmi := func(f string) string { return clean(r.read("sys/class/dmi/id/" + f)) }
	c := Chassis{
		Vendor: dmi("sys_vendor"), Product: dmi("product_name"), Version: dmi("product_version"),
		Serial: dmi("product_serial"), BoardVendor: dmi("board_vendor"), BoardName: dmi("board_name"),
		BIOSVendor: dmi("bios_vendor"), BIOSVersion: dmi("bios_version"), BIOSDate: dmi("bios_date"),
		ChassisType: chassisTypes[dmi("chassis_type")],
		Kernel:      r.read("proc/sys/kernel/osrelease"),
		OS:          r.osName(),
	}
	v := strings.ToLower(c.Vendor + " " + c.Product)
	for _, x := range virtualVendors {
		if strings.Contains(v, x) {
			c.Virtual = true
		}
	}
	if f := strings.Fields(r.read("proc/uptime")); len(f) > 0 {
		if up, err := strconv.ParseFloat(f[0], 64); err == nil {
			c.UptimeSec = up
			c.BootTime = time.Now().Add(-time.Duration(up * float64(time.Second))).UTC().Format(time.RFC3339)
		}
	}
	c.Sensors = r.Sensors()
	return c
}

// clean drops the placeholders firmware vendors put in empty DMI fields.
func clean(s string) string {
	switch strings.ToLower(s) {
	case "", "to be filled by o.e.m.", "default string", "not specified", "system product name",
		"system manufacturer", "none", "0123456789", "not applicable", "o.e.m.", "system version", "x.x":
		return ""
	}
	return s
}

func (r *Reader) osName() string {
	f, err := os.Open(r.path("etc/os-release"))
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "PRETTY_NAME="); ok {
			return strings.Trim(v, `"'`)
		}
	}
	return ""
}

// Sensors reads every hwmon chip; without hwmon temperatures it falls back to thermal zones.
func (r *Reader) Sensors() []Sensor {
	out := []Sensor{}
	chips, _ := filepath.Glob(r.path("sys/class/hwmon/hwmon*"))
	sort.Slice(chips, func(i, j int) bool { return natLess(chips[i], chips[j]) })
	for _, dir := range chips {
		rel, _ := filepath.Rel(r.Root, dir)
		rel = filepath.ToSlash(rel)
		chip := r.read(rel + "/name")
		if chip == "" {
			chip = filepath.Base(dir)
		}
		inputs, _ := filepath.Glob(filepath.Join(dir, "*_input"))
		sort.Slice(inputs, func(i, j int) bool { return natLess(inputs[i], inputs[j]) })
		for _, in := range inputs {
			base := strings.TrimSuffix(filepath.Base(in), "_input") // temp1, fan2, power1, in0
			kind, unit, scale := sensorKind(base)
			if kind == "" {
				continue
			}
			raw, ok := r.readInt(rel + "/" + base + "_input")
			if !ok {
				continue
			}
			s := Sensor{Chip: chip, Label: r.read(rel + "/" + base + "_label"), Kind: kind, Unit: unit, Value: float64(raw) / scale}
			if s.Label == "" {
				s.Label = base
			}
			if v, ok := r.readInt(rel + "/" + base + "_max"); ok && v > 0 {
				s.Max = float64(v) / scale
			}
			if v, ok := r.readInt(rel + "/" + base + "_crit"); ok && v > 0 {
				s.Crit = float64(v) / scale
			}
			if kind == "fan" && raw == 0 && r.read(rel+"/"+base+"_label") == "" {
				continue // an unconnected fan header
			}
			out = append(out, s)
		}
		// power*_average instead of power*_input on some chips (e.g. amdgpu, some PSUs).
		avgs, _ := filepath.Glob(filepath.Join(dir, "power*_average"))
		for _, a := range avgs {
			base := strings.TrimSuffix(filepath.Base(a), "_average")
			if _, err := os.Stat(filepath.Join(dir, base+"_input")); err == nil {
				continue
			}
			if raw, ok := r.readInt(rel + "/" + base + "_average"); ok {
				label := r.read(rel + "/" + base + "_label")
				if label == "" {
					label = base
				}
				out = append(out, Sensor{Chip: chip, Label: label, Kind: "power", Unit: "W", Value: float64(raw) / 1e6})
			}
		}
	}
	hasTemp := false
	for _, s := range out {
		hasTemp = hasTemp || s.Kind == "temperature"
	}
	if !hasTemp {
		zones, _ := filepath.Glob(r.path("sys/class/thermal/thermal_zone*"))
		sort.Slice(zones, func(i, j int) bool { return natLess(zones[i], zones[j]) })
		for _, z := range zones {
			rel, _ := filepath.Rel(r.Root, z)
			rel = filepath.ToSlash(rel)
			if v, ok := r.readInt(rel + "/temp"); ok {
				label := r.read(rel + "/type")
				out = append(out, Sensor{Chip: "thermal", Label: label, Kind: "temperature", Unit: "°C", Value: float64(v) / 1000})
			}
		}
	}
	return out
}

// sensorKind maps an hwmon channel prefix to what it measures and its sysfs scale.
func sensorKind(base string) (kind, unit string, scale float64) {
	switch {
	case strings.HasPrefix(base, "temp"):
		return "temperature", "°C", 1000
	case strings.HasPrefix(base, "fan"):
		return "fan", "RPM", 1
	case strings.HasPrefix(base, "power"):
		return "power", "W", 1e6
	case strings.HasPrefix(base, "in"):
		return "voltage", "V", 1000
	}
	return "", "", 0
}

// natLess orders hwmon2 before hwmon10 and temp2 before temp10.
func natLess(a, b string) bool {
	pa, na := splitNum(a)
	pb, nb := splitNum(b)
	if pa != pb || na < 0 || nb < 0 {
		return a < b
	}
	return na < nb
}

// splitNum splits ".../temp12_input" into ".../temp" and 12 (-1 without a number).
func splitNum(s string) (string, int) {
	s = strings.TrimSuffix(s, "_input")
	i := len(s)
	for i > 0 && s[i-1] >= '0' && s[i-1] <= '9' {
		i--
	}
	n, err := strconv.Atoi(s[i:])
	if err != nil {
		return s, -1
	}
	return s[:i], n
}
