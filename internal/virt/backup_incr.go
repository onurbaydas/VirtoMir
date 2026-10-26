package virt

// Incremental backups (ADR-0068). A live backup of a machine whose disks are all qcow2 also
// takes a libvirt checkpoint (a dirty bitmap in each qcow2). The next live backup can then
// copy only the blocks changed since that checkpoint: its disk files are qcow2 overlays on
// the previous backup's files (relative backing paths inside the machine's folder), so a
// chain is full → incremental → incremental… Rules that keep chains whole:
//   - a backup another one builds on cannot be deleted, and retention keeps every ancestor
//     of a backup it keeps;
//   - restore follows a chain only inside the machine's backup folder and writes a
//     standalone image;
//   - off-site copies are flattened first (FlatCopy), so every off-site copy stands alone.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/digitalocean/go-libvirt"
)

// checkpointPrefix marks the checkpoints VirtoMir takes; only these are ever deleted.
const checkpointPrefix = "virtomir-"

// MaxManualChain caps the chain of incremental backups made by hand (BackupOptions.FullEvery).
const MaxManualChain = 30

// BackupOptions says how BackupDomain backs up.
type BackupOptions struct {
	// Automatic: made by a schedule (subject to its retention).
	Automatic bool
	// FullEvery: 0 or 1 always takes a full backup; n > 1 takes incremental ones (only what
	// changed since the previous backup) with a full one every n backups.
	FullEvery int
}

// backupXMLFrom is backupXML for an incremental backup since checkpoint from ("" = full).
func backupXMLFrom(dest string, disks []BackupDisk, from string) string {
	raw := backupXML(dest, disks)
	if from == "" {
		return raw
	}
	return strings.Replace(raw, "<disks>", "<incremental>"+xmlEscape(from)+"</incremental><disks>", 1)
}

// checkpointXML takes a checkpoint (a dirty bitmap) of every backed-up disk.
func checkpointXML(name string, disks []BackupDisk) string {
	var xb strings.Builder
	fmt.Fprintf(&xb, "<domaincheckpoint><name>%s</name><disks>", xmlEscape(name))
	for _, bd := range disks {
		fmt.Fprintf(&xb, "<disk name='%s' checkpoint='bitmap'/>", xmlEscape(bd.Target))
	}
	xb.WriteString("</disks></domaincheckpoint>")
	return xb.String()
}

// pickParent chooses the backup the next incremental one builds on: the newest backup
// (backups are newest first) whose checkpoint the machine still has, with the same disks,
// whose whole chain is still there (exists) and shorter than fullEvery.
func pickParent(backups []Backup, checkpoints []string, disks []BackupDisk, fullEvery int, exists func(id string) bool) (Backup, bool) {
	if fullEvery < 2 {
		return Backup{}, false
	}
	byID := map[string]Backup{}
	for _, b := range backups {
		byID[b.ID] = b
	}
	targets := func(ds []BackupDisk) []string {
		var t []string
		for _, d := range ds {
			t = append(t, d.Target)
		}
		slices.Sort(t)
		return t
	}
	for _, b := range backups {
		if b.Checkpoint == "" || !slices.Contains(checkpoints, b.Checkpoint) {
			continue
		}
		if !slices.Equal(targets(b.Disks), targets(disks)) {
			return Backup{}, false
		}
		n := 0
		for cur, ok := b, true; ; cur, ok = byID[cur.Parent] {
			if !ok || !exists(cur.ID) {
				return Backup{}, false
			}
			n++
			if cur.Parent == "" {
				break
			}
		}
		return b, n < fullEvery
	}
	return Backup{}, false
}

// chainInside refuses an image chain with a file outside the machine's backup folder.
func chainInside(folder string, chain []imageInfo) error {
	root := filepath.Clean(folder) + string(filepath.Separator)
	for _, img := range chain {
		if !strings.HasPrefix(filepath.Clean(img.Filename), root) {
			return refusedf("%s refers to %s, outside its backup folder; VirtoMir does not follow that.", filepath.Base(folder), img.Filename)
		}
	}
	return nil
}

// dependents are the backups in the same machine folder that build on id.
func dependents(dir BackupDir, id string) []string {
	folder, _, _ := strings.Cut(id, "/")
	matches, _ := filepath.Glob(filepath.Join(string(dir), folder, "*", manifestName))
	var out []string
	for _, m := range matches {
		if b, err := readManifest(m); err == nil && b.Parent == id {
			rel, _ := filepath.Rel(string(dir), filepath.Dir(m))
			out = append(out, filepath.ToSlash(rel))
		}
	}
	return out
}

