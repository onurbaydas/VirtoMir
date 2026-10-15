package virt

import (
	"fmt"
	"slices"
	"strings"

	"libvirt.org/go/libvirtxml"
)

// DomainDetail is everything the machine page shows: the list summary plus a readable
// view of the domain XML. The XML itself stays the source of truth (see Client.DomainXML).
type DomainDetail struct {
	Domain
	Title       string     `json:"title,omitempty"`
	Description string     `json:"description,omitempty"`
	System      SystemInfo `json:"system"`
	CPU         CPUInfo    `json:"cpu"`
	Memory      MemoryInfo `json:"memory"`
	Devices     []Device   `json:"devices"`
}

type SystemInfo struct {
	Hypervisor string `json:"hypervisor"` // libvirt domain type: kvm, qemu (TCG), …
	OSType     string `json:"osType"`     // hvm, xen, linux, …
	Arch       string `json:"arch"`
	Machine    string `json:"machine"`
	Firmware   string `json:"firmware"` // "bios" or "uefi"
	SecureBoot bool   `json:"secureBoot"`
	Loader     string `json:"loader,omitempty"`
	NVRAM      string `json:"nvram,omitempty"`
	// Direct kernel boot, if configured.
	Kernel  string `json:"kernel,omitempty"`
	Cmdline string `json:"cmdline,omitempty"`
	// Boot devices in order, e.g. "Disk vda", "Network 52:54:00:…".
	BootOrder  []string `json:"bootOrder"`
	BootMenu   bool     `json:"bootMenu"`
	Emulator   string   `json:"emulator,omitempty"`
	Clock      string   `json:"clock,omitempty"`
	Features   []string `json:"features"`
	OnPoweroff string   `json:"onPoweroff,omitempty"`
	OnReboot   string   `json:"onReboot,omitempty"`
	OnCrash    string   `json:"onCrash,omitempty"`
}

type CPUInfo struct {
	Mode      string `json:"mode"` // host-passthrough, host-model, custom, maximum
	Model     string `json:"model,omitempty"`
	Sockets   int    `json:"sockets"`
	Dies      int    `json:"dies,omitempty"`
	Cores     int    `json:"cores"`
	Threads   int    `json:"threads"`
	VCPUs     uint   `json:"vcpus"`    // currently plugged
	MaxVCPUs  uint   `json:"maxVcpus"` // <vcpu> value
	CPUSet    string `json:"cpuset,omitempty"`
	Features  int    `json:"features"` // explicit <feature> overrides
	NUMANodes int    `json:"numaNodes"`
	Pinned    bool   `json:"pinned"`
}

type MemoryInfo struct {
	MaxKiB     uint64 `json:"maxKiB"`     // <memory>
	CurrentKiB uint64 `json:"currentKiB"` // <currentMemory>, i.e. the balloon target
	HotplugKiB uint64 `json:"hotplugKiB,omitempty"`
	Hugepages  bool   `json:"hugepages"`
	Balloon    string `json:"balloon,omitempty"` // memballoon model, "" when none
}

// DeviceGroup orders devices on the machine page. Groups after "display" are
// "advanced": shown on request, never hidden from the XML.
type DeviceGroup string

const (
	GroupStorage     DeviceGroup = "storage"
	GroupNetwork     DeviceGroup = "network"
	GroupDisplay     DeviceGroup = "display"
	GroupConsole     DeviceGroup = "console"
	GroupPassthrough DeviceGroup = "passthrough"
	GroupInput       DeviceGroup = "input"
	GroupSystem      DeviceGroup = "system"
	GroupController  DeviceGroup = "controller"
)

// Device is one entry of <devices>, flattened for display. Props carry the friendly label
// plus the libvirt XML name, so power users can map every value back to the XML.
type Device struct {
	Kind    string      `json:"kind"` // disk, cdrom, floppy, interface, graphics, video, serial, …
	Group   DeviceGroup `json:"group"`
	Title   string      `json:"title"`
	Summary string      `json:"summary,omitempty"`
	Boot    uint        `json:"bootOrder,omitempty"`
	Props   []Prop      `json:"props"`

	// Filled from libvirt at request time where available.
	CapacityBytes   uint64   `json:"capacityBytes,omitempty"`
	AllocationBytes uint64   `json:"allocationBytes,omitempty"`
	Addresses       []string `json:"addresses,omitempty"`

	// Identify the device for actions: a disk's guest device name, an adapter's MAC.
	Target string `json:"target,omitempty"`
	MAC    string `json:"mac,omitempty"`
}

type Prop struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Tech  string `json:"tech,omitempty"` // XML element/attribute, e.g. "driver@cache"
}

