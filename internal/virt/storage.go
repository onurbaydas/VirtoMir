package virt

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	pathpkg "path"
	"regexp"
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
	Source          string `json:"source,omitempty"` // Ceph pool (rbd), volume group (logical), zpool (zfs), host:export (netfs) or target IQN (iscsi)
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
	// Writing: a running operation (restore, upload, download, clone) is writing it now.
	Writing bool `json:"writing,omitempty"`
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
			if src := x.Source; src != nil {
				switch {
				case src.Name != "":
					info.Source = src.Name
				case x.Type == "netfs" && len(src.Host) > 0 && src.Dir != nil:
					info.Source = src.Host[0].Name + ":" + src.Dir.Path
				case x.Type == "iscsi" && len(src.Device) > 0:
					info.Source = src.Device[0].Path
				}
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
	users, err := c.diskUsers(l)
	if err != nil { // still list the volumes; "used by" may be incomplete
		c.log.Warn("list volumes: which machines use them is incomplete", "pool", pool, "err", err)
	}
	out := make([]Volume, 0, len(vols))
	for _, v := range vols {
		vol, err := volumeInfo(l, v)
		if err != nil {
			continue
		}
		vol.UsedBy = users[vol.Path]
		vol.Writing = inFlight.has(vol.Path)
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
	Name         string `json:"name"`
	SizeGiB      uint64 `json:"sizeGiB"`
	Format       string `json:"format"`                 // qcow2 (default) or raw
	Provisioning string `json:"provisioning,omitempty"` // thin (default/sparse) or thick (preallocated)
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
	if !poolKeepsFiles(l, pool) {
		format = "raw" // LVM, ZFS and Ceph volumes are block storage with no format of their own
	}
	vol, _, err := createVolume(l, pool, spec.Name, spec.SizeGiB<<30, format, spec.Provisioning)
	if err != nil {
		return Volume{}, err
	}
	c.log.Info("volume created", "pool", pool, "volume", spec.Name, "provisioning", spec.Provisioning)
	return volumeInfo(l, vol)
}

func createVolume(l *libvirt.Libvirt, pool, name string, bytes uint64, format string, provisioning ...string) (libvirt.StorageVol, string, error) {
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
	prov := "thin"
	if len(provisioning) > 0 && provisioning[0] != "" {
		prov = provisioning[0]
	}
	// Thin everywhere except LVM or explicit thick provisioning:
	var alloc uint64
	if poolTypeOf(l, p) == "logical" || prov == "thick" {
		alloc = bytes
	}
	x := libvirtxml.StorageVolume{
		Name:       name,
		Capacity:   &libvirtxml.StorageVolumeSize{Unit: "bytes", Value: bytes},
		Allocation: &libvirtxml.StorageVolumeSize{Unit: "bytes", Value: alloc},
		Target:     &libvirtxml.StorageVolumeTarget{Format: &libvirtxml.StorageVolumeTargetFormat{Type: format}},
	}
	doc, err := x.Marshal()
	if err != nil {
		return libvirt.StorageVol{}, "", err
	}
	var flags libvirt.StorageVolCreateFlags
	if format == "qcow2" {
		if prov == "thick" {
			flags = 0
		} else {
			flags = libvirt.StorageVolCreatePreallocMetadata
		}
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

// cephSecret stores a cephx key in a libvirt secret (usage "virtomir/<pool>") and returns
// its UUID. The key is base64 as Ceph prints it.
func cephSecret(l *libvirt.Libvirt, pool, key string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(key))
	if err != nil || len(raw) == 0 {
		return "", invalidf("The Ceph key is the base64 string from 'ceph auth get-key client.<user>'.")
	}
	return poolSecret(l, libvirt.SecretUsageTypeCeph, "ceph", pool, "Ceph key", raw)
}

// poolSecret keeps a pool's key or password in a private libvirt secret, never in the pool
// definition, and returns its UUID.
func poolSecret(l *libvirt.Libvirt, usageType libvirt.SecretUsageType, typ, pool, what string, value []byte) (string, error) {
	usage := "virtomir/" + pool
	// A secret from an earlier attempt (the pool itself was refused) is reused: libvirt
	// refuses a second secret for the same usage.
	sec, err := l.SecretLookupByUsage(int32(usageType), usage)
	if err != nil {
		s := libvirtxml.Secret{Ephemeral: "no", Private: "yes", Description: what + " for VirtoMir pool " + pool}
		if typ == "iscsi" {
			s.Usage = &libvirtxml.SecretUsage{Type: typ, Target: usage}
		} else {
			s.Usage = &libvirtxml.SecretUsage{Type: typ, Name: usage}
		}
		doc, err := s.Marshal()
		if err != nil {
			return "", err
		}
		if sec, err = l.SecretDefineXML(doc, 0); err != nil {
			return "", classify(err)
		}
	}
	if err := l.SecretSetValue(sec, value, 0); err != nil {
		return "", classify(err)
	}
	return formatUUID(sec.UUID), nil
}

// poolTypeOf returns a pool's libvirt type (dir, netfs, logical, …), or "" if unknown.
func poolTypeOf(l *libvirt.Libvirt, p libvirt.StoragePool) string {
	raw, err := l.StoragePoolGetXMLDesc(p, 0)
	if err != nil {
		return ""
	}
	var x libvirtxml.StoragePool
	if x.Unmarshal(raw) != nil {
		return ""
	}
	return x.Type
}

// poolKeepsFiles reports whether a pool's volumes are plain files a disk can point at;
// otherwise (iSCSI, Ceph, LVM, ZFS) disks refer to them as <source pool= volume=>.
func poolKeepsFiles(l *libvirt.Libvirt, pool string) bool {
	p, err := l.StoragePoolLookupByName(pool)
	if err != nil {
		return true
	}
	switch poolTypeOf(l, p) {
	case "", "dir", "fs", "netfs":
		return true
	}
	return false
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
	if inFlight.has(path) {
		return refusedf("%s is being written by a running job (a restore, upload, download or clone). Wait until it has finished.", name)
	}
	if err := c.checkVolumeUnused(l, v, name, path); err != nil {
		return err
	}
	if err := l.StorageVolDelete(v, libvirt.StorageVolDeleteNormal); err != nil {
		return classify(err)
	}
	c.log.Info("volume deleted", "pool", pool, "volume", name)
	return nil
}

// checkVolumeUnused refuses (fails closed) unless no machine uses the volume at path and no
// other volume of its pool has it as backing image. name is how the user knows it.
func (c *Client) checkVolumeUnused(l *libvirt.Libvirt, v libvirt.StorageVol, name, path string) error {
	all, err := c.diskUsers(l)
	if err != nil {
		return refusedf("Could not check which machines use %s, so it was kept: %v", name, err)
	}
	if users := all[path]; len(users) > 0 {
		return refusedf("%s is used by %s. Remove it from the machine first.", name, strings.Join(users, ", "))
	}
	backers, err := c.volumeBackers(l, v, path)
	if err != nil {
		return refusedf("Could not check whether other images are based on %s, so it was kept: %v", name, err)
	}
	if len(backers) > 0 {
		return refusedf("%s is the base image of %s. Delete those first.", name, strings.Join(backers, ", "))
	}
	return nil
}

// volumeBackers names the other volumes in v's pool whose backing image is path (qcow2
// overlays, linked clones). Any volume that cannot be read makes it an error.
func (c *Client) volumeBackers(l *libvirt.Libvirt, v libvirt.StorageVol, path string) ([]string, error) {
	p, err := l.StoragePoolLookupByVolume(v)
	if err != nil {
		return nil, classify(err)
	}
	vols, _, err := l.StoragePoolListAllVolumes(p, 1, 0)
	if err != nil {
		return nil, classify(err)
	}
	descs := map[string]string{}
	for _, o := range vols {
		if o.Name == v.Name {
			continue
		}
		raw, err := l.StorageVolGetXMLDesc(o, 0)
		if err != nil {
			return nil, fmt.Errorf("read volume %s: %w", o.Name, classify(err))
		}
		descs[o.Name] = raw
	}
	return backedBy(path, descs)
}

// backedBy returns (sorted) the names of the volumes, given as name -> volume XML, whose
// backing store is target. A relative backing path is relative to the volume's directory.
func backedBy(target string, descs map[string]string) ([]string, error) {
	target = pathpkg.Clean(target)
	var out []string
	for name, raw := range descs {
		var x libvirtxml.StorageVolume
		if err := x.Unmarshal(raw); err != nil {
			return nil, fmt.Errorf("parse the XML of volume %s: %w", name, err)
		}
		if x.BackingStore == nil || x.BackingStore.Path == "" {
			continue
		}
		b := x.BackingStore.Path
		if !pathpkg.IsAbs(b) && x.Target != nil && x.Target.Path != "" {
			b = pathpkg.Join(pathpkg.Dir(x.Target.Path), b)
		}
		if pathpkg.Clean(b) == target {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out, nil
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
	vol, volPath, err := createVolume(l, pool, name, uint64(size), format)
	if err != nil {
		return Volume{}, err
	}
	defer inFlight.add(volPath)()
	if err := l.StorageVolUpload(vol, &ctxReader{ctx: ctx, r: io.LimitReader(br, size)}, 0, uint64(size), 0); err != nil {
		dropVolume(c.log, l, vol)
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

// PoolSpec describes a new pool: a directory, NFS share, iSCSI target, Ceph pool, LVM
// volume group or ZFS pool.
type PoolSpec struct {
	Name string `json:"name"`
	Path string `json:"path"` // directory, or where an NFS share is mounted
	// Type "dir" (default) or "netfs": an NFS share (Host:Export) mounted at Path. The
	// same NFS pool on every host is what live migration without copying disks needs.
	Type   string `json:"type,omitempty"`
	Host   string `json:"host,omitempty"`
	Export string `json:"export,omitempty"`
	// iscsi: Host is the portal, Target the IQN; the LUNs become volumes. ChapUser and
	// ChapPassword sign in to a target that asks (the password goes to a libvirt secret).
	Target       string `json:"target,omitempty"`
	ChapUser     string `json:"chapUser,omitempty"`
	ChapPassword string `json:"chapPassword,omitempty"`
	// rbd (Ceph): Monitors (host or host:port), the Ceph pool name, and the cephx user and
	// key; the key is kept in a libvirt secret, never in the pool definition.
	Monitors []string `json:"monitors,omitempty"`
	CephPool string   `json:"cephPool,omitempty"`
	CephUser string   `json:"cephUser,omitempty"`
	CephKey  string   `json:"cephKey,omitempty"`
	// logical (LVM): Source is the volume group. With Devices, a new volume group is made
	// from those empty disks; without, an existing one is used. zfs: Source is an existing
	// zpool (or dataset); VirtoMir never creates a zpool.
	Source  string   `json:"source,omitempty"`
	Devices []string `json:"devices,omitempty"`
}

// lvmNameRE is what LVM accepts for a volume group (and ZFS for a pool, minus the dataset
// slashes allowed separately).
var lvmNameRE = regexp.MustCompile(`^[A-Za-z0-9+_.][A-Za-z0-9+_.-]*$`)

var zfsNameRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.:-]*(/[A-Za-z0-9_.:-]+)*$`)

// blockDevRE: a whole disk or partition by a stable path; nothing outside /dev.
var blockDevRE = regexp.MustCompile(`^/dev/[A-Za-z0-9][A-Za-z0-9/_.:+-]*$`)

var iqnRE = regexp.MustCompile(`^(iqn\.[0-9]{4}-[0-9]{2}\.[a-z0-9.-]+(:[^\s]+)?|eui\.[0-9A-Fa-f]{16})$`)

var nfsHostRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$|^[[0-9a-fA-F:]+]$`)

// systemDirs may not be a pool directory themselves; the ones in systemTrees not even
// below them: a pool lists (and lets users delete) the files in its directory.
var (
	systemDirs  = []string{"/", "/etc", "/usr", "/bin", "/sbin", "/lib", "/lib32", "/lib64", "/libx32", "/boot", "/dev", "/proc", "/sys", "/run", "/root", "/home", "/var", "/var/lib"}
	systemTrees = []string{"/etc", "/usr", "/bin", "/sbin", "/lib", "/lib32", "/lib64", "/libx32", "/boot", "/dev", "/proc", "/sys", "/run"}
)

// checkPoolPath validates the directory of a dir or netfs pool and returns it cleaned.
// protected are directories VirtoMir keeps its own files in (state, backups): neither they
// nor anything below them can become a pool.
func checkPoolPath(p string, protected []string) (string, error) {
	if !strings.HasPrefix(p, "/") || strings.Contains(p, "..") {
		return "", invalidf("The path must be absolute, e.g. /srv/vm-images.")
	}
	p = pathpkg.Clean(p)
	if slices.Contains(systemDirs, p) {
		return "", invalidf("%s is a system directory; use a directory of its own for machine disks, e.g. /srv/vm-images.", p)
	}
	for _, t := range systemTrees {
		if strings.HasPrefix(p, t+"/") {
			return "", invalidf("%s is inside the system directory %s; use a directory of its own for machine disks, e.g. /srv/vm-images.", p, t)
		}
	}
	for _, d := range protected {
		if d == "" || !strings.HasPrefix(d, "/") {
			continue
		}
		d = pathpkg.Clean(d)
		if p == d || strings.HasPrefix(p, strings.TrimSuffix(d, "/")+"/") {
			return "", invalidf("%s holds VirtoMir's own files (%s); pick another directory for the pool.", p, d)
		}
	}
	return p, nil
}

// poolXML builds the libvirt definition for a pool spec. protected: see checkPoolPath.
func poolXML(spec PoolSpec, protected ...string) (libvirtxml.StoragePool, error) {
	if err := checkName("Pool", spec.Name); err != nil {
		return libvirtxml.StoragePool{}, err
	}
	if spec.Type == "" || spec.Type == "dir" || spec.Type == "netfs" {
		clean, err := checkPoolPath(spec.Path, protected)
		if err != nil {
			return libvirtxml.StoragePool{}, err
		}
		spec.Path = clean
	}
	x := libvirtxml.StoragePool{Type: "dir", Name: spec.Name, Target: &libvirtxml.StoragePoolTarget{Path: spec.Path}}
	switch spec.Type {
	case "", "dir":
	case "netfs":
		if !nfsHostRE.MatchString(spec.Host) {
			return x, invalidf("Enter the NFS server as a host name or IP address, e.g. 192.168.1.20.")
		}
		if !strings.HasPrefix(spec.Export, "/") || strings.Contains(spec.Export, "..") {
			return x, invalidf("The export is the shared directory on the server, e.g. /srv/vms.")
		}
		x.Type = "netfs"
		x.Source = &libvirtxml.StoragePoolSource{
			Host:   []libvirtxml.StoragePoolSourceHost{{Name: spec.Host}},
			Dir:    &libvirtxml.StoragePoolSourceDir{Path: spec.Export},
			Format: &libvirtxml.StoragePoolSourceFormat{Type: "nfs"},
		}
	case "iscsi":
		if !nfsHostRE.MatchString(spec.Host) {
			return x, invalidf("Enter the iSCSI portal as a host name or IP address.")
		}
		if !iqnRE.MatchString(spec.Target) {
			return x, invalidf("The target is an IQN like iqn.2026-01.com.example:storage.")
		}
		x.Type = "iscsi"
		x.Source = &libvirtxml.StoragePoolSource{
			Host:   []libvirtxml.StoragePoolSourceHost{{Name: spec.Host}},
			Device: []libvirtxml.StoragePoolSourceDevice{{Path: spec.Target}},
		}
		x.Target = &libvirtxml.StoragePoolTarget{Path: "/dev/disk/by-path"}
	case "rbd":
		if len(spec.Monitors) == 0 {
			return x, invalidf("Name at least one Ceph monitor.")
		}
		if err := checkName("Ceph pool", spec.CephPool); err != nil {
			return x, err
		}
		x.Type = "rbd"
		x.Target = nil
		src := &libvirtxml.StoragePoolSource{Name: spec.CephPool}
		for _, m := range spec.Monitors {
			host, port, found := strings.Cut(strings.TrimSpace(m), ":")
			if !nfsHostRE.MatchString(host) {
				return x, invalidf("%q is not a monitor address.", m)
			}
			if !found {
				port = "6789"
			}
			src.Host = append(src.Host, libvirtxml.StoragePoolSourceHost{Name: host, Port: port})
		}
		x.Source = src
	case "logical":
		if !lvmNameRE.MatchString(spec.Source) || spec.Source == "." || spec.Source == ".." || len(spec.Source) > 127 {
			return x, invalidf("Enter the volume group name as vgs shows it, e.g. vg-fast.")
		}
		x.Type = "logical"
		x.Source = &libvirtxml.StoragePoolSource{Name: spec.Source, Format: &libvirtxml.StoragePoolSourceFormat{Type: "lvm2"}}
		seen := map[string]bool{}
		for _, d := range spec.Devices {
			d = strings.TrimSpace(d)
			if !blockDevRE.MatchString(d) || strings.Contains(d, "..") {
				return x, invalidf("%q is not a disk; use a path like /dev/sdb or /dev/disk/by-id/….", d)
			}
			if seen[d] {
				return x, invalidf("%s is listed twice.", d)
			}
			seen[d] = true
			x.Source.Device = append(x.Source.Device, libvirtxml.StoragePoolSourceDevice{Path: d})
		}
		x.Target = &libvirtxml.StoragePoolTarget{Path: "/dev/" + spec.Source}
	case "zfs":
		if !zfsNameRE.MatchString(spec.Source) || len(spec.Source) > 255 || slices.ContainsFunc(strings.Split(spec.Source, "/"), func(p string) bool { return p == "." || p == ".." }) {
			return x, invalidf("Enter the zpool (or dataset) name as zfs list shows it, e.g. tank/vms.")
		}
		if len(spec.Devices) > 0 {
			return x, invalidf("VirtoMir uses an existing zpool; create it with zpool create first.")
		}
		x.Type = "zfs"
		x.Source = &libvirtxml.StoragePoolSource{Name: spec.Source}
		x.Target = nil
	default:
		return x, invalidf("Unknown pool type %q (dir, netfs, iscsi, rbd, logical or zfs).", spec.Type)
	}
	return x, nil
}

// CreatePool defines, builds (creates the directory, mount point or volume group), starts
// (mounts) and autostarts a pool.
func (c *Client) CreatePool(spec PoolSpec) error {
	var protected []string
	if c.ProtectedDirs != nil {
		protected = c.ProtectedDirs()
	}
	x, err := poolXML(spec, protected...)
	if err != nil {
		return err
	}
	l, err := c.conn()
	if err != nil {
		return err
	}
	if spec.Type == "rbd" && spec.CephUser != "" {
		uuid, err := cephSecret(l, spec.Name, spec.CephKey)
		if err != nil {
			return err
		}
		x.Source.Auth = &libvirtxml.StoragePoolSourceAuth{Type: "ceph", Username: spec.CephUser,
			Secret: &libvirtxml.StoragePoolSourceAuthSecret{UUID: uuid}}
	}
	if spec.Type == "iscsi" && spec.ChapUser != "" {
		uuid, err := poolSecret(l, libvirt.SecretUsageTypeIscsi, "iscsi", spec.Name, "iSCSI CHAP password", []byte(spec.ChapPassword))
		if err != nil {
			return err
		}
		x.Source.Auth = &libvirtxml.StoragePoolSourceAuth{Type: "chap", Username: spec.ChapUser,
			Secret: &libvirtxml.StoragePoolSourceAuthSecret{UUID: uuid}}
	}
	doc, err := x.Marshal()
	if err != nil {
		return err
	}
	p, err := l.StoragePoolDefineXML(doc, 0)
	if err != nil {
		if strings.Contains(err.Error(), "missing backend for pool type") {
			return refusedf("This host's libvirt cannot use %s pools yet: install its storage driver (e.g. libvirt-daemon-driver-storage-%s on Debian/Ubuntu) and restart libvirtd.", x.Type, x.Type)
		}
		return classify(err)
	}
	// Only directory-like pools and a new volume group have something to build (iSCSI,
	// Ceph, ZFS and an existing volume group exist already). For LVM, NoOverwrite makes
	// libvirt refuse any disk that carries a partition table, file system or LVM label.
	build := x.Type == "dir" || x.Type == "netfs" || (x.Type == "logical" && len(x.Source.Device) > 0)
	if build {
		flags := libvirt.StoragePoolBuildNew
		if x.Type == "logical" {
			flags = libvirt.StoragePoolBuildNoOverwrite
		}
		if err := l.StoragePoolBuild(p, flags); err != nil {
			_ = l.StoragePoolUndefine(p)
			if x.Type == "logical" && strings.Contains(err.Error(), "already built") {
				return refusedf("A disk is not empty (it has a partition table, file system or LVM label), so it was left untouched. Wipe it yourself if you are sure (wipefs -a), or use its existing volume group. libvirt: %v", err)
			}
			return classify(err)
		}
	}
	if err := l.StoragePoolCreate(p, 0); err != nil {
		_ = l.StoragePoolUndefine(p)
		return classify(err)
	}
	_ = l.StoragePoolSetAutostart(p, 1)
	c.log.Info("pool created", "pool", spec.Name, "type", x.Type, "path", spec.Path)
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

// DeleteStorageVol removes a storage volume from libvirt.
func (c *Client) DeleteStorageVol(vol libvirt.StorageVol) error {
	l, err := c.conn()
	if err != nil {
		return err
	}
	return l.StorageVolDelete(vol, libvirt.StorageVolDeleteNormal)
}

// CreateVolumeFile creates a new volume in pool and returns the volume descriptor and path.
func (c *Client) CreateVolumeFile(pool, name string, bytes uint64, format string, provisioning ...string) (libvirt.StorageVol, string, error) {
	l, err := c.conn()
	if err != nil {
		return libvirt.StorageVol{}, "", err
	}
	return createVolume(l, pool, name, bytes, format, provisioning...)
}