// checkpointNames are the checkpoints the machine has.
func checkpointNames(l *libvirt.Libvirt, d libvirt.Domain) []string {
	cps, _, err := l.DomainListAllCheckpoints(d, 1, 0)
	if err != nil {
		return nil
	}
	var out []string
	for _, cp := range cps {
		out = append(out, cp.Name)
	}
	return out
}

// dropCheckpoints deletes VirtoMir's checkpoints of the machine except keep; libvirt merges
// their bitmaps, so later incremental backups stay correct.
func (c *Client) dropCheckpoints(l *libvirt.Libvirt, d libvirt.Domain, keep string) {
	cps, _, err := l.DomainListAllCheckpoints(d, 1, 0)
	if err != nil {
		return
	}
	for _, cp := range cps {
		if cp.Name != keep && strings.HasPrefix(cp.Name, checkpointPrefix) {
			if err := l.DomainCheckpointDelete(cp, 0); err != nil {
				c.log.Warn("old backup checkpoint kept", "domain", d.Name, "checkpoint", cp.Name, "err", err)
			}
		}
	}
}

// rebaseOnParent points each incremental disk file in dest at the parent backup's file,
// relative so the machine folder can move as a whole (libvirt wrote the live disk there).
func rebaseOnParent(ctx context.Context, dest string, disks []BackupDisk, parent Backup) error {
	parentStamp := parent.ID[strings.LastIndex(parent.ID, "/")+1:]
	for _, bd := range disks {
		i := slices.IndexFunc(parent.Disks, func(p BackupDisk) bool { return p.Target == bd.Target })
		if i < 0 {
			return fmt.Errorf("backup %s has no disk %s", parent.ID, bd.Target)
		}
		backing := filepath.ToSlash(filepath.Join("..", parentStamp, parent.Disks[i].File))
		cmd := exec.CommandContext(ctx, "qemu-img", "rebase", "-u", "-f", "qcow2", "-F", "qcow2", "-b", backing, filepath.Join(dest, bd.File))
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("link %s to %s: %v: %s", bd.Target, parent.ID, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// FlatCopy makes an incremental backup stand alone for an off-site copy: its chain is
// merged into <dir>/.virtomir-flat/<id>. It returns the directory to read the backup from
// (dir itself for a full backup) and a cleanup to call afterwards.
func (c *Client) FlatCopy(ctx context.Context, dir BackupDir, id string, report func(string)) (string, func(), error) {
	noop := func() {}
	// A backup in an earlier location is read from there (dir is where it lives).
	dir, p, err := c.locateBackup(dir, id)
	if err != nil {
		return "", noop, err
	}
	b, err := readManifest(filepath.Join(p, manifestName))
	if err != nil {
		return "", noop, err
	}
	if !b.Incremental {
		return string(dir), noop, nil
	}
	root := filepath.Join(string(dir), ".virtomir-flat")
	dest := filepath.Join(root, filepath.FromSlash(id))
	cleanup := func() {
		_ = os.RemoveAll(dest)
		_ = os.Remove(filepath.Dir(dest))
		_ = os.Remove(root)
	}
	if err := os.MkdirAll(dest, 0o750); err != nil {
		return "", noop, err
	}
	for i, bd := range b.Disks {
		report(fmt.Sprintf("Merging the incremental chain of %s (%d of %d)…", bd.Target, i+1, len(b.Disks)))
		src := filepath.Join(p, bd.File)
		chain, err := checkedImage(ctx, src, "qcow2", true)
		if err == nil {
			err = chainInside(filepath.Dir(p), chain)
		}
		if err == nil {
			cmd := exec.CommandContext(ctx, "qemu-img", convertArgs("qcow2", src, filepath.Join(dest, bd.File), false)...)
			if out, cerr := cmd.CombinedOutput(); cerr != nil {
				err = fmt.Errorf("merge %s: %v: %s", bd.Target, cerr, strings.TrimSpace(string(out)))
			}
		}
		if err != nil {
			cleanup()
			return "", noop, err
		}
	}
	raw, err := os.ReadFile(filepath.Join(p, "domain.xml"))
	if err == nil {
		err = os.WriteFile(filepath.Join(dest, "domain.xml"), raw, 0o640)
	}
	if err == nil {
		b.Incremental, b.Parent, b.ID = false, "", ""
		b.SizeBytes = 0
		for i := range b.Disks {
			if st, serr := os.Stat(filepath.Join(dest, b.Disks[i].File)); serr == nil {
				b.Disks[i].SizeBytes = uint64(st.Size())
				b.SizeBytes += uint64(st.Size())
			}
		}
		err = writeManifest(dest, b)
	}
	if err != nil {
		cleanup()
		return "", noop, err
	}
	return root, cleanup, nil
}