// buildDetail turns parsed domain XML into the page model. It never fails: unknown or
// missing parts are left empty, the raw XML stays available for everything else.
func buildDetail(sum Domain, x *libvirtxml.Domain) DomainDetail {
	d := DomainDetail{Domain: sum, Title: x.Title, Description: x.Description, Devices: []Device{}}
	d.System = systemInfo(x)
	d.CPU = cpuInfo(x)
	d.Memory = memoryInfo(x)

	if dev := x.Devices; dev != nil {
		for _, v := range dev.Disks {
			d.Devices = append(d.Devices, diskDevice(v))
		}
		for _, v := range dev.Filesystems {
			d.Devices = append(d.Devices, filesystemDevice(v))
		}
		for _, v := range dev.Interfaces {
			d.Devices = append(d.Devices, interfaceDevice(v))
		}
		for _, v := range dev.Graphics {
			d.Devices = append(d.Devices, graphicsDevice(v))
		}
		for _, v := range dev.Videos {
			d.Devices = append(d.Devices, videoDevice(v))
		}
		for _, v := range dev.Consoles {
			d.Devices = append(d.Devices, chardevDevice("console", "Console", v.Source, consoleTargetType(v.Target), consolePort(v.Target), v.Alias))
		}
		for _, v := range dev.Serials {
			d.Devices = append(d.Devices, chardevDevice("serial", "Serial port", v.Source, serialTargetType(v.Target), serialPort(v.Target), v.Alias))
		}
		for _, v := range dev.Parallels {
			d.Devices = append(d.Devices, chardevDevice("parallel", "Parallel port", v.Source, "", nil, v.Alias))
		}
		for _, v := range dev.Channels {
			d.Devices = append(d.Devices, channelDevice(v))
		}
		for _, v := range dev.Hostdevs {
			d.Devices = append(d.Devices, hostdevDevice(v))
		}
		for _, v := range dev.RedirDevs {
			d.Devices = append(d.Devices, redirDevice(v))
		}
		for _, v := range dev.Inputs {
			d.Devices = append(d.Devices, inputDevice(v))
		}
		for _, v := range dev.Sounds {
			d.Devices = append(d.Devices, soundDevice(v))
		}
		for _, v := range dev.TPMs {
			d.Devices = append(d.Devices, tpmDevice(v))
		}
		for _, v := range dev.RNGs {
			d.Devices = append(d.Devices, rngDevice(v))
		}
		for _, v := range dev.Watchdogs {
			d.Devices = append(d.Devices, simpleDevice("watchdog", GroupSystem, "Watchdog", v.Model, v.Alias, v.Address,
				Prop{"Model", v.Model, "watchdog@model"}, Prop{"When it fires", v.Action, "watchdog@action"}))
		}
		for _, v := range dev.Panics {
			d.Devices = append(d.Devices, simpleDevice("panic", GroupSystem, "Panic notifier", v.Model, v.Alias, v.Address,
				Prop{"Model", v.Model, "panic@model"}))
		}
		if b := dev.MemBalloon; b != nil && b.Model != "none" {
			p := []Prop{{"Model", b.Model, "memballoon@model"}, {"Auto-deflate on OOM", b.AutoDeflate, "memballoon@autodeflate"},
				{"Free page reporting", b.FreePageReporting, "memballoon@freePageReporting"}}
			if b.Stats != nil {
				p = append(p, Prop{"Stats period", fmt.Sprintf("%d s", b.Stats.Period), "stats@period"})
			}
			d.Devices = append(d.Devices, simpleDevice("memballoon", GroupSystem, "Memory balloon", label(b.Model), b.Alias, b.Address, p...))
		}
		for _, v := range dev.Memorydevs {
			d.Devices = append(d.Devices, simpleDevice("memory", GroupSystem, "Memory module", v.Model, v.Alias, v.Address,
				Prop{"Model", v.Model, "memory@model"}))
		}
		if v := dev.VSock; v != nil {
			d.Devices = append(d.Devices, simpleDevice("vsock", GroupSystem, "VSOCK", v.Model, v.Alias, v.Address,
				Prop{"Model", v.Model, "vsock@model"}))
		}
		for _, v := range dev.IOMMUs {
			d.Devices = append(d.Devices, simpleDevice("iommu", GroupSystem, "Virtual IOMMU", v.Model, v.Alias, nil,
				Prop{"Model", v.Model, "iommu@model"}))
		}
		for _, v := range dev.Smartcards {
			d.Devices = append(d.Devices, simpleDevice("smartcard", GroupPassthrough, "Smartcard", "", v.Alias, v.Address))
		}
		for _, v := range dev.Hubs {
			d.Devices = append(d.Devices, simpleDevice("hub", GroupController, "USB hub", strings.ToUpper(v.Type), v.Alias, v.Address,
				Prop{"Type", v.Type, "hub@type"}))
		}
		for _, v := range dev.Controllers {
			d.Devices = append(d.Devices, controllerDevice(v))
		}
	}
	d.System.BootOrder = bootOrder(x, d.Devices)
	return d
}

