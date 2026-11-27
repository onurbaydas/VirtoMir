package content

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/onurbaydas/virtomir/internal/virt"
	"libvirt.org/go/libvirtxml"
)

var (
	// ErrRefused indicates that an operation was refused because the state or configuration is inappropriate.
	ErrRefused = errors.New("refused")
	qcow2Magic = []byte{'Q', 'F', 'I', 0xfb}
)

type refusedError struct {
	classed
}

func (e refusedError) As(target any) bool {
	if op, ok := target.(**virt.OperationError); ok {
		*op = &virt.OperationError{Message: e.msg}
		return true
	}
	return false
}

func refusedf(format string, a ...any) error {
	return refusedError{
		classed: classed{
			class:  ErrRefused,
			second: ErrInvalid,
			msg:    fmt.Sprintf(format, a...),
		},
	}
}

// PublishTemplateReq specifies parameters for publishing an existing local VM template to the content library.
type PublishTemplateReq struct {
	TemplateUUID string     `json:"templateUuid"`
	Name         string     `json:"name"`
	Version      string     `json:"version"`
	Description  string     `json:"description,omitempty"`
	KeepVersions int        `json:"keepVersions,omitempty"`
	SyncPolicy   SyncPolicy `json:"syncPolicy,omitempty"`
}

// PublishVolumeReq specifies parameters for publishing a storage volume to the content library.
type PublishVolumeReq struct {
	Pool         string     `json:"pool"`
	Volume       string     `json:"volume"`
	Type         ItemType   `json:"type"`
	Name         string     `json:"name"`
	Version      string     `json:"version"`
	Description  string     `json:"description,omitempty"`
	KeepVersions int        `json:"keepVersions,omitempty"`
	SyncPolicy   SyncPolicy `json:"syncPolicy,omitempty"`
}

type domainInfo struct {
	Name     string
	Template bool
	State    virt.DomainState
	XML      string
}

type domainLookupFunc func(ctx context.Context, client *virt.Client, uuid string) (*domainInfo, error)
type volumeListFunc func(ctx context.Context, client *virt.Client, pool string) ([]virt.Volume, error)

type ctxLookupKey struct{}
type ctxVolumeKey struct{}

// WithDomainLookup attaches a custom domain lookup function to ctx for testing.
func WithDomainLookup(ctx context.Context, fn domainLookupFunc) context.Context {
	return context.WithValue(ctx, ctxLookupKey{}, fn)
}

// WithVolumeList attaches a custom volume list function to ctx for testing.
func WithVolumeList(ctx context.Context, fn volumeListFunc) context.Context {
	return context.WithValue(ctx, ctxVolumeKey{}, fn)
}

// HasDomainLookupHook checks if a custom domain lookup hook is attached to ctx.
func HasDomainLookupHook(ctx context.Context) bool {
	return ctx.Value(ctxLookupKey{}) != nil
}

// HasVolumeListHook checks if a custom volume list hook is attached to ctx.
func HasVolumeListHook(ctx context.Context) bool {
	return ctx.Value(ctxVolumeKey{}) != nil
}

var (
	domainLookupHook domainLookupFunc = defaultLookupDomain
	volumeListHook   volumeListFunc   = defaultListVolumes
)

func defaultLookupDomain(ctx context.Context, client *virt.Client, uuid string) (*domainInfo, error) {
	if client == nil {
		return nil, notFoundf("no libvirt client available")
	}
	detail, err := client.DomainDetail(uuid)
	if err != nil {
		return nil, err
	}
	xmlDoc, err := client.DomainXML(uuid, true)
	if err != nil {
		return nil, err
	}
	return &domainInfo{
		Name:     detail.Name,
		Template: detail.Template,
		State:    detail.State,
		XML:      xmlDoc,
	}, nil
}

func defaultListVolumes(ctx context.Context, client *virt.Client, pool string) ([]virt.Volume, error) {
	// Volumes come from libvirt only: a request never names a folder of the host directly.
	if client == nil {
		return nil, notFoundf("no libvirt client available")
	}
	return client.ListVolumes(pool)
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (cr *ctxReader) Read(p []byte) (int, error) {
	if err := cr.ctx.Err(); err != nil {
		return 0, err
	}
	return cr.r.Read(p)
}

// StreamFileSHA256 streams the file at path through sha256.New() via io.Copy with context cancellation check.
// It returns the hex-encoded checksum and the exact byte size.
func StreamFileSHA256(ctx context.Context, path string) (checksum string, size int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()

	h := sha256.New()
	cr := &ctxReader{ctx: ctx, r: f}
	written, err := io.Copy(h, cr)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), written, nil
}

