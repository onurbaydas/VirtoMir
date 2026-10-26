package virt

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// Backups made before the backup location was changed stay where they are; the client
// keeps listing, pruning, verifying and restoring them from the earlier folders
// (Client.OldBackupDirs). New backups always go to the current location.

// backupDirs is dir followed by the earlier backup locations (absolute, cleaned, without
// dir itself or duplicates).
func (c *Client) backupDirs(dir BackupDir) []BackupDir {
	var old []string
	if c != nil && c.OldBackupDirs != nil {
		old = c.OldBackupDirs()
	}
	out := []BackupDir{}
	for _, d := range mergeBackupDirs(string(dir), old) {
		out = append(out, BackupDir(d))
	}
	return out
}

// mergeBackupDirs is current first, then every absolute old folder once.
func mergeBackupDirs(current string, old []string) []string {
	out := []string{filepath.Clean(current)}
	for _, o := range old {
		if o == "" || !filepath.IsAbs(o) {
			continue
		}
		if o = filepath.Clean(o); !slices.Contains(out, o) {
			out = append(out, o)
		}
	}
	return out
}

// listIn scans one backup folder; uuid "" lists all machines.
func (c *Client) listIn(dir BackupDir, uuid string) ([]Backup, error) {
	out := []Backup{}
	if err := dir.check(); err != nil {
		return out, err
	}
	matches, err := filepath.Glob(filepath.Join(string(dir), "*", "*", manifestName))
	if err != nil {
		return out, err
	}
	for _, m := range matches {
		if strings.HasSuffix(filepath.Dir(m), partialSuffix) { // still being written (or abandoned)
			continue
		}
		b, err := readManifest(m)
		if err != nil || (uuid != "" && b.DomainUUID != uuid) {
			continue
		}
		rel, _ := filepath.Rel(string(dir), filepath.Dir(m))
		b.ID = filepath.ToSlash(rel)
		out = append(out, b)
	}
	return out, nil
}

// locateBackup finds backup id in dir or one of the earlier backup locations and returns
// that location and the backup's folder.
func (c *Client) locateBackup(dir BackupDir, id string) (BackupDir, string, error) {
	if err := dir.check(); err != nil {
		return dir, "", err
	}
	for _, d := range c.backupDirs(dir) {
		if p, err := backupPath(d, id); err == nil {
			return d, p, nil
		}
	}
	return dir, "", fmt.Errorf("%w: no backup %s", ErrNotFound, id)
}