func systemInfo(x *libvirtxml.Domain) SystemInfo {
	s := SystemInfo{
		Hypervisor: x.Type, Firmware: "bios", BootOrder: []string{}, Features: []string{},
		OnPoweroff: x.OnPoweroff, OnReboot: x.OnReboot, OnCrash: x.OnCrash,
	}
	if x.Devices != nil {
		s.Emulator = x.Devices.Emulator
	}
	if x.Clock != nil {
		s.Clock = x.Clock.Offset
		if x.Clock.TimeZone != "" {
			s.Clock += " (" + x.Clock.TimeZone + ")"
		}
	}
	if os := x.OS; os != nil {
		if os.Type != nil {
			s.OSType, s.Arch, s.Machine = os.Type.Type, os.Type.Arch, os.Type.Machine
		}
		// UEFI is either requested via firmware autoselection or configured as a pflash loader.
		if os.Firmware == "efi" || (os.Loader != nil && os.Loader.Type == "pflash") {
			s.Firmware = "uefi"
		}
		if os.Loader != nil {
			s.Loader = os.Loader.Path
			s.SecureBoot = os.Loader.Secure == "yes"
		}
		if os.FirmwareInfo != nil {
			for _, f := range os.FirmwareInfo.Features {
				if f.Name == "secure-boot" && f.Enabled == "yes" {
					s.SecureBoot = true
				}
			}
		}
		if os.NVRam != nil {
			s.NVRAM = os.NVRam.NVRam
			if s.NVRAM == "" && os.NVRam.Source != nil && os.NVRam.Source.File != nil {
				s.NVRAM = os.NVRam.Source.File.File
			}
		}
		s.Kernel, s.Cmdline = os.Kernel, os.Cmdline
		s.BootMenu = os.BootMenu != nil && os.BootMenu.Enable == "yes"
	}
	if f := x.Features; f != nil {
		on := func(name string, present bool) {
			if present {
				s.Features = append(s.Features, name)
			}
		}
		on("acpi", f.ACPI != nil)
		on("apic", f.APIC != nil)
		on("pae", f.PAE != nil)
		on("hyperv", f.HyperV != nil)
		on("kvm", f.KVM != nil)
		on("smm", f.SMM != nil && f.SMM.State != "off")
		on("vmport", f.VMPort != nil && f.VMPort.State == "on")
		on("ioapic", f.IOAPIC != nil)
		on("vmcoreinfo", f.VMCoreInfo != nil && f.VMCoreInfo.State != "off")
		on("pvspinlock", f.PVSpinlock != nil && f.PVSpinlock.State != "off")
		on("pmu", f.PMU != nil && f.PMU.State != "off")
	}
	return s
}

func cpuInfo(x *libvirtxml.Domain) CPUInfo {
	c := CPUInfo{Mode: "custom"}
	if v := x.VCPU; v != nil {
		c.MaxVCPUs, c.VCPUs, c.CPUSet = v.Value, v.Value, v.CPUSet
		if v.Current > 0 {
			c.VCPUs = v.Current
		}
	}
	if x.CPUTune != nil && len(x.CPUTune.VCPUPin) > 0 {
		c.Pinned = true
	}
	if cpu := x.CPU; cpu != nil {
		if cpu.Mode != "" {
			c.Mode = cpu.Mode
		}
		if cpu.Model != nil {
			c.Model = cpu.Model.Value
		}
		if t := cpu.Topology; t != nil {
			c.Sockets, c.Dies, c.Cores, c.Threads = t.Sockets, t.Dies, t.Cores, t.Threads
		}
		c.Features = len(cpu.Features)
		if cpu.Numa != nil {
			c.NUMANodes = len(cpu.Numa.Cell)
		}
	}
	if c.Sockets == 0 { // no <topology>: QEMU gives every vCPU its own socket
		c.Sockets, c.Cores, c.Threads = int(c.MaxVCPUs), 1, 1
	}
	return c
}

func memoryInfo(x *libvirtxml.Domain) MemoryInfo {
	var m MemoryInfo
	if x.Memory != nil {
		m.MaxKiB = toKiB(x.Memory.Value, x.Memory.Unit)
	}
	m.CurrentKiB = m.MaxKiB
	if x.CurrentMemory != nil {
		m.CurrentKiB = toKiB(x.CurrentMemory.Value, x.CurrentMemory.Unit)
	}
	if x.MaximumMemory != nil {
		m.HotplugKiB = toKiB(x.MaximumMemory.Value, x.MaximumMemory.Unit)
	}
	m.Hugepages = x.MemoryBacking != nil && x.MemoryBacking.MemoryHugePages != nil
	if x.Devices != nil && x.Devices.MemBalloon != nil && x.Devices.MemBalloon.Model != "none" {
		m.Balloon = x.Devices.MemBalloon.Model
	}
	return m
}

// toKiB converts a libvirt memory value; libvirt's default unit is KiB.
func toKiB(v uint, unit string) uint64 {
	n := uint64(v)
	switch strings.ToLower(unit) {
	case "b", "bytes":
		return n / 1024
	case "k", "kib", "":
		return n
	case "kb":
		return n * 1000 / 1024
	case "m", "mib":
		return n << 10
	case "mb":
		return n * 1000 * 1000 / 1024
	case "g", "gib":
		return n << 20
	case "gb":
		return n * 1000 * 1000 * 1000 / 1024
	case "t", "tib":
		return n << 30
	case "tb":
		return n * 1000 * 1000 * 1000 * 1000 / 1024
	}
	return n
}

