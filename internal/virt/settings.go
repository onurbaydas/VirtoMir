package virt

import (
	"fmt"
	"slices"
	"strings"

	"github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"
)

// Settings are the common knobs of a machine as a form. They are read from and written
// to the persistent XML, so the form and the XML editor always show the same truth.
type Settings struct {
	Title       string `json:"title"`
	Description string `json:"description"`

	VCPUs    uint   `json:"vcpus"`    // active at boot (<vcpu current=…>)
	MaxVCPUs uint   `json:"maxVcpus"` // <vcpu>, the hot-plug ceiling
	CPUMode  string `json:"cpuMode"`  // host-passthrough, host-model, maximum, custom
	CPUModel string `json:"cpuModel,omitempty"`
	Sockets  int    `json:"sockets"`
	Cores    int    `json:"cores"`
	Threads  int    `json:"threads"`

	MemoryMiB        uint64 `json:"memoryMiB"`        // <memory>
	CurrentMemoryMiB uint64 `json:"currentMemoryMiB"` // <currentMemory>, the balloon target

	// Device keys in boot order: "disk:<target>" for disks and drives, "net:<mac>" for adapters.
	BootOrder []string `json:"bootOrder"`
	BootMenu  bool     `json:"bootMenu"`

	Firmware   string `json:"firmware"` // uefi or bios
	SecureBoot bool   `json:"secureBoot"`
}

// BootDevice is something the machine can boot from.
type BootDevice struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

type SettingsView struct {
	Settings
	BootDevices []BootDevice `json:"bootDevices"`
	// HotplugMemoryMiB is <maxMemory>, the upper limit for memory, if configured.
	HotplugMemoryMiB uint64 `json:"hotplugMemoryMiB,omitempty"`
}

// SaveResult tells what happened: saved to the definition, and what was applied live.
type SaveResult struct {
	DefineResult
	AppliedLive []string `json:"appliedLive"` // e.g. "vCPUs", "memory"
	LiveErrors  []string `json:"liveErrors"`  // live changes that did not work (saved for next boot)
}

func settingsFromXML(x *libvirtxml.Domain) SettingsView {
	d := buildDetail(Domain{}, x)
	v := SettingsView{Settings: Settings{
		Title: x.Title, Description: x.Description,
		VCPUs: d.CPU.VCPUs, MaxVCPUs: d.CPU.MaxVCPUs, CPUMode: d.CPU.Mode, CPUModel: d.CPU.Model,
		Sockets: d.CPU.Sockets, Cores: d.CPU.Cores, Threads: d.CPU.Threads,
		MemoryMiB: d.Memory.MaxKiB >> 10, CurrentMemoryMiB: d.Memory.CurrentKiB >> 10,
		BootMenu: d.System.BootMenu, Firmware: d.System.Firmware, SecureBoot: d.System.SecureBoot,
		BootOrder: []string{},
	}, BootDevices: bootDevices(x), HotplugMemoryMiB: d.Memory.HotplugKiB >> 10}
	v.BootOrder = currentBootOrder(x, v.BootDevices)
	return v
}

func bootDevices(x *libvirtxml.Domain) []BootDevice {
	out := []BootDevice{}
	if x.Devices == nil {
		return out
	}
	for _, d := range x.Devices.Disks {
		if d.Target == nil {
			continue
		}
		kind := map[string]string{"cdrom": "CD/DVD drive", "floppy": "Floppy drive"}[d.Device]
		if kind == "" {
			kind = "Disk"
		}
		label := kind + " " + d.Target.Dev
		if src, _ := diskSource(d.Source); src != "" {
			label += " · " + baseName(src)
		} else if d.Device == "cdrom" {
			label += " · empty"
		}
		out = append(out, BootDevice{Key: "disk:" + d.Target.Dev, Label: label})
	}
	for _, n := range x.Devices.Interfaces {
		if n.MAC != nil {
			out = append(out, BootDevice{Key: "net:" + strings.ToLower(n.MAC.Address), Label: "Network (PXE) " + strings.ToLower(n.MAC.Address)})
		}
	}
	return out
}

