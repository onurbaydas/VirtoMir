// Package content manages the cluster-wide content library for VirtoMir (ADR-0063).
// This file implements zero-touch machine deployment from any library item
// (template, cloud image, ISO) on any cluster host without manual copying.
package content

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/digitalocean/go-libvirt"
	"github.com/onurbaydas/virtomir/internal/virt"
	"libvirt.org/go/libvirtxml"
)

// DeployRequest specifies parameters for deploying a new machine from a content library item.
type DeployRequest struct {
	ItemID    string          `json:"itemId"`
	VersionID string          `json:"versionId,omitempty"`
	Name      string          `json:"name"`
	Pool      string          `json:"pool"`
	Network   string          `json:"network,omitempty"`
	VCPUs     int             `json:"vcpus,omitempty"`
	MemoryMiB uint64          `json:"memoryMiB,omitempty"`
	DiskGiB   uint64          `json:"diskGiB,omitempty"`
	CloudInit *virt.CloudInit `json:"cloudInit,omitempty"`
	Start     bool            `json:"start"`
}

type deployHooks struct {
	nameTaken       func(ctx context.Context, client *virt.Client, name string) (bool, error)
	resolvePoolPath func(ctx context.Context, client *virt.Client, pool string) (string, error)
	refreshPool     func(ctx context.Context, client *virt.Client, pool string) error
	cloneVolume     func(ctx context.Context, client *virt.Client, srcPath, poolName, dstName, format string, minBytes uint64) (string, func(), error)
	createVolume    func(ctx context.Context, client *virt.Client, poolName, volName string, sizeBytes uint64, format string) (string, func(), error)
	uploadSeedISO   func(ctx context.Context, client *virt.Client, poolName, isoName string, data []byte) (string, func(), error)
	defineDomain    func(ctx context.Context, client *virt.Client, xmlDoc string) (string, any, error)
	startDomain     func(ctx context.Context, client *virt.Client, domObj any) error
}

type ctxDeployHooksKey struct{}

// WithDeployHooks attaches custom deployHooks to ctx for unit testing.
func WithDeployHooks(ctx context.Context, h *deployHooks) context.Context {
	return context.WithValue(ctx, ctxDeployHooksKey{}, h)
}

// HasDeployHooks reports whether deployHooks are attached to ctx.
func HasDeployHooks(ctx context.Context) bool {
	return ctx.Value(ctxDeployHooksKey{}) != nil
}

func deployHooksFromContext(ctx context.Context) *deployHooks {
	if val := ctx.Value(ctxDeployHooksKey{}); val != nil {
		if h, ok := val.(*deployHooks); ok {
			return h
		}
	}
	return nil
}

func checkNameExists(ctx context.Context, client *virt.Client, name string) (bool, error) {
	if h := deployHooksFromContext(ctx); h != nil && h.nameTaken != nil {
		return h.nameTaken(ctx, client, name)
	}
	if client == nil {
		return false, nil
	}
	return client.NameTaken(name)
}

func resolvePool(ctx context.Context, client *virt.Client, poolName string) (string, error) {
	if h := deployHooksFromContext(ctx); h != nil && h.resolvePoolPath != nil {
		return h.resolvePoolPath(ctx, client, poolName)
	}
	// A pool is looked up by name only: a request never names a folder of the host directly.
	if client == nil {
		return "", fmt.Errorf("libvirt client is required to find storage pool %q", poolName)
	}
	pools, err := client.ListPools()
	if err != nil {
		return "", fmt.Errorf("list storage pools: %w", err)
	}
	for _, p := range pools {
		if p.Name == poolName {
			if p.Path != "" {
				return p.Path, nil
			}
			return "", fmt.Errorf("storage pool %q has no filesystem path", poolName)
		}
	}
	return "", fmt.Errorf("storage pool %q not found", poolName)
}

func refreshPoolFn(ctx context.Context, client *virt.Client, poolName string) error {
	if h := deployHooksFromContext(ctx); h != nil && h.refreshPool != nil {
		return h.refreshPool(ctx, client, poolName)
	}
	if client != nil {
		return client.PoolAction(poolName, "refresh")
	}
	return nil
}

