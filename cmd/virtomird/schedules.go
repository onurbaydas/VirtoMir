package main

import (
	"context"
	"fmt"
	"slices"
	"strings"

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

func (r scheduleRunner) Backup(ctx context.Context, dir, uuid string, report func(string)) error {
	b, err := r.c.BackupDomain(ctx, virt.BackupDir(dir), uuid, true, report)
	if err != nil || r.off == nil || !r.off.AfterScheduled() {
		return err
	}
	if _, err := r.off.Upload(ctx, dir, b.ID, b.DomainUUID, b.Domain, report); err != nil {
		return fmt.Errorf("backed up locally as %s, but the off-site copy failed: %w", b.ID, err)
	}
	return nil
}

func (r scheduleRunner) PruneBackups(dir, uuid string, keep int) (int, error) {
	return r.c.PruneBackups(virt.BackupDir(dir), uuid, keep)
}

func jobStarter(m *jobs.Manager) schedule.Starter {
	return func(kind, title string, fn func(context.Context, func(string)) (string, error)) {
		m.Start(kind, title, fn)
	}
}