// currentBootOrder reads per-device <boot order>, or maps legacy <os><boot dev> onto devices.
func currentBootOrder(x *libvirtxml.Domain, devs []BootDevice) []string {
	type ordered struct {
		key   string
		order uint
	}
	var per []ordered
	if x.Devices != nil {
		for _, d := range x.Devices.Disks {
			if d.Boot != nil && d.Target != nil {
				per = append(per, ordered{"disk:" + d.Target.Dev, d.Boot.Order})
			}
		}
		for _, n := range x.Devices.Interfaces {
			if n.Boot != nil && n.MAC != nil {
				per = append(per, ordered{"net:" + strings.ToLower(n.MAC.Address), n.Boot.Order})
			}
		}
	}
	if len(per) > 0 {
		slices.SortStableFunc(per, func(a, b ordered) int { return int(a.order) - int(b.order) })
		out := make([]string, len(per))
		for i, p := range per {
			out[i] = p.key
		}
		return out
	}
	out := []string{}
	if x.OS == nil || x.Devices == nil {
		return out
	}
	firstOf := func(match func(string) bool) string {
		for _, d := range devs {
			if match(d.Key) {
				return d.Key
			}
		}
		return ""
	}
	for _, b := range x.OS.BootDevices {
		var key string
		switch b.Dev {
		case "hd":
			key = firstDisk(x, "disk")
		case "cdrom":
			key = firstDisk(x, "cdrom")
		case "fd":
			key = firstDisk(x, "floppy")
		case "network":
			key = firstOf(func(k string) bool { return strings.HasPrefix(k, "net:") })
		}
		if key != "" && !slices.Contains(out, key) {
			out = append(out, key)
		}
	}
	return out
}

func firstDisk(x *libvirtxml.Domain, device string) string {
	for _, d := range x.Devices.Disks {
		if orDefault(d.Device, "disk") == device && d.Target != nil {
			return "disk:" + d.Target.Dev
		}
	}
	return ""
}

var cpuModes = []string{"host-passthrough", "host-model", "maximum", "custom"}

