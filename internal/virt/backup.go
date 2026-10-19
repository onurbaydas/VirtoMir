package virt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"
)

// Backup is one full backup of a machine: its definition plus a copy of every own disk,
// in <dir>/<machine>-<uuid8>/<timestamp>/ with a manifest.json describing it.
type Backup struct {
	ID          string       `json:"id"` // "<machine dir>/<timestamp>"
	Domain      string       `json:"domain"`
	DomainUUID  string       `json:"domainUuid"`
	Created     time.Time    `json:"created"`
	Live        bool         `json:"live"`      // taken while running (libvirt backup job)
	Automatic   bool         `json:"automatic"` // made by a schedule (subject to its retention)
	Disks       []BackupDisk `json:"disks"`
	SizeBytes   uint64       `json:"sizeBytes"`
	DurationSec float64      `json:"durationSec"`
	Note        string       `json:"note,omitempty"`
	// Incremental: its disk files hold only what changed since Parent (another backup ID of
	// the same machine) and build on Parent's files. Checkpoint is the libvirt checkpoint
	// taken with it, which the next incremental backup starts from (see backup_incr.go).
	Incremental bool   `json:"incremental,omitempty"`
	Parent      string `json:"parent,omitempty"`
	Checkpoint  string `json:"checkpoint,omitempty"`
}

type BackupDisk struct {
	Target    string `json:"target"`
	File      string `json:"file"` // relative to the backup directory
	Source    string `json:"source"`
	SizeBytes uint64 `json:"sizeBytes"`
}

const manifestName = "manifest.json"

// BackupDir is where backups go; it must be on a disk with room (NFS mounts work).
type BackupDir string

func (b BackupDir) check() error {
	if !filepath.IsAbs(string(b)) {
		return invalidf("The backup location must be an absolute path.")
	}
	return nil
}

// ListBackups scans the backup directory; uuid "" lists all machines.
func (c *Client) ListBackups(dir BackupDir, uuid string) ([]Backup, error) {
	out := []Backup{}
	if err := dir.check(); err != nil {
		return out, err
	}
	matches, err := filepath.Glob(filepath.Join(string(dir), "*", "*", manifestName))
	if err != nil {
		return out, err
	}
	for _, m := range matches {
		b, err := readManifest(m)
		if err != nil || (uuid != "" && b.DomainUUID != uuid) {
			continue
		}
		rel, _ := filepath.Rel(string(dir), filepath.Dir(m))
		b.ID = filepath.ToSlash(rel)
		out = append(out, b)
	}
	slices.SortFunc(out, func(a, b Backup) int { return b.Created.Compare(a.Created) })
	return out, nil
}

func readManifest(path string) (Backup, error) {
	var b Backup
	raw, err := os.ReadFile(path)
	if err != nil {
		return b, err
	}
	return b, json.Unmarshal(raw, &b)
}

func writeManifest(dest string, b Backup) error {
	raw, _ := json.MarshalIndent(b, "", "  ")
	return os.WriteFile(filepath.Join(dest, manifestName), raw, 0o640)
}

// backupPath resolves a backup ID inside dir, refusing anything that escapes it.
func backupPath(dir BackupDir, id string) (string, error) {
	p := filepath.Join(string(dir), filepath.FromSlash(id))
	rel, err := filepath.Rel(string(dir), p)
	if err != nil || strings.HasPrefix(rel, "..") || strings.Count(filepath.ToSlash(rel), "/") != 1 {
		return "", fmt.Errorf("%w: no backup %s", ErrNotFound, id)
	}
	if _, err := os.Stat(filepath.Join(p, manifestName)); err != nil {
		return "", fmt.Errorf("%w: no backup %s", ErrNotFound, id)
	}
	return p, nil
}

// DeleteBackup removes one backup directory.
func (c *Client) DeleteBackup(dir BackupDir, id string) error {
	p, err := backupPath(dir, id)
	if err != nil {
		return err
	}
	if deps := dependents(dir, id); len(deps) > 0 {
		return refusedf("Incremental backup %s builds on %s. Delete the newer backups of its chain first.", deps[0], id)
	}
	if err := os.RemoveAll(p); err != nil {
		return err
	}
	_ = os.Remove(filepath.Dir(p)) // the machine's folder, if now empty
	c.log.Info("backup deleted", "backup", id)
	return nil
}

