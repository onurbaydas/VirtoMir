package virt

import (
	"bytes"
	"fmt"

	"github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"
)

// CreateSpec is what the new-machine wizard collects. Everything else gets a sensible,
// modern default (q35, VirtIO where the guest supports it, UEFI), and can be changed later.
type CreateSpec struct {
	Name      string `json:"name"`
	OS        string `json:"os"` // linux (default), windows, other
	VCPUs     int    `json:"vcpus"`
	MemoryMiB uint64 `json:"memoryMiB"`
	// Firmware: uefi (default) or bios. Secure Boot and TPM default on for Windows.
	Firmware   string `json:"firmware"`
	SecureBoot bool   `json:"secureBoot"`
	TPM        bool   `json:"tpm"`
	// Disk: a new empty volume (Pool + DiskGiB), a copy of a cloud image grown to DiskGiB
	// (BaseImage), an existing image used as is (DiskPath), or none.
	Pool      string `json:"pool"`
	DiskGiB   uint64 `json:"diskGiB"`
	DiskPath  string `json:"diskPath,omitempty"`
	BaseImage string `json:"baseImage,omitempty"`
	// CloudInit configures a cloud image on first boot (seed ISO attached as a CD-ROM).
	CloudInit *CloudInit `json:"cloudInit,omitempty"`
	// Installation media to boot from (optional).
	ISO     string   `json:"iso,omitempty"`
	Network *NICSpec `json:"network,omitempty"` // nil: no network adapter
	Start   bool     `json:"start"`
}

// CreateResult reports the new machine; StartError is set when it was created but did not start.
type CreateResult struct {
	UUID       string `json:"uuid"`
	StartError string `json:"startError,omitempty"`
}

// osProfile is the device set a guest OS can use without extra drivers.
type osProfile struct {
	diskBus, nicModel, video string
	localClock, hyperv       bool
}

var profiles = map[string]osProfile{
	"linux": {diskBus: "virtio", nicModel: "virtio", video: "virtio"},
	// Windows installs without VirtIO drivers on SATA + e1000e; drivers can be added later.
	"windows": {diskBus: "sata", nicModel: "e1000e", video: "vga", localClock: true, hyperv: true},
	"other":   {diskBus: "sata", nicModel: "e1000e", video: "vga"},
}

func (s *CreateSpec) normalize() error {
	if err := checkName("Machine", s.Name); err != nil {
		return err
	}
	s.OS = orDefault(s.OS, "linux")
	if _, ok := profiles[s.OS]; !ok {
		return invalidf("Unknown operating system type %q.", s.OS)
	}
	s.Firmware = orDefault(s.Firmware, "uefi")
	if s.Firmware != "uefi" && s.Firmware != "bios" {
		return invalidf("Firmware must be uefi or bios.")
	}
	if s.Firmware == "bios" && s.SecureBoot {
		return invalidf("Secure Boot needs UEFI firmware.")
	}
	if s.VCPUs < 1 || s.VCPUs > 512 {
		return invalidf("Choose between 1 and 512 virtual CPUs.")
	}
	if s.MemoryMiB < 64 || s.MemoryMiB > 16<<20 {
		return invalidf("Memory must be between 64 MiB and 16 TiB.")
	}
	if s.DiskPath == "" && s.DiskGiB > 0 && s.Pool == "" {
		s.Pool = "default"
	}
	if s.DiskGiB > 64<<10 {
		return invalidf("Disk size must be at most 64 TiB.")
	}
	if s.DiskPath != "" && s.BaseImage != "" {
		return invalidf("Use either an existing disk or a cloud image, not both.")
	}
	if s.DiskPath == "" && s.BaseImage == "" && s.DiskGiB == 0 && s.ISO == "" {
		return invalidf("Give the machine a disk or installation media, otherwise it has nothing to boot.")
	}
	if s.CloudInit != nil {
		if err := s.CloudInit.normalize(s.Name); err != nil {
			return err
		}
	}
	return nil
}