func diskDevice(v libvirtxml.DomainDisk) Device {
	kind := v.Device
	if kind == "" {
		kind = "disk"
	}
	dev := Device{Kind: kind, Group: GroupStorage}
	var target, bus string
	if v.Target != nil {
		target, bus = v.Target.Dev, v.Target.Bus
	}
	dev.Target = target
	switch kind {
	case "cdrom":
		dev.Title = "CD/DVD drive"
	case "floppy":
		dev.Title = "Floppy drive"
	case "lun":
		dev.Title = "SCSI LUN"
	default:
		dev.Title = "Disk"
	}
	if target != "" {
		dev.Title += " " + target
	}

	source, sourceTech := diskSource(v.Source)
	var format string
	if v.Driver != nil {
		format = v.Driver.Type
	}
	switch {
	case source == "" && kind == "cdrom":
		dev.Summary = "Empty"
	case source == "":
		dev.Summary = "No source"
	default:
		dev.Summary = baseName(source)
	}
	dev.Summary = joinNonEmpty(" · ", label(bus), strings.ToUpper(format), dev.Summary)

	dev.Props = props(
		Prop{"Source", source, sourceTech},
		Prop{"Bus", label(bus), "target@bus"},
		Prop{"Device name in guest", target, "target@dev"},
		Prop{"Image format", format, "driver@type"},
		Prop{"Read-only", yesIf(v.ReadOnly != nil), "readonly"},
		Prop{"Shareable", yesIf(v.Shareable != nil), "shareable"},
	)
	if dr := v.Driver; dr != nil {
		dev.Props = append(dev.Props, props(
			Prop{"Cache mode", dr.Cache, "driver@cache"},
			Prop{"I/O mode", dr.IO, "driver@io"},
			Prop{"Discard (TRIM)", dr.Discard, "driver@discard"},
			Prop{"Detect zeroes", dr.DetectZeros, "driver@detect_zeroes"},
			Prop{"Error policy", dr.ErrorPolicy, "driver@error_policy"},
		)...)
		if dr.Queues != nil {
			dev.Props = append(dev.Props, Prop{"Queues", fmt.Sprint(*dr.Queues), "driver@queues"})
		}
	}
	if v.Target != nil && v.Target.Tray != "" {
		dev.Props = append(dev.Props, Prop{"Tray", v.Target.Tray, "target@tray"})
	}
	dev.Props = append(dev.Props, props(Prop{"Serial", v.Serial, "serial"})...)
	if v.BackingStore != nil && v.BackingStore.Source != nil {
		if s, tech := diskSource(v.BackingStore.Source); s != "" {
			dev.Props = append(dev.Props, Prop{"Backing image", s, "backingStore/" + tech})
		}
	}
	if v.Boot != nil {
		dev.Boot = v.Boot.Order
	}
	dev.Props = append(dev.Props, common(v.Alias, v.Address)...)
	return dev
}

func diskSource(s *libvirtxml.DomainDiskSource) (string, string) {
	switch {
	case s == nil:
		return "", ""
	case s.File != nil:
		return s.File.File, "source@file"
	case s.Block != nil:
		return s.Block.Dev, "source@dev"
	case s.Volume != nil:
		return s.Volume.Pool + "/" + s.Volume.Volume, "source@pool/@volume"
	case s.Dir != nil:
		return s.Dir.Dir, "source@dir"
	case s.Network != nil:
		n := s.Network
		host := ""
		if len(n.Hosts) > 0 {
			host = n.Hosts[0].Name
			if n.Hosts[0].Port != "" {
				host += ":" + n.Hosts[0].Port
			}
		}
		return fmt.Sprintf("%s://%s/%s", n.Protocol, host, strings.TrimPrefix(n.Name, "/")), "source@protocol/@name"
	case s.NVME != nil:
		return "NVMe", "source"
	}
	return "", ""
}

func filesystemDevice(v libvirtxml.DomainFilesystem) Device {
	var src, dst string
	if v.Source != nil && v.Source.Mount != nil {
		src = v.Source.Mount.Dir
	}
	if v.Target != nil {
		dst = v.Target.Dir
	}
	driver := ""
	if v.Driver != nil {
		driver = v.Driver.Type
	}
	dev := Device{Kind: "filesystem", Group: GroupStorage, Title: "Shared folder", Summary: joinNonEmpty(" → ", src, dst), Target: dst}
	dev.Props = props(
		Prop{"Host directory", src, "source@dir"},
		Prop{"Mount tag in guest", dst, "target@dir"},
		Prop{"Driver", driver, "driver@type"},
		Prop{"Access mode", v.AccessMode, "filesystem@accessmode"},
		Prop{"Read-only", yesIf(v.ReadOnly != nil), "readonly"},
	)
	dev.Props = append(dev.Props, common(v.Alias, v.Address)...)
	return dev
}

