package hostinfo

import (
	"context"
	"encoding/csv"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// GPU is a display/3D controller of the host. Load figures come from the vendor's own
// interface (nvidia-smi, amdgpu sysfs); without one only the PCI facts are known.
type GPU struct {
	Address  string `json:"address"` // PCI, e.g. 0000:41:00.0
	VendorID string `json:"vendorId"`
	DeviceID string `json:"deviceId"`
	Vendor   string `json:"vendor"` // NVIDIA, AMD, Intel, …
	Name     string `json:"name,omitempty"`
	Driver   string `json:"driver,omitempty"` // nvidia, amdgpu, i915, vfio-pci, …
	// Telemetry says where the numbers below come from: "nvidia-smi", "amdgpu" or "" (none).
	Telemetry   string   `json:"telemetry,omitempty"`
	UtilPercent *float64 `json:"utilPercent,omitempty"`
	MemTotalMiB *float64 `json:"memTotalMiB,omitempty"`
	MemUsedMiB  *float64 `json:"memUsedMiB,omitempty"`
	TempC       *float64 `json:"tempC,omitempty"`
	PowerW      *float64 `json:"powerW,omitempty"`
	PowerLimitW *float64 `json:"powerLimitW,omitempty"`
	// MIG is "enabled"/"disabled" on NVIDIA cards that support Multi-Instance GPU.
	MIG string `json:"mig,omitempty"`
}

var gpuVendors = map[string]string{"0x10de": "NVIDIA", "0x1002": "AMD", "0x8086": "Intel", "0x1af4": "Virtio", "0x1234": "QEMU", "0x15ad": "VMware", "0x1b36": "Red Hat"}

// GPUs lists PCI class 03 (display) devices with whatever load data their driver offers.
func (r *Reader) GPUs(ctx context.Context) []GPU {
	devs, _ := filepath.Glob(r.path("sys/bus/pci/devices/*"))
	sort.Strings(devs)
	out := []GPU{}
	for _, d := range devs {
		addr := filepath.Base(d)
		rel := "sys/bus/pci/devices/" + addr
		if !strings.HasPrefix(r.read(rel+"/class"), "0x03") {
			continue
		}
		g := GPU{Address: addr, VendorID: r.read(rel + "/vendor"), DeviceID: r.read(rel + "/device")}
		g.Vendor = gpuVendors[g.VendorID]
		if g.Vendor == "" {
			g.Vendor = g.VendorID
		}
		if link, err := os.Readlink(filepath.Join(d, "driver")); err == nil {
			g.Driver = filepath.Base(link)
		}
		if g.Driver == "amdgpu" {
			r.amdgpu(rel, &g)
		}
		out = append(out, g)
	}
	r.nvidia(ctx, out)
	return out
}

// amdgpu exposes load and VRAM in the device directory and temperature/power in its hwmon.
func (r *Reader) amdgpu(rel string, g *GPU) {
	g.Telemetry = "amdgpu"
	if v, ok := r.readInt(rel + "/gpu_busy_percent"); ok {
		g.UtilPercent = f(float64(v))
	}
	if v, ok := r.readInt(rel + "/mem_info_vram_total"); ok {
		g.MemTotalMiB = f(float64(v) / (1 << 20))
	}
	if v, ok := r.readInt(rel + "/mem_info_vram_used"); ok {
		g.MemUsedMiB = f(float64(v) / (1 << 20))
	}
	mons, _ := filepath.Glob(r.path(rel + "/hwmon/hwmon*"))
	for _, m := range mons {
		mrel := rel + "/hwmon/" + filepath.Base(m)
		if v, ok := r.readInt(mrel + "/temp1_input"); ok {
			g.TempC = f(float64(v) / 1000)
		}
		if v, ok := r.readInt(mrel + "/power1_average"); ok {
			g.PowerW = f(float64(v) / 1e6)
		} else if v, ok := r.readInt(mrel + "/power1_input"); ok {
			g.PowerW = f(float64(v) / 1e6)
		}
		if v, ok := r.readInt(mrel + "/power1_cap"); ok {
			g.PowerLimitW = f(float64(v) / 1e6)
		}
	}
}

// nvidiaQuery is what nvidia-smi is asked for, in this order.
const nvidiaQuery = "pci.bus_id,name,utilization.gpu,memory.total,memory.used,temperature.gpu,power.draw,power.limit,mig.mode.current"

// nvidia fills in NVIDIA cards from nvidia-smi (only present with the proprietary driver,
// i.e. not for cards bound to vfio-pci for passthrough).
func (r *Reader) nvidia(ctx context.Context, gpus []GPU) {
	has := false
	for _, g := range gpus {
		has = has || (g.VendorID == "0x10de" && g.Driver == "nvidia")
	}
	if !has {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := r.run(ctx, "nvidia-smi", "--query-gpu="+nvidiaQuery, "--format=csv,noheader,nounits")
	if err != nil {
		return
	}
	rows, err := csv.NewReader(strings.NewReader(string(out))).ReadAll()
	if err != nil {
		return
	}
	for _, row := range rows {
		if len(row) < 9 {
			continue
		}
		for i := range row {
			row[i] = strings.TrimSpace(row[i])
		}
		bus := normalizeBus(row[0])
		for i := range gpus {
			if !strings.EqualFold(gpus[i].Address, bus) {
				continue
			}
			g := &gpus[i]
			g.Telemetry, g.Name = "nvidia-smi", row[1]
			g.UtilPercent, g.MemTotalMiB, g.MemUsedMiB = num(row[2]), num(row[3]), num(row[4])
			g.TempC, g.PowerW, g.PowerLimitW = num(row[5]), num(row[6]), num(row[7])
			if m := strings.ToLower(row[8]); m == "enabled" || m == "disabled" {
				g.MIG = m
			}
		}
	}
}

// normalizeBus turns nvidia-smi's "00000000:41:00.0" into sysfs's "0000:41:00.0".
func normalizeBus(s string) string {
	dom, rest, ok := strings.Cut(strings.ToLower(s), ":")
	if !ok {
		return s
	}
	if len(dom) > 4 {
		dom = dom[len(dom)-4:]
	}
	return dom + ":" + rest
}

// num parses an nvidia-smi field; "[N/A]", "[Not Supported]" and friends are nil.
func num(s string) *float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &v
}

func f(v float64) *float64 { return &v }