// buildDomain returns the domain XML for a normalized spec. diskPath/diskFormat describe the
// prepared system disk ("" for none).
func buildDomain(s CreateSpec, diskPath, diskFormat, seedPath string) *libvirtxml.Domain {
	p := profiles[s.OS]
	x := &libvirtxml.Domain{
		Type:          "kvm",
		Name:          s.Name,
		Memory:        &libvirtxml.DomainMemory{Value: uint(s.MemoryMiB), Unit: "MiB"},
		CurrentMemory: &libvirtxml.DomainCurrentMemory{Value: uint(s.MemoryMiB), Unit: "MiB"},
		VCPU:          &libvirtxml.DomainVCPU{Placement: "static", Value: uint(s.VCPUs)},
		CPU:           &libvirtxml.DomainCPU{Mode: "host-passthrough", Check: "none", Migratable: "on"},
		OS: &libvirtxml.DomainOS{
			Type: &libvirtxml.DomainOSType{Arch: "x86_64", Machine: "q35", Type: "hvm"},
		},
		Features: &libvirtxml.DomainFeatureList{
			ACPI: &libvirtxml.DomainFeature{},
			APIC: &libvirtxml.DomainFeatureAPIC{},
		},
		Clock:      &libvirtxml.DomainClock{Offset: "utc"},
		OnPoweroff: "destroy", OnReboot: "restart", OnCrash: "destroy",
		PM: &libvirtxml.DomainPM{
			SuspendToMem:  &libvirtxml.DomainPMPolicy{Enabled: "no"},
			SuspendToDisk: &libvirtxml.DomainPMPolicy{Enabled: "no"},
		},
		Devices: &libvirtxml.DomainDeviceList{},
	}
	if s.Firmware == "uefi" {
		x.OS.Firmware = "efi"
		feats := []libvirtxml.DomainOSFirmwareFeature{{Name: "secure-boot", Enabled: "no"}}
		if s.SecureBoot {
			feats = []libvirtxml.DomainOSFirmwareFeature{{Name: "secure-boot", Enabled: "yes"}, {Name: "enrolled-keys", Enabled: "yes"}}
			x.Features.SMM = &libvirtxml.DomainFeatureSMM{State: "on"}
		}
		x.OS.FirmwareInfo = &libvirtxml.DomainOSFirmwareInfo{Features: feats}
	}
	if p.localClock {
		x.Clock.Offset = "localtime"
		x.Clock.Timer = []libvirtxml.DomainTimer{{Name: "rtc", TickPolicy: "catchup"}, {Name: "pit", TickPolicy: "delay"}, {Name: "hpet", Present: "no"}, {Name: "hypervclock", Present: "yes"}}
	}
	if p.hyperv {
		on := &libvirtxml.DomainFeatureState{State: "on"}
		x.Features.HyperV = &libvirtxml.DomainFeatureHyperV{
			Relaxed:   on,
			VAPIC:     on,
			Spinlocks: &libvirtxml.DomainFeatureHyperVSpinlocks{DomainFeatureState: libvirtxml.DomainFeatureState{State: "on"}, Retries: 8191},
		}
	}

	dev := x.Devices
	boot := uint(1)
	if s.ISO != "" {
		dev.Disks = append(dev.Disks, libvirtxml.DomainDisk{
			Device:   "cdrom",
			Driver:   &libvirtxml.DomainDiskDriver{Name: "qemu", Type: "raw"},
			Source:   &libvirtxml.DomainDiskSource{File: &libvirtxml.DomainDiskSourceFile{File: s.ISO}},
			Target:   &libvirtxml.DomainDiskTarget{Dev: "sda", Bus: "sata"},
			ReadOnly: &libvirtxml.DomainDiskReadOnly{},
			Boot:     &libvirtxml.DomainDeviceBoot{Order: boot},
		})
		boot++
	}
	if diskPath != "" {
		target := "vda"
		if p.diskBus != "virtio" {
			target = "sdb"
			if s.ISO == "" {
				target = "sda"
			}
		}
		driver := &libvirtxml.DomainDiskDriver{Name: "qemu", Type: orDefault(diskFormat, "raw"), Cache: "none", Discard: "unmap"}
		dev.Disks = append(dev.Disks, libvirtxml.DomainDisk{
			Device: "disk",
			Driver: driver,
			Source: &libvirtxml.DomainDiskSource{File: &libvirtxml.DomainDiskSourceFile{File: diskPath}},
			Target: &libvirtxml.DomainDiskTarget{Dev: target, Bus: p.diskBus},
			Boot:   &libvirtxml.DomainDeviceBoot{Order: boot},
		})
	}
	if seedPath != "" {
		// cloud-init finds its NoCloud seed by the volume label; it never boots from it.
		dev.Disks = append(dev.Disks, libvirtxml.DomainDisk{
			Device:   "cdrom",
			Driver:   &libvirtxml.DomainDiskDriver{Name: "qemu", Type: "raw"},
			Source:   &libvirtxml.DomainDiskSource{File: &libvirtxml.DomainDiskSourceFile{File: seedPath}},
			Target:   &libvirtxml.DomainDiskTarget{Dev: nextTarget(x, "sd"), Bus: "sata"},
			ReadOnly: &libvirtxml.DomainDiskReadOnly{},
		})
	}
	if s.Network != nil {
		spec := *s.Network
		spec.Model = orDefault(spec.Model, p.nicModel)
		if nic, err := buildNIC(spec); err == nil {
			dev.Interfaces = append(dev.Interfaces, nic)
		}
	}
	zero := uint(0)
	dev.Controllers = append(dev.Controllers, libvirtxml.DomainController{Type: "usb", Index: &zero, Model: "qemu-xhci"})
	// Spare PCIe root ports: on q35 every hot-plugged disk or adapter needs a free one.
	// Indexes are explicit: libvirt (11.x) reads a missing index as 0, which is pcie-root's.
	dev.Controllers = append(dev.Controllers, libvirtxml.DomainController{Type: "pci", Index: &zero, Model: "pcie-root"})
	for i := uint(1); i <= 8; i++ {
		dev.Controllers = append(dev.Controllers, libvirtxml.DomainController{Type: "pci", Index: &i, Model: "pcie-root-port"})
	}
	dev.Inputs = []libvirtxml.DomainInput{{Type: "tablet", Bus: "usb"}}
	// The console proxy in virtomird connects locally; VNC is never exposed directly.
	dev.Graphics = []libvirtxml.DomainGraphic{{VNC: &libvirtxml.DomainGraphicVNC{Port: -1, AutoPort: "yes", Listen: "127.0.0.1",
		Listeners: []libvirtxml.DomainGraphicListener{{Address: &libvirtxml.DomainGraphicListenerAddress{Address: "127.0.0.1"}}}}}}
	dev.Videos = []libvirtxml.DomainVideo{{Model: libvirtxml.DomainVideoModel{Type: p.video, Primary: "yes"}}}
	port := uint(0)
	dev.Serials = []libvirtxml.DomainSerial{{Source: &libvirtxml.DomainChardevSource{Pty: &libvirtxml.DomainChardevSourcePty{}}, Target: &libvirtxml.DomainSerialTarget{Port: &port}}}
	dev.Consoles = []libvirtxml.DomainConsole{{Source: &libvirtxml.DomainChardevSource{Pty: &libvirtxml.DomainChardevSourcePty{}}, Target: &libvirtxml.DomainConsoleTarget{Type: "serial", Port: &port}}}
	dev.Channels = []libvirtxml.DomainChannel{{
		Source: &libvirtxml.DomainChardevSource{UNIX: &libvirtxml.DomainChardevSourceUNIX{Mode: "bind"}},
		Target: &libvirtxml.DomainChannelTarget{VirtIO: &libvirtxml.DomainChannelTargetVirtIO{Name: "org.qemu.guest_agent.0"}},
	}}
	dev.RNGs = []libvirtxml.DomainRNG{{Model: "virtio", Backend: &libvirtxml.DomainRNGBackend{Random: &libvirtxml.DomainRNGBackendRandom{Device: "/dev/urandom"}}}}
	dev.MemBalloon = &libvirtxml.DomainMemBalloon{Model: "virtio", Stats: &libvirtxml.DomainMemBalloonStats{Period: 5}}
	if s.TPM {
		dev.TPMs = []libvirtxml.DomainTPM{{Model: "tpm-crb", Backend: &libvirtxml.DomainTPMBackend{Emulator: &libvirtxml.DomainTPMBackendEmulator{Version: "2.0"}}}}
	}
	return x
}

