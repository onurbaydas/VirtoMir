package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/onurbaydas/virtomir/internal/api"
	"github.com/onurbaydas/virtomir/internal/jobs"
	"github.com/onurbaydas/virtomir/internal/offsite"
	"github.com/onurbaydas/virtomir/internal/schedule"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// scheduleRunner lets the scheduler use the libvirt client. off (may be nil) copies each
// scheduled backup off-site when configured to (ADR-0043).
type scheduleRunner struct {
	c   *virt.Client
	off *offsite.Store
}

func (r scheduleRunner) Snapshot(uuid, name, desc string) error {
	_, err := r.c.CreateSnapshot(uuid, virt.SnapshotSpec{Name: name, Description: desc})
	return err
}

func (r scheduleRunner) ListAutoSnapshots(uuid string) ([]string, error) {
	snaps, err := r.c.ListSnapshots(uuid)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(snaps, func(a, b virt.Snapshot) int { return int(a.Created - b.Created) })
	var out []string
	for _, s := range snaps {
		if strings.HasPrefix(s.Name, schedule.AutoPrefix) {
			out = append(out, s.Name)
		}
	}
	return out, nil
}

func (r scheduleRunner) DeleteSnapshot(uuid, name string) error {
	return r.c.DeleteSnapshot(uuid, name)
}

func (r scheduleRunner) Backup(ctx context.Context, dir, uuid string, fullEvery int, report func(string)) error {
	b, err := r.c.BackupDomain(ctx, virt.BackupDir(dir), uuid, virt.BackupOptions{Automatic: true, FullEvery: fullEvery}, report)
	if err != nil || r.off == nil || !r.off.AfterScheduled() {
		return err
	}
	// An incremental backup is merged first, so the off-site copy stands alone.
	root, cleanup, err := r.c.FlatCopy(ctx, virt.BackupDir(dir), b.ID, report)
	if err != nil {
		return fmt.Errorf("backed up locally as %s, but the off-site copy failed: %w", b.ID, err)
	}
	defer cleanup()
	if _, err := r.off.Upload(ctx, root, b.ID, b.DomainUUID, b.Domain, report); err != nil {
		return fmt.Errorf("backed up locally as %s, but the off-site copy failed: %w", b.ID, err)
	}
	return nil
}

func (r scheduleRunner) PruneBackups(dir, uuid string, keep func([]time.Time) []bool) (int, error) {
	return r.c.PruneBackups(virt.BackupDir(dir), uuid, keep)
}

func (r scheduleRunner) MachineExists(uuid string) (bool, error) {
	doms, err := r.c.ListDomains()
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(doms, func(d virt.Domain) bool { return strings.EqualFold(d.UUID, uuid) }), nil
}

func jobStarter(m *jobs.Manager) schedule.Starter {
	return func(kind, title string, fn func(context.Context, func(string)) (string, error)) {
		m.Start(kind, title, fn)
	}
}

// The API finds these through type assertions; keep *virt.Client satisfying them.
var (
	_ api.Backupper = (*virt.Client)(nil)
	_ api.Flattener = (*virt.Client)(nil)
)
