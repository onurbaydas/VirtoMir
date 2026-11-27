package content

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/onurbaydas/virtomir/internal/cluster"
	"github.com/onurbaydas/virtomir/internal/virt"
	"libvirt.org/go/libvirtxml"
)

const sampleTemplateXML = `<domain type='kvm'>
  <name>ubuntu-tpl</name>
  <uuid>aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee</uuid>
  <memory unit='MiB'>1024</memory>
  <currentMemory unit='MiB'>1024</currentMemory>
  <vcpu placement='static'>1</vcpu>
  <os firmware='efi'>
    <type arch='x86_64' machine='q35'>hvm</type>
    <nvram template='/usr/share/OVMF/OVMF_VARS.fd'>/var/lib/libvirt/qemu/nvram/ubuntu-tpl_VARS.fd</nvram>
  </os>
  <devices>
    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2'/>
      <source file='/old/storage/ubuntu-base.qcow2'/>
      <target dev='vda' bus='virtio'/>
    </disk>
    <interface type='network'>
      <mac address='52:54:00:11:22:33'/>
      <source network='default'/>
      <model type='virtio'/>
    </interface>
    <graphics type='vnc' port='5901' autoport='no'/>
  </devices>
</domain>`

// TestDeploy_LocalTemplate verifies:
// Deploy from local template creates domain with new UUID, updated disk path, and sanitized MAC.
func TestDeploy_LocalTemplate(t *testing.T) {
	store, err := Open("")
	if err != nil {
		t.Fatal(err)
	}

	poolDir := t.TempDir()
	selfHost := "node-local"

	if err := store.SetHostConfig(HostConfig{HostID: selfHost, TargetPool: poolDir}); err != nil {
		t.Fatal(err)
	}

	tplContent := []byte("template-disk-image-bytes-v1")
	tplPath := filepath.Join(poolDir, "content-ubuntu-tpl-v1.0.qcow2")
	if err := os.WriteFile(tplPath, tplContent, 0644); err != nil {
		t.Fatal(err)
	}

	item, err := store.CreateItem(Item{
		ID:   "item-tpl-1",
		Name: "ubuntu-tpl",
		Type: TypeTemplate,
		Versions: []ItemVersion{
			{
				ID:          "ver-1",
				Version:     "1.0",
				Format:      "qcow2",
				Size:        int64(len(tplContent)),
				SHA256:      sha256Hex(tplContent),
				TemplateXML: sampleTemplateXML,
				SourceHost:  selfHost,
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateItem failed: %v", err)
	}

	var definedXML string
	hooks := &deployHooks{
		resolvePoolPath: func(ctx context.Context, client *virt.Client, pool string) (string, error) {
			return poolDir, nil
		},
		cloneVolume: func(ctx context.Context, client *virt.Client, srcPath, poolName, dstName, format string, minBytes uint64) (string, func(), error) {
			dest := filepath.Join(poolDir, dstName)
			if err := os.WriteFile(dest, []byte("cloned-disk-data"), 0644); err != nil {
				return "", nil, err
			}
			cleanup := func() { _ = os.Remove(dest) }
			return dest, cleanup, nil
		},
		defineDomain: func(ctx context.Context, client *virt.Client, xmlDoc string) (string, any, error) {
			definedXML = xmlDoc
			var dom libvirtxml.Domain
			if err := dom.Unmarshal(xmlDoc); err != nil {
				return "", nil, err
			}
			return dom.UUID, nil, nil
		},
	}
	ctx := WithDeployHooks(context.Background(), hooks)

	req := DeployRequest{
		ItemID:    item.ID,
		Name:      "web-prod-01",
		Pool:      poolDir,
		VCPUs:     4,
		MemoryMiB: 4096,
		Network:   "production",
	}

	newUUID, err := Deploy(ctx, nil, store, nil, req, selfHost, nil)
	if err != nil {
		t.Fatalf("Deploy template failed: %v", err)
	}

	if newUUID == "" {
		t.Fatal("expected non-empty UUID")
	}
	if newUUID == "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" {
		t.Errorf("UUID was not regenerated, still matches original template UUID")
	}

	var parsed libvirtxml.Domain
	if err := parsed.Unmarshal(definedXML); err != nil {
		t.Fatalf("unmarshal defined XML failed: %v", err)
	}

	if parsed.Name != "web-prod-01" {
		t.Errorf("domain name = %q, want 'web-prod-01'", parsed.Name)
	}
	if parsed.VCPU == nil || parsed.VCPU.Value != 4 {
		t.Errorf("domain vcpu = %+v, want 4", parsed.VCPU)
	}
	if parsed.Memory == nil || parsed.Memory.Value != 4096 {
		t.Errorf("domain memory = %+v, want 4096", parsed.Memory)
	}

	// Check updated disk path
	expectedDiskPath := filepath.Join(poolDir, "web-prod-01-disk0.qcow2")
	if len(parsed.Devices.Disks) == 0 {
		t.Fatal("expected at least 1 disk in defined domain")
	}
	disk := parsed.Devices.Disks[0]
	if disk.Source == nil || disk.Source.File == nil || disk.Source.File.File != expectedDiskPath {
		t.Errorf("disk source file = %+v, want %q", disk.Source, expectedDiskPath)
	}

	// Check sanitized MAC: fresh MAC assigned, not 52:54:00:11:22:33
	if len(parsed.Devices.Interfaces) == 0 {
		t.Fatal("expected at least 1 network interface")
	}
	nic := parsed.Devices.Interfaces[0]
	if nic.MAC == nil {
		t.Fatal("interface has nil MAC")
	}
	if nic.MAC.Address == "52:54:00:11:22:33" {
		t.Errorf("interface still has original template MAC %q", nic.MAC.Address)
	}
	macPattern := regexp.MustCompile(`^52:54:00:[0-9a-fA-F]{2}:[0-9a-fA-F]{2}:[0-9a-fA-F]{2}$`)
	if !macPattern.MatchString(nic.MAC.Address) {
		t.Errorf("interface MAC %q does not match valid 52:54:00:xx:xx:xx format", nic.MAC.Address)
	}
	if nic.Source == nil || nic.Source.Network == nil || nic.Source.Network.Network != "production" {
		t.Errorf("interface network = %+v, want 'production'", nic.Source)
	}

	// Verify cloned file actually exists on disk
	if st, err := os.Stat(expectedDiskPath); err != nil || st.Size() == 0 {
		t.Errorf("expected cloned disk file %s to exist on disk", expectedDiskPath)
	}
}

// TestDeploy_RemoteItem_TriggersOnDemandPull verifies:
// Deploy from remote item triggers on-demand Pull before domain creation.
func TestDeploy_RemoteItem_TriggersOnDemandPull(t *testing.T) {
	store, err := Open("")
	if err != nil {
		t.Fatal(err)
	}

	poolDir := t.TempDir()
	selfHost := "node-local"
	remoteHost := "node-remote"

	if err := store.SetHostConfig(HostConfig{HostID: selfHost, TargetPool: poolDir}); err != nil {
		t.Fatal(err)
	}

	payload := []byte("remote-alpine-cloud-image-payload-data")
	payloadSHA := sha256Hex(payload)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(payload)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	peer := cluster.NewPeer(cluster.Node{ID: remoteHost, Address: server.URL}, server.Client())
	peers := MapPeerResolver{remoteHost: peer}

	item, err := store.CreateItem(Item{
		ID:   "item-remote-1",
		Name: "alpine-cloud",
		Type: TypeImage,
		Versions: []ItemVersion{
			{
				ID:         "ver-1",
				Version:    "3.20",
				Format:     "qcow2",
				Size:       int64(len(payload)),
				SHA256:     payloadSHA,
				SourceHost: remoteHost,
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateItem failed: %v", err)
	}

	// Verify target pool file does NOT exist yet
	expectedPulledPath := filepath.Join(poolDir, "content-alpine-cloud-v3.20.qcow2")
	if _, err := os.Stat(expectedPulledPath); err == nil {
		t.Fatalf("file %s should not exist prior to deploy", expectedPulledPath)
	}

	var reports []string
	reportFn := func(msg string) {
		reports = append(reports, msg)
	}

	poolRefreshed := false
	domainDefined := false
	hooks := &deployHooks{
		resolvePoolPath: func(ctx context.Context, client *virt.Client, pool string) (string, error) {
			return poolDir, nil
		},
		refreshPool: func(ctx context.Context, client *virt.Client, pool string) error {
			poolRefreshed = true
			return nil
		},
		cloneVolume: func(ctx context.Context, client *virt.Client, srcPath, poolName, dstName, format string, minBytes uint64) (string, func(), error) {
			dest := filepath.Join(poolDir, dstName)
			_ = os.WriteFile(dest, []byte("cloned-vm-disk"), 0644)
			return dest, func() { _ = os.Remove(dest) }, nil
		},
		defineDomain: func(ctx context.Context, client *virt.Client, xmlDoc string) (string, any, error) {
			domainDefined = true
			return "remote-vm-uuid-1234", nil, nil
		},
	}
	ctx := WithDeployHooks(context.Background(), hooks)

	req := DeployRequest{
		ItemID: item.ID,
		Name:   "alpine-worker",
		Pool:   poolDir,
	}

	uuid, err := Deploy(ctx, nil, store, peers, req, selfHost, reportFn)
	if err != nil {
		t.Fatalf("Deploy remote item failed: %v", err)
	}
	if uuid != "remote-vm-uuid-1234" {
		t.Errorf("UUID = %q, want 'remote-vm-uuid-1234'", uuid)
	}

	// Verify pull progress was reported
	foundSyncReport := false
	for _, r := range reports {
		if strings.Contains(r, "Syncing library image from host: node-remote") {
			foundSyncReport = true
			break
		}
	}
	if !foundSyncReport {
		t.Errorf("expected report about syncing from host node-remote, got: %v", reports)
	}

	// Verify the file was pulled and exists on disk
	if st, err := os.Stat(expectedPulledPath); err != nil || st.Size() != int64(len(payload)) {
		t.Errorf("expected pulled file %s with size %d, err: %v", expectedPulledPath, len(payload), err)
	}

	if !poolRefreshed {
		t.Errorf("expected target pool to be refreshed after pull")
	}
	if !domainDefined {
		t.Errorf("expected domain to be defined after pull")
	}
}

// TestDeploy_CloudImage_WithCloudInitSeedISO verifies:
// Deploy cloud image with CloudInit seed ISO attaches CD-ROM.
func TestDeploy_CloudImage_WithCloudInitSeedISO(t *testing.T) {
	store, err := Open("")
	if err != nil {
		t.Fatal(err)
	}

	poolDir := t.TempDir()
	selfHost := "node-local"

	imgContent := []byte("ubuntu-noble-base-image-data")
	imgPath := filepath.Join(poolDir, "content-ubuntu-noble-v24.04.qcow2")
	if err := os.WriteFile(imgPath, imgContent, 0644); err != nil {
		t.Fatal(err)
	}

	item, err := store.CreateItem(Item{
		ID:   "item-noble",
		Name: "ubuntu-noble",
		Type: TypeImage,
		Versions: []ItemVersion{
			{
				ID:         "v1",
				Version:    "24.04",
				Format:     "qcow2",
				Size:       int64(len(imgContent)),
				SHA256:     sha256Hex(imgContent),
				SourceHost: selfHost,
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateItem failed: %v", err)
	}

	var definedXML string
	var seedUploaded bool
	var seedPathCreated string

	hooks := &deployHooks{
		resolvePoolPath: func(ctx context.Context, client *virt.Client, pool string) (string, error) {
			return poolDir, nil
		},
		cloneVolume: func(ctx context.Context, client *virt.Client, srcPath, poolName, dstName, format string, minBytes uint64) (string, func(), error) {
			dest := filepath.Join(poolDir, dstName)
			_ = os.WriteFile(dest, []byte("cloned-vm-root-disk"), 0644)
			return dest, func() { _ = os.Remove(dest) }, nil
		},
		uploadSeedISO: func(ctx context.Context, client *virt.Client, poolName, isoName string, data []byte) (string, func(), error) {
			seedUploaded = true
			dest := filepath.Join(poolDir, isoName)
			_ = os.WriteFile(dest, data, 0644)
			seedPathCreated = dest
			return dest, func() { _ = os.Remove(dest) }, nil
		},
		defineDomain: func(ctx context.Context, client *virt.Client, xmlDoc string) (string, any, error) {
			definedXML = xmlDoc
			var d libvirtxml.Domain
			_ = d.Unmarshal(xmlDoc)
			return d.UUID, nil, nil
		},
	}
	ctx := WithDeployHooks(context.Background(), hooks)

	req := DeployRequest{
		ItemID:  item.ID,
		Name:    "cloud-box-01",
		Pool:    poolDir,
		DiskGiB: 20,
		CloudInit: &virt.CloudInit{
			User:     "ubuntu",
			Password: "securePassword123!",
			Hostname: "cloud-box-01",
		},
	}

	uuid, err := Deploy(ctx, nil, store, nil, req, selfHost, nil)
	if err != nil {
		t.Fatalf("Deploy with CloudInit failed: %v", err)
	}
	if uuid == "" {
		t.Fatal("expected valid UUID")
	}

	if !seedUploaded {
		t.Fatalf("expected cloud-init seed ISO to be uploaded")
	}

	var parsed libvirtxml.Domain
	if err := parsed.Unmarshal(definedXML); err != nil {
		t.Fatalf("unmarshal XML failed: %v", err)
	}

	// Verify CD-ROM drive is attached with seedPathCreated
	var foundCDROM *libvirtxml.DomainDisk
	for i := range parsed.Devices.Disks {
		d := &parsed.Devices.Disks[i]
		if d.Device == "cdrom" {
			foundCDROM = d
			break
		}
	}
	if foundCDROM == nil {
		t.Fatal("expected CD-ROM disk attached for cloud-init seed, none found")
	}
	if foundCDROM.Source == nil || foundCDROM.Source.File == nil || foundCDROM.Source.File.File != seedPathCreated {
		t.Errorf("CD-ROM source file = %+v, want %q", foundCDROM.Source, seedPathCreated)
	}
	if !strings.HasSuffix(foundCDROM.Source.File.File, "-cidata.iso") {
		t.Errorf("CD-ROM file does not end in -cidata.iso: %q", foundCDROM.Source.File.File)
	}
}

// TestDeploy_DuplicateDomainName_RejectedBeforeVolumeCreation verifies:
// Duplicate domain name is rejected before any volume is created.
func TestDeploy_DuplicateDomainName_RejectedBeforeVolumeCreation(t *testing.T) {
	store, err := Open("")
	if err != nil {
		t.Fatal(err)
	}

	poolDir := t.TempDir()
	selfHost := "node-local"

	volFile := filepath.Join(poolDir, "content-debian-v12.qcow2")
	_ = os.WriteFile(volFile, []byte("debian-disk-image"), 0644)

	item, err := store.CreateItem(Item{
		ID:   "item-deb",
		Name: "debian",
		Type: TypeImage,
		Versions: []ItemVersion{
			{
				ID:         "v1",
				Version:    "12",
				Format:     "qcow2",
				Size:       int64(len("debian-disk-image")),
				SHA256:     sha256Hex([]byte("debian-disk-image")),
				SourceHost: selfHost,
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}

	volumeCreated := false
	hooks := &deployHooks{
		nameTaken: func(ctx context.Context, client *virt.Client, name string) (bool, error) {
			if name == "existing-machine" {
				return true, nil
			}
			return false, nil
		},
		resolvePoolPath: func(ctx context.Context, client *virt.Client, pool string) (string, error) {
			return poolDir, nil
		},
		cloneVolume: func(ctx context.Context, client *virt.Client, srcPath, poolName, dstName, format string, minBytes uint64) (string, func(), error) {
			volumeCreated = true
			return "", nil, nil
		},
		createVolume: func(ctx context.Context, client *virt.Client, poolName, volName string, sizeBytes uint64, format string) (string, func(), error) {
			volumeCreated = true
			return "", nil, nil
		},
		uploadSeedISO: func(ctx context.Context, client *virt.Client, poolName, isoName string, data []byte) (string, func(), error) {
			volumeCreated = true
			return "", nil, nil
		},
	}
	ctx := WithDeployHooks(context.Background(), hooks)

	req := DeployRequest{
		ItemID: item.ID,
		Name:   "existing-machine",
		Pool:   poolDir,
	}

	_, err = Deploy(ctx, nil, store, nil, req, selfHost, nil)
	if err == nil {
		t.Fatal("expected error for duplicate domain name, got nil")
	}

	if !errors.Is(err, ErrConflict) && !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error = %v, expected conflict or 'already exists'", err)
	}

	if volumeCreated {
		t.Errorf("a storage volume was created even though machine name was already taken!")
	}
}

// TestDeploy_FailureDuringDefine_TriggersVolumeRollback verifies:
// Failure during define triggers volume rollback.
func TestDeploy_FailureDuringDefine_TriggersVolumeRollback(t *testing.T) {
	store, err := Open("")
	if err != nil {
		t.Fatal(err)
	}

	poolDir := t.TempDir()
	selfHost := "node-local"

	volFile := filepath.Join(poolDir, "content-rocky-v9.qcow2")
	_ = os.WriteFile(volFile, []byte("rocky-disk-image"), 0644)

	item, err := store.CreateItem(Item{
		ID:   "item-rocky",
		Name: "rocky",
		Type: TypeImage,
		Versions: []ItemVersion{
			{
				ID:         "v1",
				Version:    "9",
				Format:     "qcow2",
				Size:       int64(len("rocky-disk-image")),
				SHA256:     sha256Hex([]byte("rocky-disk-image")),
				SourceHost: selfHost,
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}

	var clonedPathCreated string
	var seedPathCreated string

	hooks := &deployHooks{
		resolvePoolPath: func(ctx context.Context, client *virt.Client, pool string) (string, error) {
			return poolDir, nil
		},
		cloneVolume: func(ctx context.Context, client *virt.Client, srcPath, poolName, dstName, format string, minBytes uint64) (string, func(), error) {
			dest := filepath.Join(poolDir, dstName)
			_ = os.WriteFile(dest, []byte("cloned-disk-before-fail"), 0644)
			clonedPathCreated = dest
			cleanup := func() {
				_ = os.Remove(dest)
			}
			return dest, cleanup, nil
		},
		uploadSeedISO: func(ctx context.Context, client *virt.Client, poolName, isoName string, data []byte) (string, func(), error) {
			dest := filepath.Join(poolDir, isoName)
			_ = os.WriteFile(dest, data, 0644)
			seedPathCreated = dest
			cleanup := func() {
				_ = os.Remove(dest)
			}
			return dest, cleanup, nil
		},
		defineDomain: func(ctx context.Context, client *virt.Client, xmlDoc string) (string, any, error) {
			return "", nil, errors.New("simulated libvirt define error: invalid hypervisor capability")
		},
	}
	ctx := WithDeployHooks(context.Background(), hooks)

	req := DeployRequest{
		ItemID: item.ID,
		Name:   "fail-vm",
		Pool:   poolDir,
		CloudInit: &virt.CloudInit{
			User:     "rocky",
			Password: "somepassword",
		},
	}

	_, err = Deploy(ctx, nil, store, nil, req, selfHost, nil)
	if err == nil {
		t.Fatal("expected Deploy to return error, got nil")
	}

	if !strings.Contains(err.Error(), "invalid hypervisor capability") {
		t.Errorf("unexpected error message: %v", err)
	}

	// Verify rollback cleaned up the cloned volume
	if clonedPathCreated == "" {
		t.Fatal("expected clonedPathCreated to be recorded")
	}
	if _, err := os.Stat(clonedPathCreated); err == nil {
		t.Errorf("cloned volume file %s still exists after rollback!", clonedPathCreated)
	}

	// Verify rollback cleaned up the seed ISO
	if seedPathCreated == "" {
		t.Fatal("expected seedPathCreated to be recorded")
	}
	if _, err := os.Stat(seedPathCreated); err == nil {
		t.Errorf("seed ISO file %s still exists after rollback!", seedPathCreated)
	}
}

// TestDeploy_ISODeployment verifies deploying from an ISO item:
// Creates empty disk volume of size req.DiskGiB in target pool and attaches the library ISO as bootable CD-ROM.
func TestDeploy_ISODeployment(t *testing.T) {
	store, err := Open("")
	if err != nil {
		t.Fatal(err)
	}

	poolDir := t.TempDir()
	selfHost := "node-local"

	isoData := []byte("fake-debian-netinst-iso-data")
	isoFile := filepath.Join(poolDir, "content-debian-netinst-v12.5.iso")
	_ = os.WriteFile(isoFile, isoData, 0644)

	item, err := store.CreateItem(Item{
		ID:   "item-iso-deb",
		Name: "debian-netinst",
		Type: TypeISO,
		Versions: []ItemVersion{
			{
				ID:         "v1",
				Version:    "12.5",
				Format:     "iso",
				Size:       int64(len(isoData)),
				SHA256:     sha256Hex(isoData),
				SourceHost: selfHost,
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}

	var emptyDiskCreated string
	var definedXML string
	domainStarted := false

	hooks := &deployHooks{
		resolvePoolPath: func(ctx context.Context, client *virt.Client, pool string) (string, error) {
			return poolDir, nil
		},
		createVolume: func(ctx context.Context, client *virt.Client, poolName, volName string, sizeBytes uint64, format string) (string, func(), error) {
			dest := filepath.Join(poolDir, volName)
			_ = os.WriteFile(dest, []byte("empty-qcow2-disk"), 0644)
			emptyDiskCreated = dest
			return dest, func() { _ = os.Remove(dest) }, nil
		},
		defineDomain: func(ctx context.Context, client *virt.Client, xmlDoc string) (string, any, error) {
			definedXML = xmlDoc
			var d libvirtxml.Domain
			_ = d.Unmarshal(xmlDoc)
			return d.UUID, "fake-dom-handle", nil
		},
		startDomain: func(ctx context.Context, client *virt.Client, domObj any) error {
			if domObj == "fake-dom-handle" {
				domainStarted = true
			}
			return nil
		},
	}
	ctx := WithDeployHooks(context.Background(), hooks)

	req := DeployRequest{
		ItemID:  item.ID,
		Name:    "install-vm",
		Pool:    poolDir,
		DiskGiB: 30,
		Start:   true,
	}

	uuid, err := Deploy(ctx, nil, store, nil, req, selfHost, nil)
	if err != nil {
		t.Fatalf("Deploy ISO failed: %v", err)
	}
	if uuid == "" {
		t.Fatal("expected non-empty UUID")
	}

	if emptyDiskCreated == "" {
		t.Fatal("expected empty disk to be created")
	}

	var parsed libvirtxml.Domain
	if err := parsed.Unmarshal(definedXML); err != nil {
		t.Fatalf("unmarshal XML failed: %v", err)
	}

	// Verify ISO is attached as bootable CD-ROM (Boot.Order == 1)
	var foundISO *libvirtxml.DomainDisk
	var foundDisk *libvirtxml.DomainDisk
	for i := range parsed.Devices.Disks {
		d := &parsed.Devices.Disks[i]
		if d.Device == "cdrom" && d.Source != nil && d.Source.File != nil && d.Source.File.File == isoFile {
			foundISO = d
		}
		if d.Device == "disk" && d.Source != nil && d.Source.File != nil && d.Source.File.File == emptyDiskCreated {
			foundDisk = d
		}
	}

	if foundISO == nil {
		t.Fatalf("library ISO %s was not attached as CD-ROM in domain XML", isoFile)
	}
	if foundISO.Boot == nil || foundISO.Boot.Order != 1 {
		t.Errorf("ISO CD-ROM boot order = %+v, want 1", foundISO.Boot)
	}
	if foundDisk == nil {
		t.Fatalf("empty disk %s was not attached as disk in domain XML", emptyDiskCreated)
	}
	if !domainStarted {
		t.Errorf("expected domain to be started because req.Start was true")
	}
}

// TestDeploy_WithoutLibvirt_DoesNotFakeSuccess verifies that deploying on a host without a
// libvirt client fails with a clear error instead of returning a fake UUID as success.
func TestDeploy_WithoutLibvirt_DoesNotFakeSuccess(t *testing.T) {
	store, err := Open("")
	if err != nil {
		t.Fatal(err)
	}

	poolDir := t.TempDir()
	selfHost := "node-local"
	if err := store.SetHostConfig(HostConfig{HostID: selfHost, TargetPool: poolDir}); err != nil {
		t.Fatal(err)
	}

	imgContent := []byte("standalone-image-without-libvirt")
	imgPath := filepath.Join(poolDir, "content-standalone-v1.0.qcow2")
	if err := os.WriteFile(imgPath, imgContent, 0644); err != nil {
		t.Fatal(err)
	}

	item, err := store.CreateItem(Item{
		ID:   "item-standalone",
		Name: "standalone",
		Type: TypeImage,
		Versions: []ItemVersion{
			{
				ID:         "v1",
				Version:    "1.0",
				Format:     "qcow2",
				Size:       int64(len(imgContent)),
				SHA256:     sha256Hex(imgContent),
				SourceHost: selfHost,
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}

	// No deploy hooks and a nil client: Deploy must report failure, not a fabricated UUID.
	uuid, err := Deploy(context.Background(), nil, store, nil, DeployRequest{
		ItemID: item.ID,
		Name:   "should-fail-vm",
		Pool:   poolDir,
	}, selfHost, nil)
	if err == nil {
		t.Fatalf("Deploy without a libvirt client returned uuid %q and nil error (fake success)", uuid)
	}
	if !strings.Contains(err.Error(), "libvirt client is required") {
		t.Errorf("error = %v, want it to mention the missing libvirt client", err)
	}
}

// TestDeploy_SubscriberUsesLocalSyncedCopy verifies that a subscriber host deploying
// from an item it has already synced uses the local copy without contacting the
// source host, even though the version's SourceHost is remote.
func TestDeploy_SubscriberUsesLocalSyncedCopy(t *testing.T) {
	store, err := Open("")
	if err != nil {
		t.Fatal(err)
	}

	poolDir := t.TempDir()
	selfHost := "host-b"
	remoteHost := "host-a"

	if err := store.SetHostConfig(HostConfig{HostID: selfHost, TargetPool: poolDir}); err != nil {
		t.Fatal(err)
	}

	payload := []byte("subscriber-local-synced-copy-payload")
	localPath := filepath.Join(poolDir, "content-cirros-lib-v1.0.img")
	if err := os.WriteFile(localPath, payload, 0644); err != nil {
		t.Fatal(err)
	}

	item, err := store.CreateItem(Item{
		ID:   "item-e2e-deploy",
		Name: "cirros-lib",
		Type: TypeImage,
		Versions: []ItemVersion{
			{
				ID:         "ver-e2e-1",
				Version:    "1.0",
				Format:     "img",
				Size:       int64(len(payload)),
				SHA256:     sha256Hex(payload),
				SourceHost: remoteHost,
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateItem failed: %v", err)
	}

	// Peer points at a closed port: any pull attempt would fail the deploy.
	peer := cluster.NewPeer(cluster.Node{ID: remoteHost, Address: "http://127.0.0.1:9"}, nil)
	peers := MapPeerResolver{remoteHost: peer}

	var reports []string
	reportFn := func(msg string) { reports = append(reports, msg) }

	hooks := &deployHooks{
		resolvePoolPath: func(ctx context.Context, client *virt.Client, pool string) (string, error) {
			return poolDir, nil
		},
		cloneVolume: func(ctx context.Context, client *virt.Client, srcPath, poolName, dstName, format string, minBytes uint64) (string, func(), error) {
			dest := filepath.Join(poolDir, dstName)
			if err := copyArtifactFile(srcPath, dest); err != nil {
				return "", nil, err
			}
			cleanup := func() { _ = os.Remove(dest) }
			return dest, cleanup, nil
		},
		defineDomain: func(ctx context.Context, client *virt.Client, xmlDoc string) (string, any, error) {
			var d libvirtxml.Domain
			_ = d.Unmarshal(xmlDoc)
			return d.UUID, nil, nil
		},
	}
	ctx := WithDeployHooks(context.Background(), hooks)

	req := DeployRequest{
		ItemID: item.ID,
		Name:   "subscriber-vm",
		Pool:   poolDir,
	}

	uuid, err := Deploy(ctx, nil, store, peers, req, selfHost, reportFn)
	if err != nil {
		t.Fatalf("Deploy on subscriber failed: %v", err)
	}
	if uuid == "" {
		t.Fatal("expected non-empty UUID")
	}

	localCopyUsed := false
	for _, r := range reports {
		if strings.Contains(r, "Using local synced copy") {
			localCopyUsed = true
			break
		}
	}
	if !localCopyUsed {
		t.Errorf("expected a report about using the local synced copy, got: %v", reports)
	}

	// The local library copy must still exist and the cloned VM disk must read from it
	if _, err := os.Stat(localPath); err != nil {
		t.Errorf("local synced copy missing after deploy: %v", err)
	}
	if st, err := os.Stat(filepath.Join(poolDir, "subscriber-vm.qcow2")); err != nil || st.Size() != int64(len(payload)) {
		t.Errorf("expected cloned disk copied from the local synced copy, st=%+v err=%v", st, err)
	}
}

// TestDeploy_LocalTemplate_WithCloudInit verifies that deploying a template with CloudInit
// builds and uploads a seed ISO, attaches it as a CD-ROM, and grows the disk if DiskGiB is set.
func TestDeploy_LocalTemplate_WithCloudInit(t *testing.T) {
	store, err := Open("")
	if err != nil {
		t.Fatal(err)
	}

	poolDir := t.TempDir()
	selfHost := "node-local"

	if err := store.SetHostConfig(HostConfig{HostID: selfHost, TargetPool: poolDir}); err != nil {
		t.Fatal(err)
	}

	tplContent := []byte("template-disk-image-bytes-v1")
	tplPath := filepath.Join(poolDir, "content-ubuntu-tpl-ci-v1.0.qcow2")
	if err := os.WriteFile(tplPath, tplContent, 0644); err != nil {
		t.Fatal(err)
	}

	item, err := store.CreateItem(Item{
		ID:   "item-tpl-ci",
		Name: "ubuntu-tpl-ci",
		Type: TypeTemplate,
		Versions: []ItemVersion{
			{
				ID:          "ver-1",
				Version:     "1.0",
				Format:      "qcow2",
				Size:        int64(len(tplContent)),
				SHA256:      sha256Hex(tplContent),
				TemplateXML: sampleTemplateXML,
				SourceHost:  selfHost,
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateItem failed: %v", err)
	}

	var definedXML string
	var seedUploaded bool
	var uploadedSeedPath string
	var clonedMinBytes uint64

	hooks := &deployHooks{
		resolvePoolPath: func(ctx context.Context, client *virt.Client, pool string) (string, error) {
			return poolDir, nil
		},
		cloneVolume: func(ctx context.Context, client *virt.Client, srcPath, poolName, dstName, format string, minBytes uint64) (string, func(), error) {
			clonedMinBytes = minBytes
			dest := filepath.Join(poolDir, dstName)
			if err := os.WriteFile(dest, []byte("cloned-disk-data"), 0644); err != nil {
				return "", nil, err
			}
			cleanup := func() { _ = os.Remove(dest) }
			return dest, cleanup, nil
		},
		uploadSeedISO: func(ctx context.Context, client *virt.Client, poolName, isoName string, data []byte) (string, func(), error) {
			seedUploaded = true
			dest := filepath.Join(poolDir, isoName)
			if err := os.WriteFile(dest, data, 0644); err != nil {
				return "", nil, err
			}
			uploadedSeedPath = dest
			cleanup := func() { _ = os.Remove(dest) }
			return dest, cleanup, nil
		},
		defineDomain: func(ctx context.Context, client *virt.Client, xmlDoc string) (string, any, error) {
			definedXML = xmlDoc
			var dom libvirtxml.Domain
			if err := dom.Unmarshal(xmlDoc); err != nil {
				return "", nil, err
			}
			return dom.UUID, nil, nil
		},
	}
	ctx := WithDeployHooks(context.Background(), hooks)

	req := DeployRequest{
		ItemID:  item.ID,
		Name:    "tpl-ci-vm",
		Pool:    poolDir,
		DiskGiB: 35,
		CloudInit: &virt.CloudInit{
			User:     "ubuntu",
			Password: "securePassword456!",
			Hostname: "tpl-ci-vm",
		},
	}

	newUUID, err := Deploy(ctx, nil, store, nil, req, selfHost, nil)
	if err != nil {
		t.Fatalf("Deploy template with cloudinit failed: %v", err)
	}
	if newUUID == "" {
		t.Fatal("expected non-empty UUID")
	}

	if !seedUploaded {
		t.Fatal("expected seed ISO to be uploaded")
	}
	if clonedMinBytes != 35<<30 {
		t.Errorf("cloned minBytes = %d, want %d", clonedMinBytes, uint64(35<<30))
	}

	var parsed libvirtxml.Domain
	if err := parsed.Unmarshal(definedXML); err != nil {
		t.Fatalf("unmarshal defined XML failed: %v", err)
	}

	// Verify attached seed ISO CD-ROM
	var foundSeed *libvirtxml.DomainDisk
	for i := range parsed.Devices.Disks {
		d := &parsed.Devices.Disks[i]
		if d.Device == "cdrom" && d.Source != nil && d.Source.File != nil && d.Source.File.File == uploadedSeedPath {
			foundSeed = d
			break
		}
	}
	if foundSeed == nil {
		t.Fatalf("cloud-init seed ISO %s not attached to defined domain", uploadedSeedPath)
	}
}

// TestDeploy_LocalTemplate_StaleSeedStripped verifies that an old -cidata.iso in the template XML
// is stripped when deploying without CloudInit, so stale identity is not inherited.
func TestDeploy_LocalTemplate_StaleSeedStripped(t *testing.T) {
	store, err := Open("")
	if err != nil {
		t.Fatal(err)
	}

	poolDir := t.TempDir()
	selfHost := "node-local"

	if err := store.SetHostConfig(HostConfig{HostID: selfHost, TargetPool: poolDir}); err != nil {
		t.Fatal(err)
	}

	tplContent := []byte("template-disk-image-bytes-v1")
	tplPath := filepath.Join(poolDir, "content-ubuntu-stale-v1.0.qcow2")
	if err := os.WriteFile(tplPath, tplContent, 0644); err != nil {
		t.Fatal(err)
	}

	xmlWithStaleSeed := `<domain type='kvm'>
  <name>ubuntu-stale</name>
  <uuid>11111111-2222-3333-4444-555555555555</uuid>
  <memory unit='MiB'>1024</memory>
  <vcpu placement='static'>1</vcpu>
  <os><type arch='x86_64' machine='q35'>hvm</type></os>
  <devices>
    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2'/>
      <source file='/old/storage/ubuntu-base.qcow2'/>
      <target dev='vda' bus='virtio'/>
    </disk>
    <disk type='file' device='cdrom'>
      <driver name='qemu' type='raw'/>
      <source file='/old/storage/old-ubuntu-cidata.iso'/>
      <target dev='sda' bus='sata'/>
      <readonly/>
    </disk>
  </devices>
</domain>`

	item, err := store.CreateItem(Item{
		ID:   "item-tpl-stale",
		Name: "ubuntu-stale",
		Type: TypeTemplate,
		Versions: []ItemVersion{
			{
				ID:          "ver-1",
				Version:     "1.0",
				Format:      "qcow2",
				Size:        int64(len(tplContent)),
				SHA256:      sha256Hex(tplContent),
				TemplateXML: xmlWithStaleSeed,
				SourceHost:  selfHost,
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateItem failed: %v", err)
	}

	var definedXML string
	hooks := &deployHooks{
		resolvePoolPath: func(ctx context.Context, client *virt.Client, pool string) (string, error) {
			return poolDir, nil
		},
		cloneVolume: func(ctx context.Context, client *virt.Client, srcPath, poolName, dstName, format string, minBytes uint64) (string, func(), error) {
			dest := filepath.Join(poolDir, dstName)
			_ = os.WriteFile(dest, []byte("cloned-disk"), 0644)
			return dest, func() { _ = os.Remove(dest) }, nil
		},
		defineDomain: func(ctx context.Context, client *virt.Client, xmlDoc string) (string, any, error) {
			definedXML = xmlDoc
			return "new-dom-uuid", nil, nil
		},
	}
	ctx := WithDeployHooks(context.Background(), hooks)

	req := DeployRequest{
		ItemID: item.ID,
		Name:   "clean-vm",
		Pool:   poolDir,
	}

	_, err = Deploy(ctx, nil, store, nil, req, selfHost, nil)
	if err != nil {
		t.Fatalf("Deploy failed: %v", err)
	}

	var parsed libvirtxml.Domain
	if err := parsed.Unmarshal(definedXML); err != nil {
		t.Fatalf("unmarshal XML failed: %v", err)
	}

	for _, d := range parsed.Devices.Disks {
		if d.Source != nil && d.Source.File != nil && strings.Contains(d.Source.File.File, "-cidata.iso") {
			t.Errorf("stale seed ISO %s was NOT stripped from template definition", d.Source.File.File)
		}
	}
}

// copyArtifactFile copies src to dst through a temporary file (the cloneVolume hook of the tests).
func copyArtifactFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	tmpDst := dst + ".tmp"
	out, err := os.Create(tmpDst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(tmpDst)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmpDst)
		return err
	}
	return os.Rename(tmpDst, dst)
}