func interfaceDevice(v libvirtxml.DomainInterface) Device {
	dev := Device{Kind: "interface", Group: GroupNetwork, Title: "Network adapter"}
	var mac, model string
	if v.MAC != nil {
		mac = strings.ToLower(v.MAC.Address)
	}
	if v.Model != nil {
		model = v.Model.Type
	}
	dev.MAC = mac

	var conn, connTech string
	if s := v.Source; s != nil {
		switch {
		case s.Network != nil:
			conn, connTech = "Virtual network "+s.Network.Network, "source@network"
		case s.Bridge != nil:
			conn, connTech = "Bridge "+s.Bridge.Bridge, "source@bridge"
		case s.Direct != nil:
			conn, connTech = fmt.Sprintf("Direct (macvtap) on %s, %s mode", s.Direct.Dev, s.Direct.Mode), "source@dev/@mode"
		case s.User != nil:
			conn, connTech = "User-mode networking (no host setup)", "interface@type=user"
		case s.Hostdev != nil:
			conn, connTech = "SR-IOV host device", "interface@type=hostdev"
		case s.VHostUser != nil:
			conn, connTech = "vhost-user socket", "interface@type=vhostuser"
		case s.Ethernet != nil:
			conn, connTech = "Generic ethernet (tap)", "interface@type=ethernet"
		case s.Null != nil:
			conn, connTech = "Not connected", "interface@type=null"
		}
	}
	dev.Summary = joinNonEmpty(" · ", conn, label(model))
	linkDown := v.Link != nil && v.Link.State == "down"
	dev.Props = props(
		Prop{"Connected to", conn, connTech},
		Prop{"MAC address", mac, "mac@address"},
		Prop{"Adapter model", label(model), "model@type"},
	)
	if linkDown {
		dev.Props = append(dev.Props, Prop{"Link", "down (cable unplugged)", "link@state"})
	}
	if v.Target != nil {
		dev.Props = append(dev.Props, props(Prop{"Host interface", v.Target.Dev, "target@dev"})...)
	}
	if v.MTU != nil {
		dev.Props = append(dev.Props, Prop{"MTU", fmt.Sprint(v.MTU.Size), "mtu@size"})
	}
	if v.Driver != nil && v.Driver.Queues > 0 {
		dev.Props = append(dev.Props, Prop{"Queues", fmt.Sprint(v.Driver.Queues), "driver@queues"})
	}
	if v.FilterRef != nil {
		dev.Props = append(dev.Props, props(Prop{"Network filter", v.FilterRef.Filter, "filterref@filter"})...)
	}
	if v.Boot != nil {
		dev.Boot = v.Boot.Order
	}
	dev.Props = append(dev.Props, common(v.Alias, v.Address)...)
	return dev
}

func graphicsDevice(v libvirtxml.DomainGraphic) Device {
	dev := Device{Kind: "graphics", Group: GroupDisplay}
	listen := func(addr string, ls []libvirtxml.DomainGraphicListener) string {
		for _, l := range ls {
			switch {
			case l.Address != nil && l.Address.Address != "":
				return l.Address.Address
			case l.Network != nil:
				return joinNonEmpty(" ", l.Network.Address, "(network "+l.Network.Network+")")
			case l.Socket != nil:
				return "UNIX socket"
			}
		}
		return addr
	}
	port := func(p int, auto string) string {
		switch {
		case p > 0:
			return fmt.Sprint(p)
		case auto == "yes" || p == -1:
			return "automatic"
		}
		return ""
	}
	switch {
	case v.VNC != nil:
		g := v.VNC
		dev.Title = "Display (VNC)"
		dev.Summary = joinNonEmpty(" · ", "port "+port(g.Port, g.AutoPort), g.Keymap)
		dev.Props = props(
			Prop{"Protocol", "VNC", "graphics@type"},
			Prop{"Port", port(g.Port, g.AutoPort), "graphics@port"},
			Prop{"Listen address", listen(g.Listen, g.Listeners), "listen@address"},
			Prop{"Keyboard layout", g.Keymap, "graphics@keymap"},
			Prop{"Password protected", yesIf(g.Passwd != ""), "graphics@passwd"},
		)
		if g.WebSocket > 0 {
			dev.Props = append(dev.Props, Prop{"WebSocket port", fmt.Sprint(g.WebSocket), "graphics@websocket"})
		}
	case v.Spice != nil:
		g := v.Spice
		dev.Title = "Display (SPICE)"
		dev.Summary = joinNonEmpty(" · ", "port "+port(g.Port, g.AutoPort), g.Keymap)
		dev.Props = props(
			Prop{"Protocol", "SPICE", "graphics@type"},
			Prop{"Port", port(g.Port, g.AutoPort), "graphics@port"},
			Prop{"TLS port", port(g.TLSPort, ""), "graphics@tlsPort"},
			Prop{"Listen address", listen(g.Listen, g.Listeners), "listen@address"},
			Prop{"Keyboard layout", g.Keymap, "graphics@keymap"},
			Prop{"Password protected", yesIf(g.Passwd != ""), "graphics@passwd"},
		)
		if g.GL != nil && g.GL.Enable == "yes" {
			dev.Props = append(dev.Props, Prop{"OpenGL", "enabled", "gl@enable"})
		}
	case v.RDP != nil:
		dev.Title, dev.Props = "Display (RDP)", []Prop{{"Protocol", "RDP", "graphics@type"}}
	case v.EGLHeadless != nil:
		dev.Title, dev.Props = "Display (EGL headless)", []Prop{{"Protocol", "egl-headless", "graphics@type"}}
	case v.DBus != nil:
		dev.Title, dev.Props = "Display (D-Bus)", []Prop{{"Protocol", "dbus", "graphics@type"}}
	case v.SDL != nil:
		dev.Title, dev.Props = "Display (SDL window)", []Prop{{"Protocol", "sdl", "graphics@type"}}
	default:
		dev.Title, dev.Props = "Display", []Prop{}
	}
	return dev
}

