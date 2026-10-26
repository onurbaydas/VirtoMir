package virt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/onurbaydas/virtomir/internal/atomicfile"
)

// A backup is checked right after it is written (qemu-img check on every disk file, and a
// SHA-256 of each file recorded in the manifest), again whenever someone presses Verify,
// and its checksums are compared before a restore. A backup whose files changed or are
// damaged says so in the list and is only restored on request.

// qemuCheck runs "qemu-img check" on a backup disk file (a variable for tests).
var qemuCheck = func(ctx context.Context, path string) error {
	out, err := exec.CommandContext(ctx, "qemu-img", "check", "-f", "qcow2", path).CombinedOutput()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &exit) && exit.ExitCode() == 3:
		return nil // leaked clusters only: wasted space, no damage
	default:
		return fmt.Errorf("qemu-img check %s: %v: %s", filepath.Base(path), err, strings.TrimSpace(lastLines(string(out), 3)))
	}
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " ")
}

// hashFile is the hex SHA-256 of a file.
func hashFile(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, &ctxReader{ctx: ctx, r: f}); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// verifyBackupFiles checks the disk files of b in folder. With record, missing checksums
// are filled in (a new backup, or one made before checksums existed); a recorded checksum
// that no longer matches is always an error. b.Verified and b.CheckError are set.
func verifyBackupFiles(ctx context.Context, folder string, b *Backup, record bool, report func(string)) error {
	var errs []error
	for i := range b.Disks {
		bd := &b.Disks[i]
		p := filepath.Join(folder, bd.File)
		report(fmt.Sprintf("Checking disk %s (%d of %d)…", bd.Target, i+1, len(b.Disks)))
		if err := qemuCheck(ctx, p); err != nil {
			errs = append(errs, err)
			continue
		}
		sum, err := hashFile(ctx, p)
		if err != nil {
			errs = append(errs, fmt.Errorf("read %s: %w", bd.File, err))
			continue
		}
		switch {
		case bd.SHA256 == "" && record:
			bd.SHA256 = sum
		case bd.SHA256 != "" && bd.SHA256 != sum:
			errs = append(errs, fmt.Errorf("%s changed since the backup was made (checksum differs)", bd.File))
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	now := time.Now().UTC()
	b.Verified = &now
	b.CheckError = ""
	if err := errors.Join(errs...); err != nil {
		b.CheckError = err.Error()
		return err
	}
	return nil
}

// checksumsMatch compares the recorded checksums of b's disk files in folder with the files
// (no qemu-img: a restore reads them right after). Disks without a recorded checksum pass.
func checksumsMatch(ctx context.Context, folder string, b Backup, report func(string)) error {
	for i, bd := range b.Disks {
		if bd.SHA256 == "" {
			continue
		}
		report(fmt.Sprintf("Checking backup integrity of %s (%d of %d)…", bd.Target, i+1, len(b.Disks)))
		sum, err := hashFile(ctx, filepath.Join(folder, bd.File))
		if err != nil {
			return fmt.Errorf("read %s: %w", bd.File, err)
		}
		if sum != bd.SHA256 {
			return refusedf("%s of this backup changed since it was made (its checksum differs), so it may be damaged. Restore it anyway only if you accept that.", bd.File)
		}
	}
	return nil
}

// VerifyBackup checks a backup's files again (qemu-img check and the recorded checksums)
// and stores the result in its manifest.
func (c *Client) VerifyBackup(ctx context.Context, dir BackupDir, id string, report func(string)) (Backup, error) {
	loc, p, err := c.locateBackup(dir, id)
	if err != nil {
		return Backup{}, err
	}
	b, err := readManifest(filepath.Join(p, manifestName))
	if err != nil {
		return Backup{}, err
	}
	verr := verifyBackupFiles(ctx, p, &b, true, report)
	if errors.Is(verr, context.Canceled) {
		return Backup{}, verr
	}
	b.ID = id
	if loc != dir {
		b.Location = string(loc)
	}
	if err := saveManifest(p, b); err != nil {
		return b, err
	}
	c.log.Info("backup verified", "backup", id, "ok", verr == nil)
	return b, verr
}

// saveManifest replaces a backup's manifest atomically (ID and Location are not stored).
func saveManifest(folder string, b Backup) error {
	b.ID, b.Location = "", ""
	raw, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(folder, manifestName), raw, 0o640)
}