// applySettings validates s and writes it into x. Only what changed is touched, so
// settings the form does not know about survive untouched.
func applySettings(x *libvirtxml.Domain, s Settings) error {
	old := settingsFromXML(x)

	if s.MaxVCPUs < 1 || s.MaxVCPUs > 512 {
		return invalidf("Maximum vCPUs must be between 1 and 512.")
	}
	if s.VCPUs < 1 || s.VCPUs > s.MaxVCPUs {
		return invalidf("Active vCPUs must be between 1 and the maximum (%d).", s.MaxVCPUs)
	}
	if s.Sockets < 1 || s.Cores < 1 || s.Threads < 1 || uint(s.Sockets*s.Cores*s.Threads) != s.MaxVCPUs {
		return invalidf("Sockets × cores × threads must equal the maximum vCPUs (%d).", s.MaxVCPUs)
	}
	if !slices.Contains(cpuModes, s.CPUMode) {
		return invalidf("Unknown CPU mode %q.", s.CPUMode)
	}
	if s.CPUMode == "custom" && strings.TrimSpace(s.CPUModel) == "" {
		return invalidf("A named CPU model is required for custom mode, e.g. Skylake-Client or EPYC.")
	}
	if s.MemoryMiB < 64 {
		return invalidf("Memory must be at least 64 MiB.")
	}
	if s.CurrentMemoryMiB < 64 || s.CurrentMemoryMiB > s.MemoryMiB {
		return invalidf("Current memory must be between 64 MiB and the assigned memory.")
	}
	if old.HotplugMemoryMiB > 0 && s.MemoryMiB > old.HotplugMemoryMiB {
		return invalidf("Memory cannot exceed the hot-plug maximum (%d MiB).", old.HotplugMemoryMiB)
	}
	if s.Firmware != "uefi" && s.Firmware != "bios" {
		return invalidf("Firmware must be uefi or bios.")
	}
	if s.Firmware == "bios" && s.SecureBoot {
		return invalidf("Secure Boot needs UEFI firmware.")
	}

	x.Title, x.Description = s.Title, s.Description

	// CPU
	x.VCPU = orNew(x.VCPU)
	x.VCPU.Value, x.VCPU.Current = s.MaxVCPUs, 0
	if s.VCPUs != s.MaxVCPUs {
		x.VCPU.Current = s.VCPUs
	}
	x.CPU = orNew(x.CPU)
	x.CPU.Mode = s.CPUMode
	if s.CPUMode == "custom" {
		x.CPU.Model = &libvirtxml.DomainCPUModel{Value: s.CPUModel, Fallback: "allow"}
		if x.CPU.Match == "" {
			x.CPU.Match = "exact"
		}
	} else {
		x.CPU.Model, x.CPU.Match = nil, ""
	}
	topo := orNew(x.CPU.Topology)
	topo.Sockets, topo.Cores, topo.Threads = s.Sockets, s.Cores, s.Threads
	if topo.Dies > 1 || topo.Clusters > 1 {
		topo.Dies, topo.Clusters = 1, 1 // the form models sockets × cores × threads only
	}
	x.CPU.Topology = topo

	// Memory (written in MiB; libvirt normalizes to KiB)
	if s.MemoryMiB != old.MemoryMiB {
		x.Memory = &libvirtxml.DomainMemory{Value: uint(s.MemoryMiB), Unit: "MiB"}
	}
	if s.CurrentMemoryMiB != old.CurrentMemoryMiB || s.MemoryMiB != old.MemoryMiB {
		x.CurrentMemory = &libvirtxml.DomainCurrentMemory{Value: uint(s.CurrentMemoryMiB), Unit: "MiB"}
	}

	// Boot: per-device order replaces <os><boot dev>; libvirt does not allow both.
	if !slices.Equal(s.BootOrder, old.BootOrder) {
		if err := applyBootOrder(x, s.BootOrder); err != nil {
			return err
		}
	}
	x.OS = orNew(x.OS)
	if s.BootMenu != old.BootMenu {
		if s.BootMenu {
			x.OS.BootMenu = &libvirtxml.DomainBootMenu{Enable: "yes"}
		} else {
			x.OS.BootMenu = nil
		}
	}

	// Firmware: changing it resets UEFI variables (and an installed OS may not boot).
	if s.Firmware != old.Firmware || s.SecureBoot != old.SecureBoot {
		applyFirmware(x, s.Firmware, s.SecureBoot)
	}
	return nil
}

func applyBootOrder(x *libvirtxml.Domain, order []string) error {
	known := map[string]bool{}
	for _, d := range bootDevices(x) {
		known[d.Key] = true
	}
	pos := map[string]uint{}
	for i, k := range order {
		if !known[k] {
			return invalidf("Unknown boot device %q.", k)
		}
		if pos[k] != 0 {
			return invalidf("%s appears twice in the boot order.", k)
		}
		pos[k] = uint(i + 1)
	}
	if x.OS != nil {
		x.OS.BootDevices = nil
	}
	if x.Devices == nil {
		return nil
	}
	for i := range x.Devices.Disks {
		d := &x.Devices.Disks[i]
		d.Boot = nil
		if d.Target != nil {
			if o := pos["disk:"+d.Target.Dev]; o > 0 {
				d.Boot = &libvirtxml.DomainDeviceBoot{Order: o}
			}
		}
	}
	for i := range x.Devices.Interfaces {
		n := &x.Devices.Interfaces[i]
		n.Boot = nil
		if n.MAC != nil {
			if o := pos["net:"+strings.ToLower(n.MAC.Address)]; o > 0 {
				n.Boot = &libvirtxml.DomainDeviceBoot{Order: o}
			}
		}
	}
	// Hostdevs keep no boot order either: the list above is the whole order.
	for i := range x.Devices.Hostdevs {
		x.Devices.Hostdevs[i].Boot = nil
	}
	return nil
}