func videoDevice(v libvirtxml.DomainVideo) Device {
	m := v.Model
	dev := Device{Kind: "video", Group: GroupDisplay, Title: "Video card", Summary: label(m.Type)}
	dev.Props = props(Prop{"Model", label(m.Type), "model@type"})
	if m.Heads > 0 {
		dev.Props = append(dev.Props, Prop{"Monitors", fmt.Sprint(m.Heads), "model@heads"})
	}
	if m.VRam > 0 {
		dev.Props = append(dev.Props, Prop{"Video memory", fmt.Sprintf("%d KiB", m.VRam), "model@vram"})
	}
	if m.Accel != nil && m.Accel.Accel3D == "yes" {
		dev.Props = append(dev.Props, Prop{"3D acceleration", "enabled", "acceleration@accel3d"})
	}
	if m.Primary == "yes" {
		dev.Props = append(dev.Props, Prop{"Primary", "yes", "model@primary"})
	}
	dev.Props = append(dev.Props, common(v.Alias, v.Address)...)
	return dev
}

func chardevDevice(kind, title string, src *libvirtxml.DomainChardevSource, targetType string, port *uint, alias *libvirtxml.DomainAlias) Device {
	if port != nil {
		title += fmt.Sprintf(" %d", *port)
	}
	conn, tech := chardevSource(src)
	dev := Device{Kind: kind, Group: GroupConsole, Title: title, Summary: joinNonEmpty(" · ", targetType, conn)}
	dev.Props = props(
		Prop{"Guest side", targetType, "target@type"},
		Prop{"Host side", conn, tech},
	)
	dev.Props = append(dev.Props, common(alias, nil)...)
	return dev
}

func chardevSource(s *libvirtxml.DomainChardevSource) (string, string) {
	switch {
	case s == nil:
		return "", ""
	case s.Pty != nil:
		if s.Pty.Path != "" {
			return "pseudo-terminal " + s.Pty.Path, "source@path"
		}
		return "pseudo-terminal", "@type=pty"
	case s.UNIX != nil:
		return "UNIX socket " + s.UNIX.Path, "source@path"
	case s.TCP != nil:
		return fmt.Sprintf("TCP %s:%s (%s)", s.TCP.Host, s.TCP.Service, s.TCP.Mode), "source@host/@service"
	case s.File != nil:
		return "file " + s.File.Path, "source@path"
	case s.Dev != nil:
		return "host device " + s.Dev.Path, "source@path"
	case s.SpiceVMC != nil:
		return "SPICE agent channel", "@type=spicevmc"
	case s.SpicePort != nil:
		return "SPICE port " + s.SpicePort.Channel, "source@channel"
	case s.QEMUVDAgent != nil:
		return "QEMU vdagent", "@type=qemu-vdagent"
	case s.Null != nil:
		return "discarded", "@type=null"
	}
	return "", ""
}

func consoleTargetType(t *libvirtxml.DomainConsoleTarget) string {
	if t == nil {
		return ""
	}
	return t.Type
}

func consolePort(t *libvirtxml.DomainConsoleTarget) *uint {
	if t == nil {
		return nil
	}
	return t.Port
}

func serialTargetType(t *libvirtxml.DomainSerialTarget) string {
	if t == nil {
		return ""
	}
	return t.Type
}

func serialPort(t *libvirtxml.DomainSerialTarget) *uint {
	if t == nil {
		return nil
	}
	return t.Port
}

func channelDevice(v libvirtxml.DomainChannel) Device {
	conn, tech := chardevSource(v.Source)
	dev := Device{Kind: "channel", Group: GroupConsole, Title: "Channel"}
	name, state := "", ""
	if t := v.Target; t != nil && t.VirtIO != nil {
		name, state = t.VirtIO.Name, t.VirtIO.State
	}
	switch name {
	case "org.qemu.guest_agent.0":
		dev.Title = "QEMU guest agent"
	case "com.redhat.spice.0":
		dev.Title = "SPICE agent"
	default:
		if name != "" {
			dev.Title = "Channel " + name
		}
	}
	switch state {
	case "connected":
		dev.Summary = "Connected"
	case "disconnected":
		dev.Summary = "Not connected"
	}
	if isVDAgent(v) {
		dev.Kind, dev.Title = "clipboard", "Clipboard sharing"
	}
	dev.Props = props(
		Prop{"Name", name, "target@name"},
		Prop{"Agent state", state, "target@state"},
		Prop{"Host side", conn, tech},
	)
	dev.Props = append(dev.Props, common(v.Alias, v.Address)...)
	return dev
}