// backupDisks are the disks worth backing up: the machine's own writable disks.
func backupDisks(x *libvirtxml.Domain) []libvirtxml.DomainDisk {
	var out []libvirtxml.DomainDisk
	if x.Devices == nil {
		return out
	}
	for _, d := range x.Devices.Disks {
		if orDefault(d.Device, "disk") != "disk" || d.ReadOnly != nil || d.Shareable != nil || d.Target == nil {
			continue
		}
		if src, _ := diskSource(d.Source); src == "" {
			continue
		}
		out = append(out, d)
	}
	return out
}

func machineFolder(name, uuid string) string {
	return fmt.Sprintf("%s-%s", name, strings.ReplaceAll(uuid, "-", "")[:8])
}

// BackupDomain takes a backup. Running machines are backed up live and consistently with
// libvirt's backup job (no downtime, no snapshot left behind) — incrementally when opts and
// the machine allow it (see backup_incr.go); stopped ones are copied in full with qemu-img.
// Progress goes to report.
func (c *Client) BackupDomain(ctx context.Context, dir BackupDir, uuid string, opts BackupOptions, report func(string)) (Backup, error) {
	if err := dir.check(); err != nil {
		return Backup{}, err
	}
	l, d, err := c.lookup(uuid)
	if err != nil {
		return Backup{}, err
	}
	sum, err := summary(l, d)
	if err != nil {
		return Backup{}, classify(err)
	}
	x, err := c.parseDomain(l, d, true)
	if err != nil {
		return Backup{}, err
	}
	disks := backupDisks(x)
	if len(disks) == 0 {
		return Backup{}, refusedf("%s has no disks to back up.", d.Name)
	}
	started := time.Now()
	stamp := started.UTC().Format("20060102-150405")
	dest := filepath.Join(string(dir), machineFolder(d.Name, formatUUID(d.UUID)), stamp)
	if err := os.MkdirAll(dest, 0o750); err != nil {
		return Backup{}, fmt.Errorf("create backup folder: %w", err)
	}
	fail := func(err error) (Backup, error) {
		_ = os.RemoveAll(dest)
		return Backup{}, err
	}

	b := Backup{Domain: d.Name, DomainUUID: formatUUID(d.UUID), Created: started.UTC(), Automatic: opts.Automatic}
	qcow2 := true
	for _, disk := range disks {
		src, _ := diskSource(disk.Source)
		b.Disks = append(b.Disks, BackupDisk{Target: disk.Target.Dev, File: disk.Target.Dev + ".qcow2", Source: src})
		qcow2 = qcow2 && diskFormat(disk) == "qcow2"
	}

	if isOn(sum.State) || sum.State == StatePaused {
		b.Live = true
		// Only qcow2 disks can keep the dirty bitmap a checkpoint needs.
		var parent Backup
		incremental := false
		if qcow2 {
			b.Checkpoint = checkpointPrefix + stamp
			if earlier, err := c.ListBackups(dir, b.DomainUUID); err == nil {
				parent, incremental = pickParent(earlier, checkpointNames(l, d), b.Disks, opts.FullEvery, func(id string) bool {
					return backupFilesExist(dir, earlier, id)
				})
			}
		}
		// QEMU writes the copy itself, as its own unprivileged user.
		uid, gid, ok := qemuOwner(l, d)
		if ok {
			if err := letQEMUWrite(dir, dest, uid, gid); err != nil {
				return fail(err)
			}
		}
		from := ""
		if incremental {
			from = parent.Checkpoint
		}
		err := c.liveBackup(ctx, l, d, dest, b.Disks, from, b.Checkpoint, report)
		if ok {
			reclaim(dest)
		}
		if err == nil && incremental {
			b.Incremental, b.Parent = true, parent.ID
			err = rebaseOnParent(ctx, dest, b.Disks, parent)
		}
		if err != nil {
			if b.Checkpoint != "" {
				if cp, cerr := l.DomainCheckpointLookupByName(d, b.Checkpoint, 0); cerr == nil {
					_ = l.DomainCheckpointDelete(cp, 0)
				}
			}
			return fail(err)
		}
	} else {
		for i, bd := range b.Disks {
			report(fmt.Sprintf("Copying disk %s (%d of %d)…", bd.Target, i+1, len(b.Disks)))
			// Opened as the format libvirt uses, never probed (see imagefmt.go).
			format := diskFormat(disks[i])
			if _, err := checkedImage(ctx, bd.Source, format, true); err != nil {
				return fail(fmt.Errorf("copy %s: %w", bd.Target, err))
			}
			cmd := exec.CommandContext(ctx, "qemu-img", convertArgs(format, bd.Source, filepath.Join(dest, bd.File), true)...)
			if out, err := cmd.CombinedOutput(); err != nil {
				return fail(fmt.Errorf("copy %s: %v: %s", bd.Target, err, strings.TrimSpace(string(out))))
			}
		}
	}

	// The definition, without live-only details, so a restore is a normal define.
	inactive, err := l.DomainGetXMLDesc(d, libvirt.DomainXMLInactive)
	if err != nil {
		return fail(classify(err))
	}
	if err := os.WriteFile(filepath.Join(dest, "domain.xml"), []byte(inactive), 0o640); err != nil {
		return fail(err)
	}
	for i := range b.Disks {
		if st, err := os.Stat(filepath.Join(dest, b.Disks[i].File)); err == nil {
			b.Disks[i].SizeBytes = uint64(st.Size())
			b.SizeBytes += uint64(st.Size())
		}
	}
	b.DurationSec = time.Since(started).Seconds()
	if err := writeManifest(dest, b); err != nil {
		return fail(err)
	}
	rel, _ := filepath.Rel(string(dir), dest)
	b.ID = filepath.ToSlash(rel)
	// The new checkpoint is the base of the next incremental backup; older ones go.
	if b.Checkpoint != "" {
		c.dropCheckpoints(l, d, b.Checkpoint)
	}
	c.log.Info("backup done", "domain", d.Name, "live", b.Live, "incremental", b.Incremental, "bytes", b.SizeBytes, "seconds", int(b.DurationSec))
	return b, nil
}

