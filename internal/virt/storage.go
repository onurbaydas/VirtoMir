package virt

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"
)

// Pool is a libvirt storage pool (usually a directory such as /var/lib/libvirt/images).
type Pool struct {
	Name            string `json:"name"`
	UUID            string `json:"uuid"`
	Type            string `json:"type"` // dir, logical, netfs, iscsi, …
	Path            string `json:"path,omitempty"`
	Active          bool   `json:"active"`
	Autostart       bool   `json:"autostart"`
	CapacityBytes   uint64 `json:"capacityBytes"`
	AllocationBytes uint64 `json:"allocationBytes"`
	AvailableBytes  uint64 `json:"availableBytes"`
	Volumes         int    `json:"volumes"`
}

// Volume is one image in a pool.
type Volume struct {
	Name            string   `json:"name"`
	Path            string   `json:"path"`
	Type            string   `json:"type"`   // file, block, dir, network
	Format          string   `json:"format"` // qcow2, raw, iso, …
	CapacityBytes   uint64   `json:"capacityBytes"`
	AllocationBytes uint64   `json:"allocationBytes"`
	Backing         string   `json:"backing,omitempty"`
	UsedBy          []string `json:"usedBy"`
}

func (c *Client) ListPools() ([]Pool, error) {
	l, err := c.conn()
	if err != nil {
		return nil, err
	}
	pools, _, err := l.ConnectListAllStoragePools(1, 0)
	if err != nil {
		return nil, classify(err)
	}
	out := make([]Pool, 0, len(pools))
	for _, p := range pools {
		info, err := poolInfo(l, p)
		if err != nil {
			c.log.Debug("skipping pool", "pool", p.Name, "err", err)
			continue
		}
		out = append(out, info)
	}
	slices.SortFunc(out, func(a, b Pool) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func poolInfo(l *libvirt.Libvirt, p libvirt.StoragePool) (Pool, error) {
	info := Pool{Name: p.Name, UUID: formatUUID(p.UUID)}
	state, capacity, alloc, avail, err := l.StoragePoolGetInfo(p)
	if err != nil {
		return info, classify(err)
	}
	info.Active = state == 2 // VIR_STORAGE_POOL_RUNNING
	info.CapacityBytes, info.AllocationBytes, info.AvailableBytes = capacity, alloc, avail
	if a, err := l.StoragePoolGetAutostart(p); err == nil {
		info.Autostart = a == 1
	}
	if raw, err := l.StoragePoolGetXMLDesc(p, 0); err == nil {
		var x libvirtxml.StoragePool
		if x.Unmarshal(raw) == nil {
			info.Type = x.Type
			if x.Target != nil {
				info.Path = x.Target.Path
			}
		}
	}
	if info.Active {
		if vols, _, err := l.StoragePoolListAllVolumes(p, 1, 0); err == nil {
			info.Volumes = len(vols)
		}
	}
	return info, nil
}

// ListVolumes lists a pool's volumes with the machines that use each of them.
func (c *Client) ListVolumes(pool string) ([]Volume, error) {
	l, err := c.conn()
	if err != nil {
		return nil, err
	}
	p, err := l.StoragePoolLookupByName(pool)
	if err != nil {
		return nil, classify(err)
	}
	vols, _, err := l.StoragePoolListAllVolumes(p, 1, 0)
	if err != nil {
		return nil, classify(err)
	}
	users := c.diskUsers(l)
	out := make([]Volume, 0, len(vols))
	for _, v := range vols {
		vol, err := volumeInfo(l, v)
		if err != nil {
			continue
		}
		vol.UsedBy = users[vol.Path]
		if vol.UsedBy == nil {
			vol.UsedBy = []string{}
		}
		out = append(out, vol)
	}
	slices.SortFunc(out, func(a, b Volume) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

var volTypes = map[int8]string{0: "file", 1: "block", 2: "dir", 3: "network", 4: "netdir", 5: "ploop"}

func volumeInfo(l *libvirt.Libvirt, v libvirt.StorageVol) (Volume, error) {
	vol := Volume{Name: v.Name, Path: v.Key}
	typ, capacity, alloc, err := l.StorageVolGetInfo(v)
	if err != nil {
		return vol, classify(err)
	}
	vol.Type, vol.CapacityBytes, vol.AllocationBytes = volTypes[typ], capacity, alloc
	if p, err := l.StorageVolGetPath(v); err == nil {
		vol.Path = p
	}
	if raw, err := l.StorageVolGetXMLDesc(v, 0); err == nil {
		var x libvirtxml.StorageVolume
		if x.Unmarshal(raw) == nil {
			if x.Target != nil && x.Target.Format != nil {
				vol.Format = x.Target.Format.Type
			}
			if x.BackingStore != nil {
				vol.Backing = x.BackingStore.Path
			}
		}
	}
	if vol.Format == "raw" && strings.HasSuffix(strings.ToLower(vol.Name), ".iso") {
		vol.Format = "iso"
	}
	return vol, nil
}

// VolumeSpec describes a new empty volume.
type VolumeSpec struct {
	Name    string `json:"name"`
	SizeGiB uint64 `json:"sizeGiB"`
	Format  string `json:"format"` // qcow2 (default) or raw
}

func (c *Client) CreateVolume(pool string, spec VolumeSpec) (Volume, error) {
	if err := checkName("Volume", spec.Name); err != nil {
		return Volume{}, err
	}
	if spec.SizeGiB == 0 || spec.SizeGiB > 64<<10 {
		return Volume{}, invalidf("Size must be between 1 GiB and 64 TiB.")
	}
	l, err := c.conn()
	if err != nil {
		return Volume{}, err
	}
	format := orDefault(spec.Format, "qcow2")
	vol, _, err := createVolume(l, pool, spec.Name, spec.SizeGiB<<30, format)
	if err != nil {
		return Volume{}, err
	}
	c.log.Info("volume created", "pool", pool, "volume", spec.Name)
	return volumeInfo(l, vol)
}

func createVolume(l *libvirt.Libvirt, pool, name string, bytes uint64, format string) (libvirt.StorageVol, string, error) {
	if format != "qcow2" && format != "raw" {
		return libvirt.StorageVol{}, "", invalidf("Format must be qcow2 or raw.")
	}
	p, err := l.StoragePoolLookupByName(pool)
	if err != nil {
		return libvirt.StorageVol{}, "", classify(err)
	}
	if _, err := l.StorageVolLookupByName(p, name); err == nil {
		return libvirt.StorageVol{}, "", refusedf("Pool %s already has a volume named %s.", pool, name)
	}
	x := libvirtxml.StorageVolume{
		Name:       name,
		Capacity:   &libvirtxml.StorageVolumeSize{Unit: "bytes", Value: bytes},
		Allocation: &libvirtxml.StorageVolumeSize{Unit: "bytes", Value: 0},
		Target:     &libvirtxml.StorageVolumeTarget{Format: &libvirtxml.StorageVolumeTargetFormat{Type: format}},
	}
	doc, err := x.Marshal()
	if err != nil {
		return libvirt.StorageVol{}, "", err
	}
	var flags libvirt.StorageVolCreateFlags
	if format == "qcow2" {
		flags = libvirt.StorageVolCreatePreallocMetadata
	}
	vol, err := l.StorageVolCreateXML(p, doc, flags)
	if err != nil {
		return libvirt.StorageVol{}, "", classify(err)
	}
	path, err := l.StorageVolGetPath(vol)
	if err != nil {
		return libvirt.StorageVol{}, "", classify(err)
	}
	return vol, path, nil
}

func volumePath(l *libvirt.Libvirt, pool, name string) (path, format string, err error) {
	p, err := l.StoragePoolLookupByName(pool)
	if err != nil {
		return "", "", classify(err)
	}
	v, err := l.StorageVolLookupByName(p, name)
	if err != nil {
		return "", "", classify(err)
	}
	info, err := volumeInfo(l, v)
	if err != nil {
		return "", "", err
	}
	if info.Format == "iso" {
		info.Format = "raw"
	}
	return info.Path, info.Format, nil
}

// DeleteVolume removes a volume that no machine uses.
func (c *Client) DeleteVolume(pool, name string) error {
	l, err := c.conn()
	if err != nil {
		return err
	}
	p, err := l.StoragePoolLookupByName(pool)
	if err != nil {
		return classify(err)
	}
	v, err := l.StorageVolLookupByName(p, name)
	if err != nil {
		return classify(err)
	}
	path, err := l.StorageVolGetPath(v)
	if err != nil {
		return classify(err)
	}
	if users := c.diskUsers(l)[path]; len(users) > 0 {
		return refusedf("%s is used by %s. Remove it from the machine first.", name, strings.Join(users, ", "))
	}
	if err := l.StorageVolDelete(v, libvirt.StorageVolDeleteNormal); err != nil {
		return classify(err)
	}
	c.log.Info("volume deleted", "pool", pool, "volume", name)
	return nil
}

// UploadVolume streams size bytes from r into a new volume. QCOW2 images (cloud images)
// are recognized by their magic bytes and stored as qcow2; everything else (ISOs, raw
// disks) as raw. A failed or cancelled upload leaves nothing behind.
func (c *Client) UploadVolume(ctx context.Context, pool, name string, size int64, r io.Reader) (Volume, error) {
	if err := checkName("Volume", name); err != nil {
		return Volume{}, err
	}
	if size <= 0 {
		return Volume{}, invalidf("The upload is empty or its size is unknown.")
	}
	l, err := c.conn()
	if err != nil {
		return Volume{}, err
	}
	br := bufio.NewReader(r)
	format := "raw"
	if magic, _ := br.Peek(4); bytes.Equal(magic, qcow2Magic) {
		format = "qcow2"
	}
	vol, _, err := createVolume(l, pool, name, uint64(size), format)
	if err != nil {
		return Volume{}, err
	}
	if err := l.StorageVolUpload(vol, &ctxReader{ctx: ctx, r: io.LimitReader(br, size)}, 0, uint64(size), 0); err != nil {
		_ = l.StorageVolDelete(vol, libvirt.StorageVolDeleteNormal)
		if ctx.Err() != nil {
			return Volume{}, ctx.Err()
		}
		return Volume{}, classify(err)
	}
	// The uploaded qcow2 header carries its own virtual size; let libvirt re-read it.
	if p, err := l.StoragePoolLookupByName(pool); err == nil {
		_ = l.StoragePoolRefresh(p, 0)
		if v, err := l.StorageVolLookupByName(p, name); err == nil {
			vol = v
		}
	}
	c.log.Info("volume uploaded", "pool", pool, "volume", name, "bytes", size, "format", format)
	return volumeInfo(l, vol)
}

var qcow2Magic = []byte{'Q', 'F', 'I', 0xfb}

// ctxReader stops a long upload when the client goes away.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// PoolAction starts, stops or refreshes (rescans) a pool.
func (c *Client) PoolAction(name, action string) error {
	l, err := c.conn()
	if err != nil {
		return err
	}
	p, err := l.StoragePoolLookupByName(name)
	if err != nil {
		return classify(err)
	}
	switch action {
	case "start":
		err = l.StoragePoolCreate(p, 0)
	case "stop":
		err = l.StoragePoolDestroy(p)
	case "refresh":
		err = l.StoragePoolRefresh(p, 0)
	default:
		return fmt.Errorf("%w: unknown action %s", ErrNotFound, action)
	}
	return classify(err)
}

func (c *Client) SetPoolAutostart(name string, on bool) error {
	l, err := c.conn()
	if err != nil {
		return err
	}
	p, err := l.StoragePoolLookupByName(name)
	if err != nil {
		return classify(err)
	}
	return classify(l.StoragePoolSetAutostart(p, boolInt(on)))
}

// PoolSpec describes a new directory pool.
type PoolSpec struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// CreatePool defines, builds (creates the directory), starts and autostarts a directory pool.
func (c *Client) CreatePool(spec PoolSpec) error {
	if err := checkName("Pool", spec.Name); err != nil {
		return err
	}
	if !strings.HasPrefix(spec.Path, "/") || strings.Contains(spec.Path, "..") {
		return invalidf("The path must be absolute, e.g. /srv/vm-images.")
	}
	l, err := c.conn()
	if err != nil {
		return err
	}
	x := libvirtxml.StoragePool{Type: "dir", Name: spec.Name, Target: &libvirtxml.StoragePoolTarget{Path: spec.Path}}
	doc, err := x.Marshal()
	if err != nil {
		return err
	}
	p, err := l.StoragePoolDefineXML(doc, 0)
	if err != nil {
		return classify(err)
	}
	if err := l.StoragePoolBuild(p, libvirt.StoragePoolBuildNew); err != nil {
		_ = l.StoragePoolUndefine(p)
		return classify(err)
	}
	if err := l.StoragePoolCreate(p, 0); err != nil {
		_ = l.StoragePoolUndefine(p)
		return classify(err)
	}
	_ = l.StoragePoolSetAutostart(p, 1)
	c.log.Info("pool created", "pool", spec.Name, "path", spec.Path)
	return nil
}

// DeletePool forgets a stopped pool. The directory and its files stay on disk.
func (c *Client) DeletePool(name string) error {
	l, err := c.conn()
	if err != nil {
		return err
	}
	p, err := l.StoragePoolLookupByName(name)
	if err != nil {
		return classify(err)
	}
	if a, err := l.StoragePoolIsActive(p); err == nil && a == 1 {
		return refusedf("Stop pool %s before removing it.", name)
	}
	return classify(l.StoragePoolUndefine(p))
}