// DetectDiskFormat inspects the first 4 bytes of path to recognize qcow2 magic bytes,
// falling back to the specified fallback or "raw".
func DetectDiskFormat(path string, fallback string) string {
	f, err := os.Open(path)
	if err != nil {
		if fallback != "" {
			return fallback
		}
		return "raw"
	}
	defer f.Close()

	magic := make([]byte, 4)
	n, _ := f.Read(magic)
	if n >= 4 && bytes.Equal(magic, qcow2Magic) {
		return "qcow2"
	}
	if fallback != "" {
		return fallback
	}
	return "raw"
}

// SanitizeDomainXML sanitizes domain XML for portable distribution across hosts:
// strips UUID/ID/HWUUID/GenID, drops MAC addresses and targets from <interface> elements,
// removes <nvram> path references (while preserving template attributes),
// resets graphics ports to automatic, and removes fixed PCI/bus slot addresses.
func SanitizeDomainXML(doc string) (string, error) {
	var x libvirtxml.Domain
	if err := x.Unmarshal(doc); err != nil {
		return "", fmt.Errorf("parse domain XML: %w", err)
	}
	SanitizeDomain(&x)
	return x.Marshal()
}

// SanitizeDomain applies in-place portability sanitization to a libvirtxml.Domain struct.
func SanitizeDomain(x *libvirtxml.Domain) {
	x.UUID = ""
	x.ID = nil
	x.HWUUID = ""
	x.GenID = nil

	if x.OS != nil && x.OS.NVRam != nil {
		x.OS.NVRam.NVRam = ""
		x.OS.NVRam.Source = nil
	}

	if x.Devices == nil {
		return
	}

	for i := range x.Devices.Interfaces {
		x.Devices.Interfaces[i].MAC = nil
		x.Devices.Interfaces[i].Target = nil
	}

	for i := range x.Devices.Graphics {
		g := &x.Devices.Graphics[i]
		if g.VNC != nil && g.VNC.Port > 0 {
			g.VNC.Port, g.VNC.AutoPort = -1, "yes"
		}
		if g.Spice != nil && g.Spice.Port > 0 {
			g.Spice.Port, g.Spice.AutoPort = -1, "yes"
		}
	}

	stripDeviceAddresses(x.Devices)
}

func stripDeviceAddresses(devs *libvirtxml.DomainDeviceList) {
	if devs == nil {
		return
	}
	for i := range devs.Disks {
		devs.Disks[i].Address = nil
	}
	for i := range devs.Controllers {
		devs.Controllers[i].Address = nil
	}
	for i := range devs.Interfaces {
		devs.Interfaces[i].Address = nil
	}
	for i := range devs.Videos {
		devs.Videos[i].Address = nil
	}
	for i := range devs.Sounds {
		devs.Sounds[i].Address = nil
	}
	for i := range devs.Hostdevs {
		devs.Hostdevs[i].Address = nil
	}
	for i := range devs.RedirDevs {
		devs.RedirDevs[i].Address = nil
	}
	for i := range devs.Channels {
		devs.Channels[i].Address = nil
	}
	for i := range devs.Serials {
		devs.Serials[i].Address = nil
	}
	for i := range devs.Consoles {
		devs.Consoles[i].Address = nil
	}
	for i := range devs.Inputs {
		devs.Inputs[i].Address = nil
	}
	for i := range devs.RNGs {
		devs.RNGs[i].Address = nil
	}
	for i := range devs.Watchdogs {
		devs.Watchdogs[i].Address = nil
	}
	if devs.MemBalloon != nil {
		devs.MemBalloon.Address = nil
	}
	if devs.VSock != nil {
		devs.VSock.Address = nil
	}
	for i := range devs.Shmems {
		devs.Shmems[i].Address = nil
	}
	for i := range devs.TPMs {
		devs.TPMs[i].Address = nil
	}
	for i := range devs.Filesystems {
		devs.Filesystems[i].Address = nil
	}
}