// backupFilesExist reports whether backup id (one of all) still has its manifest and disks.
func backupFilesExist(dir BackupDir, all []Backup, id string) bool {
	i := slices.IndexFunc(all, func(b Backup) bool { return b.ID == id })
	if i < 0 {
		return false
	}
	for _, bd := range all[i].Disks {
		if _, err := os.Stat(filepath.Join(string(dir), filepath.FromSlash(id), bd.File)); err != nil {
			return false
		}
	}
	return true
}

// backupXML is the push-mode <domainbackup> that writes each disk to a qcow2 file in dest.
func backupXML(dest string, disks []BackupDisk) string {
	var xb strings.Builder
	xb.WriteString("<domainbackup mode='push'><disks>")
	for _, bd := range disks {
		fmt.Fprintf(&xb, "<disk name='%s' backup='yes' type='file'><target file='%s'/><driver type='qcow2'/></disk>",
			xmlEscape(bd.Target), xmlEscape(filepath.Join(dest, bd.File)))
	}
	xb.WriteString("</disks></domainbackup>")
	return xb.String()
}

// liveBackup runs a libvirt push-mode backup job into dest and waits for it: incremental
// since checkpoint from unless that is "", taking checkpoint cp unless that is "".
func (c *Client) liveBackup(ctx context.Context, l *libvirt.Libvirt, d libvirt.Domain, dest string, disks []BackupDisk, from, cp string, report func(string)) error {
	if from != "" {
		report("Starting incremental live backup…")
	} else {
		report("Starting live backup…")
	}
	var cpXML libvirt.OptString
	if cp != "" {
		cpXML = libvirt.OptString{checkpointXML(cp, disks)}
	}
	// Application consistency: with a guest agent, freeze the guest's filesystems only for
	// the moment the job starts. The backup copies that point in time, so the guest thaws
	// right away instead of staying frozen for the whole copy.
	_, freezeErr := l.DomainFsfreeze(d, nil, 0)
	err := l.DomainBackupBegin(d, backupXMLFrom(dest, disks, from), cpXML, 0)
	if freezeErr == nil {
		_, _ = l.DomainFsthaw(d, nil, 0)
	}
	if err != nil {
		return classify(err)
	}
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = l.DomainAbortJob(d)
			return ctx.Err()
		case <-t.C:
		}
		typ, params, err := l.DomainGetJobStats(d, 0)
		if err != nil {
			return classify(err)
		}
		if libvirt.DomainJobType(typ) == libvirt.DomainJobNone {
			break
		}
		var done, total uint64
		for _, p := range params {
			switch p.Field {
			case "disk_processed":
				done = paramUint(p.Value)
			case "disk_total":
				total = paramUint(p.Value)
			}
		}
		if total > 0 {
			report(fmt.Sprintf("Backing up live: %d %% of %s", done*100/total, humanBytes(total)))
		}
	}
	// The job ended; ask how.
	typ, params, err := l.DomainGetJobStats(d, libvirt.DomainJobStatsCompleted)
	if err != nil {
		return classify(err)
	}
	if libvirt.DomainJobType(typ) != libvirt.DomainJobCompleted {
		msg := "the backup job did not complete"
		for _, p := range params {
			if p.Field == "errmsg" {
				msg = paramString(p.Value)
			}
		}
		return errors.New(msg)
	}
	return nil
}