func cloneVol(ctx context.Context, client *virt.Client, srcPath, poolName, dstName, format string, minBytes uint64) (string, func(), error) {
	if h := deployHooksFromContext(ctx); h != nil && h.cloneVolume != nil {
		return h.cloneVolume(ctx, client, srcPath, poolName, dstName, format, minBytes)
	}
	if client == nil {
		return "", nil, errors.New("libvirt client is required to clone storage volumes")
	}
	vol, path, err := client.CopyVolumeInto(srcPath, poolName, dstName, format, minBytes)
	if err != nil {
		return "", nil, err
	}
	cleanup := func() {
		_ = client.DeleteStorageVol(vol)
		_ = os.Remove(path)
	}
	return path, cleanup, nil
}

func createEmptyVol(ctx context.Context, client *virt.Client, poolName, volName string, sizeBytes uint64, format string) (string, func(), error) {
	if h := deployHooksFromContext(ctx); h != nil && h.createVolume != nil {
		return h.createVolume(ctx, client, poolName, volName, sizeBytes, format)
	}
	if client == nil {
		return "", nil, errors.New("libvirt client is required to create storage volumes")
	}
	vol, path, err := client.CreateVolumeFile(poolName, volName, sizeBytes, format)
	if err != nil {
		return "", nil, err
	}
	cleanup := func() {
		_ = client.DeleteStorageVol(vol)
		_ = os.Remove(path)
	}
	return path, cleanup, nil
}

func uploadSeed(ctx context.Context, client *virt.Client, poolName, isoName string, data []byte) (string, func(), error) {
	if h := deployHooksFromContext(ctx); h != nil && h.uploadSeedISO != nil {
		return h.uploadSeedISO(ctx, client, poolName, isoName, data)
	}
	if client == nil {
		return "", nil, errors.New("libvirt client is required to upload cloud-init seed ISO")
	}
	vol, path, err := client.UploadBytes(poolName, isoName, data)
	if err != nil {
		return "", nil, err
	}
	cleanup := func() {
		_ = client.DeleteStorageVol(vol)
		_ = os.Remove(path)
	}
	return path, cleanup, nil
}

func defineDom(ctx context.Context, client *virt.Client, xmlDoc string) (string, any, error) {
	if h := deployHooksFromContext(ctx); h != nil && h.defineDomain != nil {
		return h.defineDomain(ctx, client, xmlDoc)
	}
	if client == nil {
		return "", nil, errors.New("libvirt client is required to define domain")
	}
	l, err := client.Conn()
	if err != nil {
		return "", nil, err
	}
	dom, err := l.DomainDefineXMLFlags(xmlDoc, libvirt.DomainDefineValidate)
	if err != nil {
		return "", nil, err
	}
	return virt.FormatUUID(dom.UUID), dom, nil
}

func startDom(ctx context.Context, client *virt.Client, domObj any) error {
	if h := deployHooksFromContext(ctx); h != nil && h.startDomain != nil {
		return h.startDomain(ctx, client, domObj)
	}
	if client == nil {
		return errors.New("libvirt client is required to start domain")
	}
	l, err := client.Conn()
	if err != nil {
		return err
	}
	if dom, ok := domObj.(libvirt.Domain); ok {
		return l.DomainCreate(dom)
	}
	return nil
}

func randomUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // Version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func randomMAC() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return fmt.Sprintf("52:54:00:%02x:%02x:%02x", b[0], b[1], b[2])
}

func isSeedISO(disk libvirtxml.DomainDisk) bool {
	return disk.Device == "cdrom" && disk.Source != nil && disk.Source.File != nil &&
		disk.Source.File.File != "" && strings.HasSuffix(disk.Source.File.File, "-cidata.iso")
}

func setSeed(x *libvirtxml.Domain, seed string) {
	if x.Devices == nil {
		x.Devices = &libvirtxml.DomainDeviceList{}
	}
	for i := range x.Devices.Disks {
		disk := &x.Devices.Disks[i]
		if isSeedISO(*disk) {
			if disk.Source == nil {
				disk.Source = &libvirtxml.DomainDiskSource{}
			}
			disk.Source.File = &libvirtxml.DomainDiskSourceFile{File: seed}
			return
		}
	}
	x.Devices.Disks = append(x.Devices.Disks, libvirtxml.DomainDisk{
		Device:   "cdrom",
		Driver:   &libvirtxml.DomainDiskDriver{Name: "qemu", Type: "raw"},
		Source:   &libvirtxml.DomainDiskSource{File: &libvirtxml.DomainDiskSourceFile{File: seed}},
		Target:   &libvirtxml.DomainDiskTarget{Dev: nextTarget(x, "sd"), Bus: "sata"},
		ReadOnly: &libvirtxml.DomainDiskReadOnly{},
	})
}