// CreateDomain creates the system disk, defines the machine and optionally starts it.
func (c *Client) CreateDomain(spec CreateSpec) (CreateResult, error) {
	if err := spec.normalize(); err != nil {
		return CreateResult{}, err
	}
	l, err := c.conn()
	if err != nil {
		return CreateResult{}, err
	}
	if _, err := l.DomainLookupByName(spec.Name); err == nil {
		return CreateResult{}, refusedf("A machine named %s already exists.", spec.Name)
	}

	diskPath, diskFormat := spec.DiskPath, ""
	var created []libvirt.StorageVol
	rollback := func() {
		for _, v := range created {
			_ = l.StorageVolDelete(v, libvirt.StorageVolDeleteNormal)
		}
	}
	switch {
	case spec.BaseImage != "":
		vol, p, err := copyVolumeInto(l, spec.BaseImage, spec.Pool, spec.Name+".qcow2", "qcow2", spec.DiskGiB<<30)
		if err != nil {
			return CreateResult{}, err
		}
		created, diskPath, diskFormat = append(created, vol), p, "qcow2"
	case diskPath == "" && spec.DiskGiB > 0:
		vol, p, err := createVolume(l, spec.Pool, spec.Name+".qcow2", spec.DiskGiB<<30, "qcow2")
		if err != nil {
			return CreateResult{}, err
		}
		created, diskPath, diskFormat = append(created, vol), p, "qcow2"
	case diskPath != "":
		if v, err := l.StorageVolLookupByPath(diskPath); err == nil {
			if info, err := volumeInfo(l, v); err == nil && info.Format != "iso" {
				diskFormat = info.Format
			}
		}
	}

	seedPath := ""
	if spec.CloudInit != nil {
		iso, err := spec.CloudInit.seedISO()
		if err != nil {
			rollback()
			return CreateResult{}, fmt.Errorf("build cloud-init seed: %w", err)
		}
		vol, p, err := uploadBytes(l, spec.Pool, spec.Name+"-cidata.iso", iso)
		if err != nil {
			rollback()
			return CreateResult{}, err
		}
		created, seedPath = append(created, vol), p
	}

	doc, err := buildDomain(spec, diskPath, diskFormat, seedPath).Marshal()
	if err != nil {
		rollback()
		return CreateResult{}, err
	}
	d, err := l.DomainDefineXMLFlags(doc, libvirt.DomainDefineValidate)
	if err != nil {
		rollback()
		return CreateResult{}, classify(err)
	}
	res := CreateResult{UUID: formatUUID(d.UUID)}
	c.log.Info("domain created", "domain", spec.Name, "os", spec.OS)
	if spec.Start {
		if err := l.DomainCreate(d); err != nil {
			res.StartError = classify(err).Error()
		}
	}
	return res, nil
}