func hostdevDevice(v libvirtxml.DomainHostdev) Device {
	dev := Device{Kind: "hostdev", Group: GroupPassthrough, Title: "Host device"}
	switch {
	case v.SubsysPCI != nil:
		dev.Kind, dev.Title = "hostdev-pci", "PCI device"
		if s := v.SubsysPCI.Source; s != nil && s.Address != nil {
			dev.Summary = pciAddr(s.Address)
			dev.Target = dev.Summary
			dev.Props = append(dev.Props, Prop{"Host PCI address", dev.Summary, "source/address"})
		}
	case v.SubsysUSB != nil:
		dev.Kind, dev.Title = "hostdev-usb", "USB device"
		if s := v.SubsysUSB.Source; s != nil {
			if s.Vendor != nil && s.Product != nil {
				dev.Summary = strings.TrimPrefix(s.Vendor.ID, "0x") + ":" + strings.TrimPrefix(s.Product.ID, "0x")
				dev.Target = usbKey(s.Vendor.ID, s.Product.ID)
				dev.Props = append(dev.Props, Prop{"Vendor:product", dev.Summary, "source/vendor@id,product@id"})
			}
			if s.Address != nil && s.Address.Bus != nil && s.Address.Device != nil {
				dev.Props = append(dev.Props, Prop{"Host bus/device", fmt.Sprintf("%03d/%03d", *s.Address.Bus, *s.Address.Device), "source/address"})
			}
		}
	case v.SubsysMDev != nil:
		dev.Kind, dev.Title = "hostdev-mdev", "Mediated device (vGPU)"
		dev.Summary = v.SubsysMDev.Model
		if s := v.SubsysMDev.Source; s != nil && s.Address != nil {
			dev.Target = strings.ToLower(s.Address.UUID)
			dev.Props = append(dev.Props, Prop{"Device UUID", dev.Target, "source/address@uuid"})
		}
	case v.SubsysSCSI != nil:
		dev.Kind, dev.Title = "hostdev-scsi", "SCSI device"
	}
	dev.Props = append(dev.Props, props(Prop{"Managed by libvirt", v.Managed, "hostdev@managed"})...)
	if v.Boot != nil {
		dev.Boot = v.Boot.Order
	}
	dev.Props = append(dev.Props, common(v.Alias, v.Address)...)
	return dev
}

func redirDevice(v libvirtxml.DomainRedirDev) Device {
	conn, tech := chardevSource(v.Source)
	dev := Device{Kind: "redirdev", Group: GroupPassthrough, Title: "USB redirection", Summary: conn}
	dev.Props = props(Prop{"Bus", label(v.Bus), "redirdev@bus"}, Prop{"Via", conn, tech})
	dev.Props = append(dev.Props, common(v.Alias, v.Address)...)
	return dev
}

func inputDevice(v libvirtxml.DomainInput) Device {
	title := map[string]string{"mouse": "Mouse", "tablet": "Tablet (absolute pointer)", "keyboard": "Keyboard", "passthrough": "Input passthrough", "evdev": "evdev input"}[v.Type]
	if title == "" {
		title = "Input " + v.Type
	}
	dev := Device{Kind: "input", Group: GroupInput, Title: title, Summary: label(v.Bus)}
	dev.Props = props(Prop{"Type", v.Type, "input@type"}, Prop{"Bus", label(v.Bus), "input@bus"}, Prop{"Model", v.Model, "input@model"})
	dev.Props = append(dev.Props, common(v.Alias, v.Address)...)
	return dev
}

func soundDevice(v libvirtxml.DomainSound) Device {
	dev := Device{Kind: "sound", Group: GroupInput, Title: "Sound card", Summary: label(v.Model)}
	dev.Props = props(Prop{"Model", label(v.Model), "sound@model"})
	dev.Props = append(dev.Props, common(v.Alias, v.Address)...)
	return dev
}

func tpmDevice(v libvirtxml.DomainTPM) Device {
	dev := Device{Kind: "tpm", Group: GroupSystem, Title: "TPM"}
	backend := ""
	version := ""
	if b := v.Backend; b != nil {
		switch {
		case b.Emulator != nil:
			backend, version = "emulated (swtpm)", b.Emulator.Version
		case b.Passthrough != nil:
			backend = "host TPM passthrough"
		case b.External != nil:
			backend = "external swtpm"
		}
	}
	if version != "" {
		dev.Title = "TPM " + version
	}
	dev.Summary = joinNonEmpty(" · ", v.Model, backend)
	dev.Props = props(Prop{"Model", v.Model, "tpm@model"}, Prop{"Backend", backend, "backend@type"}, Prop{"Version", version, "backend@version"})
	dev.Props = append(dev.Props, common(v.Alias, v.Address)...)
	return dev
}

func rngDevice(v libvirtxml.DomainRNG) Device {
	src := ""
	if b := v.Backend; b != nil {
		switch {
		case b.Random != nil:
			src = b.Random.Device
		case b.BuiltIn != nil:
			src = "built-in"
		case b.EGD != nil:
			src = "EGD daemon"
		}
	}
	dev := Device{Kind: "rng", Group: GroupSystem, Title: "Random number generator", Summary: joinNonEmpty(" · ", label(v.Model), src)}
	dev.Props = props(Prop{"Model", label(v.Model), "rng@model"}, Prop{"Entropy source", src, "backend"})
	dev.Props = append(dev.Props, common(v.Alias, v.Address)...)
	return dev
}