func nextTarget(x *libvirtxml.Domain, prefix string) string {
	used := map[string]bool{}
	if x.Devices != nil {
		for _, d := range x.Devices.Disks {
			if d.Target != nil {
				used[d.Target.Dev] = true
			}
		}
	}
	for i := 0; ; i++ {
		name := prefix + diskLetters(i)
		if !used[name] {
			return name
		}
	}
}

func diskLetters(i int) string {
	s := ""
	for i++; i > 0; i = (i - 1) / 26 {
		s = string(rune('a'+(i-1)%26)) + s
	}
	return s
}

// Deploy deploys a machine from any library item (template, cloud image, ISO) on any cluster host.
func Deploy(
	ctx context.Context,
	client *virt.Client,
	store *Store,
	peers PeerResolver,
	req DeployRequest,
	selfHostID string,
	report func(string),
) (string, error) {
	if report == nil {
		report = func(string) {}
	}

	// 1. Normalize and validate request
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return "", invalidf("machine name is required")
	}
	if err := virt.CheckName("Machine", req.Name); err != nil {
		return "", err
	}

	taken, err := checkNameExists(ctx, client, req.Name)
	if err != nil {
		return "", err
	}
	if taken {
		return "", conflictf("A machine named %s already exists.", req.Name)
	}

	if req.CloudInit != nil {
		if err := req.CloudInit.Normalize(req.Name); err != nil {
			return "", err
		}
	}

	// 2. Resolve Item and Version from store
	if store == nil {
		return "", invalidf("content store is required")
	}
	item, ok := store.GetItem(strings.TrimSpace(req.ItemID))
	if !ok {
		return "", notFoundf("item %q not found", req.ItemID)
	}
	if len(item.Versions) == 0 {
		return "", notFoundf("item %q has no versions", req.ItemID)
	}

	var version ItemVersion
	var foundVersion bool
	if strings.TrimSpace(req.VersionID) != "" {
		vID := strings.TrimSpace(req.VersionID)
		for _, v := range item.Versions {
			if v.ID == vID || v.Version == vID {
				version = v
				foundVersion = true
				break
			}
		}
		if !foundVersion {
			return "", notFoundf("version %q not found for item %q", req.VersionID, req.ItemID)
		}
	} else if lv := item.LatestVersion(); lv != nil {
		version = *lv
	}

	// 3. Check if version's volume file exists in the target pool (or host library pool)
	targetPool := strings.TrimSpace(req.Pool)
	if targetPool == "" {
		if cfg, ok := store.GetHostConfig(selfHostID); ok && strings.TrimSpace(cfg.TargetPool) != "" {
			targetPool = strings.TrimSpace(cfg.TargetPool)
		} else {
			targetPool = "default"
		}
	}
	targetPoolPath, err := resolvePool(ctx, client, targetPool)
	if err != nil {
		return "", err
	}

	var libPoolPath string
	var libPoolName string
	if cfg, ok := store.GetHostConfig(selfHostID); ok && strings.TrimSpace(cfg.TargetPool) != "" {
		libPoolName = strings.TrimSpace(cfg.TargetPool)
		libPoolPath, _ = resolvePool(ctx, client, libPoolName)
	}

	volFileName, err := FileName(item.Name, version.Version, version.Format)
	if err != nil {
		return "", err
	}
	var libraryVolumePath string

	candidates := []string{
		filepath.Join(targetPoolPath, volFileName),
	}
	if libPoolPath != "" && libPoolPath != targetPoolPath {
		candidates = append(candidates, filepath.Join(libPoolPath, volFileName))
	}
	if srcVol, err := VolFile(version.SourceVol); err == nil {
		candidates = append(candidates, filepath.Join(targetPoolPath, srcVol))
		if libPoolPath != "" && libPoolPath != targetPoolPath {
			candidates = append(candidates, filepath.Join(libPoolPath, srcVol))
		}
	}

	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			if version.Size <= 0 || st.Size() == version.Size {
				libraryVolumePath = p
				break
			}
		}
	}

	if libraryVolumePath != "" {
		report(fmt.Sprintf("Using local synced copy: %s", libraryVolumePath))
	} else {
		// Not present locally: pull from version.SourceHost
		if version.SourceHost == "" || version.SourceHost == selfHostID {
			return "", notFoundf("library volume for %s (v%s) is not present locally on host %s", item.Name, version.Version, selfHostID)
		}
		if peers == nil {
			return "", fmt.Errorf("cannot pull library image from %s: peer resolver is nil", version.SourceHost)
		}
		peer, ok := peers.PeerFor(version.SourceHost)
		if !ok || peer == nil {
			return "", fmt.Errorf("source host %q not found in cluster peers", version.SourceHost)
		}

		pullPoolPath := targetPoolPath
		pullPoolName := targetPool
		if libPoolPath != "" {
			pullPoolPath = libPoolPath
			pullPoolName = libPoolName
		}

		report(fmt.Sprintf("Syncing library image from host: %s", version.SourceHost))
		pulledPath, err := Pull(ctx, peer, item, version, pullPoolPath, func(msg string) {
			report(msg)
		})
		if err != nil {
			return "", fmt.Errorf("failed to sync library image from %s: %w", version.SourceHost, err)
		}
		libraryVolumePath = pulledPath

		if store != nil {
			_, _ = store.SetHostSyncState(item.ID, HostSyncState{
				Host:    selfHostID,
				State:   "synced",
				Updated: time.Now().UTC(),
			})
		}

		if err := refreshPoolFn(ctx, client, pullPoolName); err != nil {
			report(fmt.Sprintf("warning: failed to refresh storage pool %s: %v", pullPoolName, err))
		}
		if pullPoolName != targetPool {
			_ = refreshPoolFn(ctx, client, targetPool)
		}
	}

	// 4. Deploys based on item.Type
	var cleanups []func()
	rollback := func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			if cleanups[i] != nil {
				cleanups[i]()
			}
		}
	}

	var finalUUID string
	var finalDomObj any

	switch item.Type {
	case TypeTemplate:
		var minBytes uint64
		if req.DiskGiB > 0 {
			minBytes = req.DiskGiB << 30
		}
		dstVolName := fmt.Sprintf("%s-disk0.qcow2", req.Name)
		clonedPath, cleanupVol, err := cloneVol(ctx, client, libraryVolumePath, targetPool, dstVolName, "qcow2", minBytes)
		if err != nil {
			return "", err
		}
		cleanups = append(cleanups, cleanupVol)

		if strings.TrimSpace(version.TemplateXML) == "" {
			rollback()
			return "", invalidf("template version %q has no TemplateXML definition", version.Version)
		}

		var x libvirtxml.Domain
		if err := x.Unmarshal(version.TemplateXML); err != nil {
			rollback()
			return "", fmt.Errorf("parse template XML: %w", err)
		}
		SanitizeDomain(&x)

		newUUID := randomUUID()
		x.UUID = newUUID
		x.Name = req.Name
		if x.Title != "" {
			x.Title = req.Name
		}

		if req.VCPUs > 0 {
			x.VCPU = &libvirtxml.DomainVCPU{Placement: "static", Value: uint(req.VCPUs)}
		}
		if req.MemoryMiB > 0 {
			x.Memory = &libvirtxml.DomainMemory{Value: uint(req.MemoryMiB), Unit: "MiB"}
			x.CurrentMemory = &libvirtxml.DomainCurrentMemory{Value: uint(req.MemoryMiB), Unit: "MiB"}
		}

		if x.OS != nil && x.OS.NVRam != nil {
			x.OS.NVRam.NVRam = ""
			x.OS.NVRam.Source = nil
		}

		if x.Devices == nil {
			x.Devices = &libvirtxml.DomainDeviceList{}
		}

		// Assign fresh MAC address to interfaces
		for i := range x.Devices.Interfaces {
			x.Devices.Interfaces[i].MAC = &libvirtxml.DomainInterfaceMAC{Address: randomMAC()}
			x.Devices.Interfaces[i].Target = nil
			if req.Network != "" {
				if x.Devices.Interfaces[i].Source == nil {
					x.Devices.Interfaces[i].Source = &libvirtxml.DomainInterfaceSource{}
				}
				x.Devices.Interfaces[i].Source.Network = &libvirtxml.DomainInterfaceSourceNetwork{Network: req.Network}
			}
		}
		if len(x.Devices.Interfaces) == 0 && req.Network != "" {
			x.Devices.Interfaces = append(x.Devices.Interfaces, libvirtxml.DomainInterface{
				Source: &libvirtxml.DomainInterfaceSource{Network: &libvirtxml.DomainInterfaceSourceNetwork{Network: req.Network}},
				Model:  &libvirtxml.DomainInterfaceModel{Type: "virtio"},
				MAC:    &libvirtxml.DomainInterfaceMAC{Address: randomMAC()},
			})
		}

		// Update primary disk source file to the cloned disk path
		updatedDisk := false
		for i := range x.Devices.Disks {
			d := &x.Devices.Disks[i]
			if (d.Device == "" || d.Device == "disk") && d.ReadOnly == nil && d.Shareable == nil {
				d.Source = &libvirtxml.DomainDiskSource{File: &libvirtxml.DomainDiskSourceFile{File: clonedPath}}
				d.BackingStore = nil
				if d.Driver == nil {
					d.Driver = &libvirtxml.DomainDiskDriver{Name: "qemu", Type: "qcow2"}
				} else {
					d.Driver.Type = "qcow2"
				}
				updatedDisk = true
				break
			}
		}
		if !updatedDisk {
			x.Devices.Disks = append(x.Devices.Disks, libvirtxml.DomainDisk{
				Device: "disk",
				Driver: &libvirtxml.DomainDiskDriver{Name: "qemu", Type: "qcow2"},
				Source: &libvirtxml.DomainDiskSource{File: &libvirtxml.DomainDiskSourceFile{File: clonedPath}},
				Target: &libvirtxml.DomainDiskTarget{Dev: "vda", Bus: "virtio"},
			})
		}

		// Cloud-init seed ISO
		if req.CloudInit != nil {
			isoBytes, err := req.CloudInit.SeedISO()
			if err != nil {
				rollback()
				return "", fmt.Errorf("build cloud-init seed: %w", err)
			}
			seedVolName := fmt.Sprintf("%s-cidata.iso", req.Name)
			seedPath, cleanupSeed, err := uploadSeed(ctx, client, targetPool, seedVolName, isoBytes)
			if err != nil {
				rollback()
				return "", fmt.Errorf("upload cloud-init seed: %w", err)
			}
			cleanups = append(cleanups, cleanupSeed)
			setSeed(&x, seedPath)
		} else {
			// Remove any leftover seed ISO from the template definition so stale identity is not inherited
			filtered := make([]libvirtxml.DomainDisk, 0, len(x.Devices.Disks))
			for _, d := range x.Devices.Disks {
				if !isSeedISO(d) {
					filtered = append(filtered, d)
				}
			}
			x.Devices.Disks = filtered
		}

		xmlDoc, err := x.Marshal()
		if err != nil {
			rollback()
			return "", fmt.Errorf("marshal domain XML: %w", err)
		}

		createdUUID, domObj, err := defineDom(ctx, client, xmlDoc)
		if err != nil {
			rollback()
			return "", fmt.Errorf("define domain: %w", err)
		}
		if createdUUID == "" {
			createdUUID = newUUID
		}
		finalUUID = createdUUID
		finalDomObj = domObj

	case TypeImage:
		var sizeBytes uint64
		if req.DiskGiB > 0 {
			sizeBytes = req.DiskGiB << 30
		} else if version.Size > 0 {
			sizeBytes = uint64(version.Size)
		}
		dstVolName := fmt.Sprintf("%s.qcow2", req.Name)
		clonedPath, cleanupVol, err := cloneVol(ctx, client, libraryVolumePath, targetPool, dstVolName, "qcow2", sizeBytes)
		if err != nil {
			return "", err
		}
		cleanups = append(cleanups, cleanupVol)

		var seedPath string
		if req.CloudInit != nil {
			isoBytes, err := req.CloudInit.SeedISO()
			if err != nil {
				rollback()
				return "", fmt.Errorf("build cloud-init seed: %w", err)
			}
			seedVolName := fmt.Sprintf("%s-cidata.iso", req.Name)
			sp, cleanupSeed, err := uploadSeed(ctx, client, targetPool, seedVolName, isoBytes)
			if err != nil {
				rollback()
				return "", fmt.Errorf("upload cloud-init seed: %w", err)
			}
			cleanups = append(cleanups, cleanupSeed)
			seedPath = sp
		}

		vcpus := req.VCPUs
		if vcpus <= 0 {
			vcpus = 2
		}
		mem := req.MemoryMiB
		if mem <= 0 {
			mem = 2048
		}
		spec := virt.CreateSpec{
			Name:      req.Name,
			OS:        "linux",
			VCPUs:     vcpus,
			MemoryMiB: mem,
			Pool:      targetPool,
			DiskPath:  clonedPath,
		}
		if req.Network != "" {
			spec.Network = &virt.NICSpec{Network: req.Network}
		}

		x := virt.BuildDomain(spec, clonedPath, "qcow2", seedPath)
		newUUID := randomUUID()
		x.UUID = newUUID
		xmlDoc, err := x.Marshal()
		if err != nil {
			rollback()
			return "", fmt.Errorf("marshal domain XML: %w", err)
		}

		createdUUID, domObj, err := defineDom(ctx, client, xmlDoc)
		if err != nil {
			rollback()
			return "", fmt.Errorf("define domain: %w", err)
		}
		if createdUUID == "" {
			createdUUID = newUUID
		}
		finalUUID = createdUUID
		finalDomObj = domObj

	case TypeISO:
		diskGiB := req.DiskGiB
		if diskGiB <= 0 {
			diskGiB = 20
		}
		dstVolName := fmt.Sprintf("%s.qcow2", req.Name)
		emptyDiskPath, cleanupVol, err := createEmptyVol(ctx, client, targetPool, dstVolName, diskGiB<<30, "qcow2")
		if err != nil {
			return "", err
		}
		cleanups = append(cleanups, cleanupVol)

		var seedPath string
		if req.CloudInit != nil {
			isoBytes, err := req.CloudInit.SeedISO()
			if err != nil {
				rollback()
				return "", fmt.Errorf("build cloud-init seed: %w", err)
			}
			seedVolName := fmt.Sprintf("%s-cidata.iso", req.Name)
			sp, cleanupSeed, err := uploadSeed(ctx, client, targetPool, seedVolName, isoBytes)
			if err != nil {
				rollback()
				return "", fmt.Errorf("upload cloud-init seed: %w", err)
			}
			cleanups = append(cleanups, cleanupSeed)
			seedPath = sp
		}

		vcpus := req.VCPUs
		if vcpus <= 0 {
			vcpus = 2
		}
		mem := req.MemoryMiB
		if mem <= 0 {
			mem = 2048
		}
		spec := virt.CreateSpec{
			Name:      req.Name,
			OS:        "linux",
			VCPUs:     vcpus,
			MemoryMiB: mem,
			Pool:      targetPool,
			ISO:       libraryVolumePath, // attaches the library ISO volume as a bootable CD-ROM drive
			DiskPath:  emptyDiskPath,
		}
		if req.Network != "" {
			spec.Network = &virt.NICSpec{Network: req.Network}
		}

		x := virt.BuildDomain(spec, emptyDiskPath, "qcow2", seedPath)
		newUUID := randomUUID()
		x.UUID = newUUID
		xmlDoc, err := x.Marshal()
		if err != nil {
			rollback()
			return "", fmt.Errorf("marshal domain XML: %w", err)
		}

		createdUUID, domObj, err := defineDom(ctx, client, xmlDoc)
		if err != nil {
			rollback()
			return "", fmt.Errorf("define domain: %w", err)
		}
		if createdUUID == "" {
			createdUUID = newUUID
		}
		finalUUID = createdUUID
		finalDomObj = domObj

	default:
		return "", invalidf("unsupported item type %q for deployment", item.Type)
	}

	// 5. If req.Start is true: boots the created domain
	if req.Start && finalDomObj != nil {
		if err := startDom(ctx, client, finalDomObj); err != nil {
			report(fmt.Sprintf("%s was created but failed to start: %v", req.Name, err))
		}
	}

	return finalUUID, nil
}