// PublishTemplate publishes a local VM template to the content library catalog.
func PublishTemplate(ctx context.Context, client *virt.Client, store *Store, selfHostID string, req PublishTemplateReq) (Item, error) {
	if store == nil {
		return Item{}, invalidf("store is required")
	}
	req.TemplateUUID = strings.TrimSpace(req.TemplateUUID)
	if req.TemplateUUID == "" {
		return Item{}, invalidf("template UUID is required")
	}
	req.Version = strings.TrimSpace(req.Version)
	if req.Version == "" {
		return Item{}, invalidf("version is required")
	}

	lookup := domainLookupHook
	if fn, ok := ctx.Value(ctxLookupKey{}).(domainLookupFunc); ok && fn != nil {
		lookup = fn
	}

	info, err := lookup(ctx, client, req.TemplateUUID)
	if err != nil {
		return Item{}, err
	}
	if !info.Template {
		return Item{}, refusedf("machine %q is not a template", info.Name)
	}
	if info.State != virt.StateShutoff && info.State != virt.StateCrashed {
		return Item{}, refusedf("machine %q is running; a template must be shut off before publishing", info.Name)
	}

	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		req.Name = info.Name
	}
	if req.Name == "" {
		return Item{}, invalidf("item name is required")
	}

	var x libvirtxml.Domain
	if err := x.Unmarshal(info.XML); err != nil {
		return Item{}, fmt.Errorf("parse template domain XML: %w", err)
	}

	disks := virt.TemplateDisks(&x)
	if len(disks) == 0 {
		return Item{}, invalidf("template %q has no writable disks", info.Name)
	}
	primary := disks[0]
	if primary.Source == nil || primary.Source.File == nil || primary.Source.File.File == "" {
		return Item{}, invalidf("template %q primary disk has no file image source", info.Name)
	}
	srcPath := primary.Source.File.File

	st, err := os.Stat(srcPath)
	if err != nil {
		return Item{}, notFoundf("template primary disk %q does not exist: %v", srcPath, err)
	}
	if st.IsDir() {
		return Item{}, invalidf("template primary disk %q is a directory", srcPath)
	}

	checksum, size, err := StreamFileSHA256(ctx, srcPath)
	if err != nil {
		return Item{}, err
	}

	fallbackFmt := "raw"
	if primary.Driver != nil && primary.Driver.Type != "" {
		fallbackFmt = primary.Driver.Type
	}
	format := DetectDiskFormat(srcPath, fallbackFmt)

	SanitizeDomain(&x)
	sanitizedXML, err := x.Marshal()
	if err != nil {
		return Item{}, fmt.Errorf("marshal sanitized domain XML: %w", err)
	}

	now := time.Now().UTC()
	version := ItemVersion{
		ID:          randomHex(8),
		Version:     req.Version,
		Description: req.Description,
		SHA256:      checksum,
		Size:        size,
		Format:      format,
		SourceHost:  selfHostID,
		TemplateXML: sanitizedXML,
		Created:     now,
	}

	var existing *Item
	for _, it := range store.ListItems() {
		if strings.EqualFold(it.Name, req.Name) {
			existing = &it
			break
		}
	}

	if existing != nil {
		if existing.Type != TypeTemplate {
			return Item{}, conflictf("an item named %q already exists with type %q", req.Name, existing.Type)
		}
		updated, _, err := store.AddVersion(existing.ID, version)
		return updated, err
	}

	item := Item{
		Name:         req.Name,
		Type:         TypeTemplate,
		Description:  req.Description,
		SourceHost:   selfHostID,
		KeepVersions: req.KeepVersions,
		SyncPolicy:   req.SyncPolicy,
		Versions:     []ItemVersion{version},
		Hosts:        []HostSyncState{{Host: selfHostID, State: "synced", Updated: now}},
	}
	return store.CreateItem(item)
}