func controllerDevice(v libvirtxml.DomainController) Device {
	idx := ""
	if v.Index != nil {
		idx = fmt.Sprint(*v.Index)
	}
	title := map[string]string{
		"pci": "PCI controller", "usb": "USB controller", "sata": "SATA controller", "scsi": "SCSI controller",
		"ide": "IDE controller", "virtio-serial": "VirtIO serial controller", "fdc": "Floppy controller",
		"ccid": "Smartcard controller", "nvme": "NVMe controller", "xenbus": "XenBus controller", "isa": "ISA controller",
	}[v.Type]
	if title == "" {
		title = v.Type + " controller"
	}
	dev := Device{Kind: "controller", Group: GroupController, Title: title + " " + idx, Summary: v.Model}
	dev.Props = props(Prop{"Type", v.Type, "controller@type"}, Prop{"Index", idx, "controller@index"}, Prop{"Model", v.Model, "controller@model"})
	dev.Props = append(dev.Props, common(v.Alias, v.Address)...)
	return dev
}

func simpleDevice(kind string, group DeviceGroup, title, summary string, alias *libvirtxml.DomainAlias, addr *libvirtxml.DomainAddress, p ...Prop) Device {
	dev := Device{Kind: kind, Group: group, Title: title, Summary: summary, Props: props(p...)}
	dev.Props = append(dev.Props, common(alias, addr)...)
	return dev
}

// bootOrder lists the boot devices: per-device <boot order=…/> wins over <os><boot dev=…/>.
func bootOrder(x *libvirtxml.Domain, devs []Device) []string {
	var ordered []Device
	for _, d := range devs {
		if d.Boot > 0 {
			ordered = append(ordered, d)
		}
	}
	if len(ordered) > 0 {
		slices.SortStableFunc(ordered, func(a, b Device) int { return int(a.Boot) - int(b.Boot) })
		out := make([]string, len(ordered))
		for i, d := range ordered {
			out[i] = d.Title
		}
		return out
	}
	out := []string{}
	if x.OS == nil {
		return out
	}
	names := map[string]string{"hd": "Hard disk", "cdrom": "CD/DVD", "network": "Network (PXE)", "fd": "Floppy"}
	for _, b := range x.OS.BootDevices {
		if n, ok := names[b.Dev]; ok {
			out = append(out, n)
		} else {
			out = append(out, b.Dev)
		}
	}
	return out
}

// common adds the libvirt alias and guest bus address, useful for matching with QEMU/guest views.
func common(alias *libvirtxml.DomainAlias, addr *libvirtxml.DomainAddress) []Prop {
	var p []Prop
	if a := guestAddr(addr); a != "" {
		p = append(p, Prop{"Guest address", a, "address"})
	}
	if alias != nil && alias.Name != "" {
		p = append(p, Prop{"Alias", alias.Name, "alias@name"})
	}
	return p
}

func guestAddr(a *libvirtxml.DomainAddress) string {
	switch {
	case a == nil:
		return ""
	case a.PCI != nil:
		return "PCI " + pciAddr(a.PCI)
	case a.Drive != nil:
		d := a.Drive
		return fmt.Sprintf("drive %d:%d:%d", deref(d.Controller), deref(d.Bus), deref(d.Unit))
	case a.USB != nil:
		return fmt.Sprintf("USB bus %d port %s", deref(a.USB.Bus), a.USB.Port)
	case a.VirtioSerial != nil:
		v := a.VirtioSerial
		return fmt.Sprintf("virtio-serial %d:%d:%d", deref(v.Controller), deref(v.Bus), deref(v.Port))
	case a.ISA != nil:
		return "ISA"
	}
	return ""
}

func pciAddr(a *libvirtxml.DomainAddressPCI) string {
	return fmt.Sprintf("%04x:%02x:%02x.%x", deref(a.Domain), deref(a.Bus), deref(a.Slot), deref(a.Function))
}

func deref(p *uint) uint {
	if p == nil {
		return 0
	}
	return *p
}

// props drops entries without a value, so the UI only shows what is configured.
func props(p ...Prop) []Prop {
	out := make([]Prop, 0, len(p))
	for _, x := range p {
		if x.Value != "" {
			out = append(out, x)
		}
	}
	return out
}

func yesIf(b bool) string {
	if b {
		return "yes"
	}
	return ""
}

// label prettifies common libvirt model/bus names; unknown values pass through unchanged.
func label(s string) string {
	if l, ok := labels[s]; ok {
		return l
	}
	return s
}

var labels = map[string]string{
	"virtio": "VirtIO", "virtio-non-transitional": "VirtIO", "virtio-transitional": "VirtIO (transitional)",
	"sata": "SATA", "scsi": "SCSI", "ide": "IDE", "usb": "USB", "nvme": "NVMe", "fdc": "Floppy", "xen": "Xen", "sd": "SD card",
	"ps2": "PS/2", "e1000": "Intel e1000", "e1000e": "Intel e1000e", "rtl8139": "Realtek RTL8139", "vmxnet3": "VMware vmxnet3",
	"qxl": "QXL", "vga": "VGA", "cirrus": "Cirrus", "bochs": "Bochs", "ramfb": "ramfb", "none": "None",
	"ich6": "Intel HDA (ICH6)", "ich9": "Intel HDA (ICH9)", "ac97": "AC97", "usb-audio": "USB audio",
}

func baseName(p string) string {
	if i := strings.LastIndexAny(p, "/"); i >= 0 && i < len(p)-1 {
		return p[i+1:]
	}
	return p
}

func joinNonEmpty(sep string, parts ...string) string {
	out := parts[:0:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}