// copyVolumeInto copies the pool volume at src into pool as name (converting to format) and
// grows it to minBytes if it is smaller. Used for cloud images.
func copyVolumeInto(l *libvirt.Libvirt, src, pool, name, format string, minBytes uint64) (libvirt.StorageVol, string, error) {
	srcVol, err := l.StorageVolLookupByPath(src)
	if err != nil {
		return libvirt.StorageVol{}, "", refusedf("%s is not in a storage pool. Upload the image in Storage first.", src)
	}
	_, srcCap, _, err := l.StorageVolGetInfo(srcVol)
	if err != nil {
		return libvirt.StorageVol{}, "", classify(err)
	}
	p, err := l.StoragePoolLookupByName(pool)
	if err != nil {
		return libvirt.StorageVol{}, "", classify(err)
	}
	if _, err := l.StorageVolLookupByName(p, name); err == nil {
		return libvirt.StorageVol{}, "", refusedf("Pool %s already has a volume named %s.", pool, name)
	}
	nv := libvirtxml.StorageVolume{
		Name:     name,
		Capacity: &libvirtxml.StorageVolumeSize{Unit: "bytes", Value: srcCap},
		Target:   &libvirtxml.StorageVolumeTarget{Format: &libvirtxml.StorageVolumeTargetFormat{Type: format}},
	}
	doc, err := nv.Marshal()
	if err != nil {
		return libvirt.StorageVol{}, "", err
	}
	vol, err := l.StorageVolCreateXMLFrom(p, doc, srcVol, 0)
	if err != nil {
		return libvirt.StorageVol{}, "", classify(err)
	}
	if minBytes > srcCap {
		if err := l.StorageVolResize(vol, minBytes, 0); err != nil {
			_ = l.StorageVolDelete(vol, libvirt.StorageVolDeleteNormal)
			return libvirt.StorageVol{}, "", classify(err)
		}
	}
	path, err := l.StorageVolGetPath(vol)
	if err != nil {
		_ = l.StorageVolDelete(vol, libvirt.StorageVolDeleteNormal)
		return libvirt.StorageVol{}, "", classify(err)
	}
	return vol, path, nil
}

// uploadBytes stores data as a new raw volume (e.g. a cloud-init seed ISO).
func uploadBytes(l *libvirt.Libvirt, pool, name string, data []byte) (libvirt.StorageVol, string, error) {
	vol, path, err := createVolume(l, pool, name, uint64(len(data)), "raw")
	if err != nil {
		return libvirt.StorageVol{}, "", err
	}
	if err := l.StorageVolUpload(vol, bytes.NewReader(data), 0, uint64(len(data)), 0); err != nil {
		_ = l.StorageVolDelete(vol, libvirt.StorageVolDeleteNormal)
		return libvirt.StorageVol{}, "", classify(err)
	}
	return vol, path, nil
}