func applyFirmware(x *libvirtxml.Domain, fw string, secure bool) {
	x.OS = orNew(x.OS)
	// Explicit loader/NVRAM paths belong to the old firmware; let libvirt pick new ones.
	x.OS.Loader, x.OS.NVRam = nil, nil
	if fw == "bios" {
		x.OS.Firmware, x.OS.FirmwareInfo = "", nil
		return
	}
	x.OS.Firmware = "efi"
	feats := []libvirtxml.DomainOSFirmwareFeature{{Name: "secure-boot", Enabled: "no"}}
	if secure {
		feats = []libvirtxml.DomainOSFirmwareFeature{{Name: "secure-boot", Enabled: "yes"}, {Name: "enrolled-keys", Enabled: "yes"}}
		x.Features = orNew(x.Features)
		x.Features.SMM = &libvirtxml.DomainFeatureSMM{State: "on"}
	}
	// Secure Boot needs a q35 machine; libvirt reports that clearly on save.
	x.OS.FirmwareInfo = &libvirtxml.DomainOSFirmwareInfo{Features: feats}
}

func orNew[T any](p *T) *T {
	if p == nil {
		return new(T)
	}
	return p
}

// GetSettings reads the form from the persistent definition.
func (c *Client) GetSettings(uuid string) (SettingsView, error) {
	l, d, err := c.lookup(uuid)
	if err != nil {
		return SettingsView{}, err
	}
	x, err := c.parseDomain(l, d, true)
	if err != nil {
		return SettingsView{}, err
	}
	return settingsFromXML(x), nil
}

// PreviewSettings returns the XML the settings would produce, without saving.
func (c *Client) PreviewSettings(uuid string, s Settings) (string, error) {
	l, d, err := c.lookup(uuid)
	if err != nil {
		return "", err
	}
	x, err := c.parseDomain(l, d, true)
	if err != nil {
		return "", err
	}
	if err := applySettings(x, s); err != nil {
		return "", err
	}
	return x.Marshal()
}

// SaveSettings writes the settings to the definition. On a running machine, active vCPUs
// and the balloon target are also applied live when possible.
func (c *Client) SaveSettings(uuid string, s Settings) (SaveResult, error) {
	res := SaveResult{AppliedLive: []string{}, LiveErrors: []string{}}
	l, d, err := c.lookup(uuid)
	if err != nil {
		return res, err
	}
	x, err := c.parseDomain(l, d, true)
	if err != nil {
		return res, err
	}
	old := settingsFromXML(x)
	if err := applySettings(x, s); err != nil {
		return res, err
	}
	doc, err := x.Marshal()
	if err != nil {
		return res, err
	}
	sum, err := summary(l, d)
	if err != nil {
		return res, classify(err)
	}
	if _, err := l.DomainDefineXMLFlags(doc, libvirt.DomainDefineValidate); err != nil {
		return res, classify(err)
	}
	res.AppliesOnNextBoot = isOn(sum.State)
	c.log.Info("domain settings saved", "domain", d.Name)

	if !isOn(sum.State) {
		return res, nil
	}
	if s.VCPUs != old.VCPUs {
		if err := l.DomainSetVcpusFlags(d, uint32(s.VCPUs), uint32(libvirt.DomainVCPULive)); err != nil {
			res.LiveErrors = append(res.LiveErrors, fmt.Sprintf("vCPUs: %v", classify(err)))
		} else {
			res.AppliedLive = append(res.AppliedLive, "vCPUs")
		}
	}
	if s.CurrentMemoryMiB != old.CurrentMemoryMiB {
		if err := l.DomainSetMemoryFlags(d, s.CurrentMemoryMiB<<10, uint32(libvirt.DomainMemLive)); err != nil {
			res.LiveErrors = append(res.LiveErrors, fmt.Sprintf("memory: %v", classify(err)))
		} else {
			res.AppliedLive = append(res.AppliedLive, "memory")
		}
	}
	return res, nil
}