// qemuOwner is the user and group the machine's QEMU process runs as (libvirt's DAC label,
// e.g. "+64055:+64055"). ok is false when QEMU runs as root or the label is unknown.
func qemuOwner(l *libvirt.Libvirt, d libvirt.Domain) (uid, gid int, ok bool) {
	raw, err := l.DomainGetXMLDesc(d, 0)
	if err != nil {
		return 0, 0, false
	}
	var x libvirtxml.Domain
	if x.Unmarshal(raw) != nil {
		return 0, 0, false
	}
	for _, s := range x.SecLabel {
		if s.Model == "dac" {
			return parseDACLabel(s.Label)
		}
	}
	return 0, 0, false
}

func parseDACLabel(label string) (uid, gid int, ok bool) {
	u, g, found := strings.Cut(label, ":")
	if !found {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(strings.TrimPrefix(u, "+")+" "+strings.TrimPrefix(g, "+"), "%d %d", &uid, &gid); err != nil {
		return 0, 0, false
	}
	return uid, gid, uid != 0
}

// letQEMUWrite hands the backup folder to QEMU's user for the duration of a live backup.
// The folders above it inside the backup location only get the pass-through bit (o+x, no
// listing); folders above the backup location are not touched; if QEMU cannot pass one of
// them, the user is told which and how to fix it.
func letQEMUWrite(dir BackupDir, dest string, uid, gid int) error {
	root := filepath.Clean(string(dir))
	for p := filepath.Dir(dest); ; p = filepath.Dir(p) {
		st, err := os.Stat(p)
		if err != nil {
			return err
		}
		if !canPass(st, uid, gid) {
			inside := p == root || strings.HasPrefix(p, root+string(filepath.Separator))
			if !inside {
				return refusedf("QEMU (user %d) cannot reach the backup location because it may not pass through %s. "+
					"Allow it with: chmod o+x %s", uid, p, p)
			}
			if err := os.Chmod(p, st.Mode().Perm()|0o001); err != nil {
				return err
			}
		}
		if parent := filepath.Dir(p); parent == p {
			break
		}
	}
	if err := os.Chown(dest, uid, gid); err != nil {
		return fmt.Errorf("prepare backup folder: %w", err)
	}
	return os.Chmod(dest, 0o700)
}

// reclaim gives the backup folder and its files back to root, readable only by root's group.
func reclaim(dest string) {
	_ = os.Chown(dest, 0, 0)
	_ = os.Chmod(dest, 0o750)
	entries, _ := os.ReadDir(dest)
	for _, e := range entries {
		p := filepath.Join(dest, e.Name())
		_ = os.Chown(p, 0, 0)
		_ = os.Chmod(p, 0o640)
	}
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "'", "&apos;", `"`, "&quot;").Replace(s)
}

