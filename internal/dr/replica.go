package dr

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/onurbaydas/virtomir/internal/zfsrep"
)

// ZFSRunner runs zfs commands (internal/zfsrep.Exec on a real host, a fake in
// tests). It mirrors the subset of zfsrep.Runner DR needs to inspect and clone
// replicas, without opening an ssh pipe.
type ZFSRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// DRPrefix marks snapshots DR itself makes on the target host; replication
// snapshots keep zfsrep.SnapPrefix.
const DRPrefix = "virtomir-dr-"

// FindLatestReplicaSnapshot returns the tag (the part after "@") of the newest
// snapshot of dataset whose tag begins with prefix. An empty prefix means the
// replication snapshots of zfsrep.SnapPrefix. Snapshots come back oldest first
// (zfs list -s creation), so the last matching line is the newest one.
func FindLatestReplicaSnapshot(ctx context.Context, r ZFSRunner, dataset string, prefix string) (string, error) {
	if prefix == "" {
		prefix = zfsrep.SnapPrefix
	}
	out, err := r.Run(ctx, "zfs", "list", "-H", "-t", "snapshot", "-o", "name", "-s", "creation", dataset)
	if err != nil {
		return "", err
	}
	latest := ""
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if _, snap, ok := strings.Cut(line, "@"); ok && strings.HasPrefix(snap, prefix) {
			latest = snap // oldest first: keep overwriting so the last match wins
		}
	}
	if latest == "" {
		return "", fmt.Errorf("%w: no %s* snapshot of %s", ErrNotFound, prefix, dataset)
	}
	return latest, nil
}

// CloneInfo is one test clone DR made from a disk's latest replica snapshot.
type CloneInfo struct {
	SourceDataset string `json:"sourceDataset"`
	Snapshot      string `json:"snapshot"`
	CloneDataset  string `json:"cloneDataset"`
	TargetDev     string `json:"targetDev"`
}

// replicaDataset is the replicated dataset a disk's copy lives in on the DR
// host: the target pool holds the disk's target volume, the same name
// PromoteReplica makes writable. Reading SourceVolume here would look in the
// wrong dataset whenever a replica landed under a different name.
func replicaDataset(targetPool string, disk ReplicatedDisk) string {
	return targetPool + "/" + disk.TargetVolume
}

// CreateTestClones clones each disk's latest replica snapshot into the target
// pool, so a test failover runs on throwaway copies that leave the replica
// itself (and production) untouched. The clone of disk SourceVolume is named
// <targetPool>/virtomir-test-<runID>-<SourceVolume>. If any clone fails, the
// clones made so far are destroyed in reverse order and the error is returned.
func CreateTestClones(ctx context.Context, r ZFSRunner, targetPool string, disks []ReplicatedDisk, runID string) ([]CloneInfo, error) {
	clones := make([]CloneInfo, 0, len(disks))
	for _, disk := range disks {
		source := replicaDataset(targetPool, disk)
		snap, err := FindLatestReplicaSnapshot(ctx, r, source, zfsrep.SnapPrefix)
		if err != nil {
			return rollbackClones(ctx, r, clones, fmt.Errorf("disk %s: %w", disk.Dev, err))
		}
		clone := fmt.Sprintf("%s/virtomir-test-%s-%s", targetPool, runID, disk.SourceVolume)
		if _, err := r.Run(ctx, "zfs", "clone", source+"@"+snap, clone); err != nil {
			return rollbackClones(ctx, r, clones, fmt.Errorf("clone %s@%s: %w", source, snap, err))
		}
		clones = append(clones, CloneInfo{
			SourceDataset: source,
			Snapshot:      snap,
			CloneDataset:  clone,
			TargetDev:     disk.Dev,
		})
	}
	return clones, nil
}

// rollbackClones destroys the clones made so far (newest first, the reverse of
// creation) and returns cause. Cleanup runs even when ctx was cancelled, like
// zfsrep's own rollback.
func rollbackClones(ctx context.Context, r ZFSRunner, clones []CloneInfo, cause error) ([]CloneInfo, error) {
	for i := len(clones) - 1; i >= 0; i-- {
		_, _ = r.Run(context.WithoutCancel(ctx), "zfs", "destroy", "-r", clones[i].CloneDataset)
	}
	return nil, cause
}

// DestroyTestClones removes every clone a test failover made, collecting any
// errors instead of stopping at the first, so one stubborn clone cannot leak
// the rest. Cleanup runs even when ctx was cancelled, like rollbackClones: a
// torn-down test must not leave clones behind just because its run was.
func DestroyTestClones(ctx context.Context, r ZFSRunner, clones []CloneInfo) error {
	var errs []error
	for _, c := range clones {
		if _, err := r.Run(context.WithoutCancel(ctx), "zfs", "destroy", "-r", c.CloneDataset); err != nil {
			errs = append(errs, fmt.Errorf("destroy %s: %w", c.CloneDataset, err))
		}
	}
	return errors.Join(errs...)
}

// PromoteReplica makes the replicas writable for a real failover: a zfs
// receive lands readonly, so the DR host can start the machine but not write to
// its disks until readonly is turned off.
func PromoteReplica(ctx context.Context, r ZFSRunner, disks []ReplicatedDisk) error {
	var errs []error
	for _, disk := range disks {
		dataset := disk.TargetPool + "/" + disk.TargetVolume
		out, err := r.Run(ctx, "zfs", "get", "-H", "-o", "value", "readonly", dataset)
		if err != nil {
			errs = append(errs, fmt.Errorf("read %s readonly: %w", dataset, err))
			continue
		}
		if strings.TrimSpace(string(out)) != "on" {
			continue
		}
		if _, err := r.Run(ctx, "zfs", "set", "readonly=off", dataset); err != nil {
			errs = append(errs, fmt.Errorf("promote %s: %w", dataset, err))
		}
	}
	return errors.Join(errs...)
}