// PublishVolume publishes a local storage volume (ISO or cloud image) to the content library catalog.
func PublishVolume(ctx context.Context, client *virt.Client, store *Store, selfHostID string, req PublishVolumeReq) (Item, error) {
	if store == nil {
		return Item{}, invalidf("store is required")
	}
	if req.Type != TypeISO && req.Type != TypeImage {
		return Item{}, invalidf("invalid item type %q (must be iso or image)", req.Type)
	}
	req.Pool = strings.TrimSpace(req.Pool)
	if req.Pool == "" {
		return Item{}, invalidf("storage pool is required")
	}
	req.Volume = strings.TrimSpace(req.Volume)
	if req.Volume == "" {
		return Item{}, invalidf("storage volume name is required")
	}
	req.Version = strings.TrimSpace(req.Version)
	if req.Version == "" {
		return Item{}, invalidf("version is required")
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		req.Name = req.Volume
	}

	volList := volumeListHook
	if fn, ok := ctx.Value(ctxVolumeKey{}).(volumeListFunc); ok && fn != nil {
		volList = fn
	}

	vols, err := volList(ctx, client, req.Pool)
	if err != nil {
		return Item{}, err
	}

	var targetVol *virt.Volume
	for i := range vols {
		if vols[i].Name == req.Volume {
			targetVol = &vols[i]
			break
		}
	}
	if targetVol == nil {
		return Item{}, notFoundf("volume %q not found in pool %q", req.Volume, req.Pool)
	}

	if targetVol.Path == "" {
		return Item{}, notFoundf("volume %q has no filesystem path", req.Volume)
	}
	st, err := os.Stat(targetVol.Path)
	if err != nil {
		return Item{}, notFoundf("volume file %s does not exist: %v", targetVol.Path, err)
	}
	if st.IsDir() {
		return Item{}, invalidf("volume file %s is a directory", targetVol.Path)
	}

	checksum, size, err := StreamFileSHA256(ctx, targetVol.Path)
	if err != nil {
		return Item{}, err
	}

	fallbackFmt := "raw"
	if req.Type == TypeISO {
		fallbackFmt = "iso"
	} else if targetVol.Format != "" {
		fallbackFmt = targetVol.Format
	}
	format := DetectDiskFormat(targetVol.Path, fallbackFmt)

	now := time.Now().UTC()
	version := ItemVersion{
		ID:          randomHex(8),
		Version:     req.Version,
		Description: req.Description,
		SHA256:      checksum,
		Size:        size,
		Format:      format,
		SourceHost:  selfHostID,
		SourcePool:  req.Pool,
		SourceVol:   req.Volume,
		Created:     now,
	}

	var existing *Item
	for _, it := range store.ListItems() {
		if strings.EqualFold(it.Name, req.Name) {
			existing = &it
			break
		}
	}

	if existing != nil {
		if existing.Type != req.Type {
			return Item{}, conflictf("an item named %q already exists with type %q", req.Name, existing.Type)
		}
		updated, _, err := store.AddVersion(existing.ID, version)
		return updated, err
	}

	item := Item{
		Name:         req.Name,
		Type:         req.Type,
		Description:  req.Description,
		SourceHost:   selfHostID,
		KeepVersions: req.KeepVersions,
		SyncPolicy:   req.SyncPolicy,
		Versions:     []ItemVersion{version},
		Hosts:        []HostSyncState{{Host: selfHostID, State: "synced", Updated: now}},
	}
	return store.CreateItem(item)
}

// PublishArtifact registers or updates a content library item and its version directly.
// If an item with matching name or ID exists, the version is appended; otherwise a new item is created.
func PublishArtifact(store *Store, item Item, version ItemVersion) (Item, error) {
	if store == nil {
		return Item{}, invalidf("store is required")
	}
	item.Name = strings.TrimSpace(item.Name)
	if item.Name == "" {
		return Item{}, invalidf("item name is required")
	}

	var existing *Item
	if item.ID != "" {
		if it, ok := store.GetItem(item.ID); ok {
			existing = &it
		}
	}
	if existing == nil {
		for _, it := range store.ListItems() {
			if strings.EqualFold(it.Name, item.Name) {
				existing = &it
				break
			}
		}
	}

	if existing != nil {
		if version.Version != "" || version.SHA256 != "" {
			if version.ID == "" {
				version.ID = randomHex(8)
			}
			if version.Created.IsZero() {
				version.Created = time.Now().UTC()
			}
			updated, _, err := store.AddVersion(existing.ID, version)
			return updated, err
		}
		return *existing, nil
	}

	now := time.Now().UTC()
	if len(item.Versions) == 0 && (version.Version != "" || version.SHA256 != "") {
		if version.ID == "" {
			version.ID = randomHex(8)
		}
		if version.Created.IsZero() {
			version.Created = now
		}
		item.Versions = []ItemVersion{version}
	}
	if len(item.Hosts) == 0 && item.SourceHost != "" {
		item.Hosts = []HostSyncState{{Host: item.SourceHost, State: "synced", Updated: now}}
	}

	created, err := store.CreateItem(item)
	if err != nil {
		return Item{}, err
	}
	return created, nil
}