// RestoreBackup creates a new machine from a backup: disks are copied into pool, the
// definition gets the new name, a new UUID and new MAC addresses. The original machine
// (if it still exists) is never touched.
func (c *Client) RestoreBackup(ctx context.Context, dir BackupDir, id, name, pool string, report func(string)) (string, error) {
	if err := checkName("Machine", name); err != nil {
		return "", err
	}
	p, err := backupPath(dir, id)
	if err != nil {
		return "", err
	}
	b, err := readManifest(filepath.Join(p, manifestName))
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(filepath.Join(p, "domain.xml"))
	if err != nil {
		return "", err
	}
	var x libvirtxml.Domain
	if err := x.Unmarshal(string(raw)); err != nil {
		return "", err
	}
	l, err := c.conn()
	if err != nil {
		return "", err
	}
	if _, err := l.DomainLookupByName(name); err == nil {
		return "", refusedf("A machine named %s already exists.", name)
	}
	sp, err := l.StoragePoolLookupByName(pool)
	if err != nil {
		return "", classify(err)
	}
	praw, err := l.StoragePoolGetXMLDesc(sp, 0)
	if err != nil {
		return "", classify(err)
	}
	var px libvirtxml.StoragePool
	if err := px.Unmarshal(praw); err != nil || px.Target == nil || px.Target.Path == "" || px.Type != "dir" {
		return "", invalidf("Restore into a directory pool (pool %s is not one).", pool)
	}

	prepareClone(&x, name)
	var written []string
	rollback := func() {
		for _, f := range written {
			_ = os.Remove(f)
		}
		_ = l.StoragePoolRefresh(sp, 0)
	}
	for i, bd := range b.Disks {
		dst := filepath.Join(px.Target.Path, fmt.Sprintf("%s-%s.qcow2", name, bd.Target))
		if _, err := os.Stat(dst); err == nil {
			rollback()
			return "", refusedf("%s already exists in pool %s.", filepath.Base(dst), pool)
		}
		report(fmt.Sprintf("Restoring disk %s (%d of %d)…", bd.Target, i+1, len(b.Disks)))
		// Backups are qcow2 files VirtoMir wrote: standalone, or for an incremental backup a
		// chain inside the machine's backup folder; anything else is refused.
		src := filepath.Join(p, bd.File)
		chain, err := checkedImage(ctx, src, "qcow2", b.Incremental)
		if err == nil {
			err = chainInside(filepath.Dir(p), chain)
		}
		if err != nil {
			rollback()
			return "", fmt.Errorf("restore %s: %w", bd.Target, err)
		}
		cmd := exec.CommandContext(ctx, "qemu-img", convertArgs("qcow2", src, dst, false)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			rollback()
			return "", fmt.Errorf("restore %s: %v: %s", bd.Target, err, strings.TrimSpace(string(out)))
		}
		written = append(written, dst)
		if disk := findDisk(&x, bd.Target); disk != nil {
			disk.Source = &libvirtxml.DomainDiskSource{File: &libvirtxml.DomainDiskSourceFile{File: dst}}
			disk.Driver = &libvirtxml.DomainDiskDriver{Name: "qemu", Type: "qcow2", Cache: driverCache(disk.Driver), Discard: "unmap"}
			disk.BackingStore = nil
		}
	}
	_ = l.StoragePoolRefresh(sp, 0)
	doc, err := x.Marshal()
	if err != nil {
		rollback()
		return "", err
	}
	report("Defining " + name + "…")
	nd, err := l.DomainDefineXMLFlags(doc, libvirt.DomainDefineValidate)
	if err != nil {
		rollback()
		return "", classify(err)
	}
	c.log.Info("backup restored", "backup", id, "as", name)
	return formatUUID(nd.UUID), nil
}

func driverCache(d *libvirtxml.DomainDiskDriver) string {
	if d == nil {
		return ""
	}
	return d.Cache
}

// PruneBackups deletes the automatic backups of a machine that keep (given their creation
// times, newest first) does not keep. Manual backups are never touched, and neither is a
// backup that a kept incremental backup builds on.
func (c *Client) PruneBackups(dir BackupDir, uuid string, keep func([]time.Time) []bool) (int, error) {
	all, err := c.ListBackups(dir, uuid)
	if err != nil {
		return 0, err
	}
	var auto []Backup
	var times []time.Time
	for _, b := range all {
		if b.Automatic {
			auto, times = append(auto, b), append(times, b.Created)
		}
	}
	drop := map[string]bool{}
	for i, k := range keep(times) {
		if !k {
			drop[auto[i].ID] = true
		}
	}
	parent := map[string]string{}
	for _, b := range all {
		parent[b.ID] = b.Parent
	}
	for _, b := range all {
		if drop[b.ID] {
			continue
		}
		for p := b.Parent; p != ""; p = parent[p] {
			delete(drop, p)
		}
	}
	// Newest first, so a chain goes from its end and every delete finds no dependents.
	n := 0
	for _, b := range auto {
		if drop[b.ID] {
			if err := c.DeleteBackup(dir, b.ID); err == nil {
				n++
			}
		}
	}
	return n, nil
}
