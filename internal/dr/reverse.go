package dr

// Hardware-specific remote SSH replication setup is experimental, not tested on hardware.
// The reverse replication engine connects DR and primary hosts over SSH pipes or shell commands
// to stream deltas back to primary storage before executing failback.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"
)

// findAnyLatestSnapshot returns the newest snapshot tag of a dataset, oldest first.
func findAnyLatestSnapshot(ctx context.Context, r ZFSRunner, dataset string) (string, error) {
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
		if _, snap, ok := strings.Cut(line, "@"); ok {
			latest = snap
		}
	}
	if latest == "" {
		return "", fmt.Errorf("%w: no snapshot found on %s", ErrNotFound, dataset)
	}
	return latest, nil
}

// validateHost validates that a host string is a safe hostname or IP address,
// rejecting values that could inject shell metacharacters or SSH options.
// An empty host is allowed (the caller decides what to do with it).
func validateHost(host string) error {
	if host == "" || host == "localhost" {
		return nil
	}
	if strings.ContainsAny(host, " \t\n\r;|&$`<>(){}[]\\\"'\x00") {
		return fmt.Errorf("%w: host %q contains shell metacharacters", ErrInvalid, host)
	}
	if strings.HasPrefix(host, "-") {
		return fmt.Errorf("%w: host %q must not start with a dash", ErrInvalid, host)
	}
	if net.ParseIP(host) != nil {
		return nil
	}
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.', r == '-', r == ':':
		default:
			return fmt.Errorf("%w: host %q contains invalid characters", ErrInvalid, host)
		}
	}
	return nil
}

// streamZFSDelta streams an incremental delta from fromDataset@baseSnap to fromDataset@targetSnap
// back to toDataset on destHost.
//
// Hardware-specific remote SSH replication setup is experimental, not tested on hardware.
func streamZFSDelta(ctx context.Context, zfs ZFSRunner, fromDataset, fromVolume, targetSnap, baseSnap, destHost, toDataset string) error {
	if err := validateHost(destHost); err != nil {
		return err
	}
	if piper, ok := zfs.(interface {
		Pipe(ctx context.Context, send, recv []string) (uint64, error)
	}); ok {
		sendCmd := []string{"zfs", "send", "-i", "@" + baseSnap, fromDataset + "@" + targetSnap}
		recvCmd := []string{"ssh", destHost, "zfs", "recv", "-F", toDataset}
		if destHost == "" || destHost == "localhost" {
			recvCmd = []string{"zfs", "recv", "-F", toDataset}
		}
		_, err := piper.Pipe(ctx, sendCmd, recvCmd)
		return err
	}

	recvTarget := fmt.Sprintf("ssh %s zfs recv -F %s", destHost, toDataset)
	if destHost == "" || destHost == "localhost" {
		recvTarget = fmt.Sprintf("zfs recv -F %s", toDataset)
	}
	cmdStr := fmt.Sprintf("zfs send -i @%s %s@%s | %s", baseSnap, fromDataset, targetSnap, recvTarget)
	_, err := zfs.Run(ctx, "sh", "-c", cmdStr)
	return err
}

// findDRDomainUUID returns the domain UUID defined during a prior failover run for this VM,
// or vmUUID if none was recorded.
func (o *Orchestrator) findDRDomainUUID(planID, vmUUID string) string {
	for _, r := range o.store.ListRuns(planID) {
		if r.Mode == ModeRealFailover {
			for _, s := range r.Steps {
				if s.VMUUID == vmUUID && s.DomainUUID != "" {
					return s.DomainUUID
				}
			}
		}
	}
	return vmUUID
}

// ExecuteFailback executes reverse replication and failback to the restored primary host (ADR-0069).
func (o *Orchestrator) ExecuteFailback(
	ctx context.Context,
	planID string,
	prodVirt VirtController,
	drVirt VirtController,
	zfs ZFSRunner,
	hooks HookRunner,
	report func(StepLog),
) (ExecutionRun, error) {
	if drVirt == nil {
		drVirt = o.virt
	}
	if drVirt == nil {
		return ExecutionRun{}, fmt.Errorf("%w: target virt controller is unavailable", ErrInvalid)
	}
	if zfs == nil {
		zfs = o.zfs
	}
	if zfs == nil {
		return ExecutionRun{}, fmt.Errorf("%w: ZFS runner is unavailable", ErrInvalid)
	}
	if hooks == nil {
		hooks = o.hooks
	}
	if hooks == nil {
		hooks = NewDefaultHookRunner()
	}

	plan, ok := o.store.GetPlan(planID)
	if !ok {
		return ExecutionRun{}, fmt.Errorf("%w: recovery plan %q", ErrNotFound, planID)
	}
	group, ok := o.store.GetProtectionGroup(plan.ProtectionGroupID)
	if !ok {
		return ExecutionRun{}, fmt.Errorf("%w: protection group %q of plan %q", ErrNotFound, plan.ProtectionGroupID, plan.Name)
	}
	if group.Status != StatusFailedOver {
		return ExecutionRun{}, fmt.Errorf("%w: protection group %q is %s, must be %s to fail back", ErrInvalid, group.ID, group.Status, StatusFailedOver)
	}
	if prodVirt == nil {
		return ExecutionRun{}, fmt.Errorf("%w: primary host controller is unavailable", ErrInvalid)
	}

	// Validate primary host is restored and reachable
	for _, uuid := range group.VMUUIDs {
		if _, err := prodVirt.DomainState(uuid); err != nil {
			return ExecutionRun{}, fmt.Errorf("primary host is unreachable: %w", err)
		}
	}

	run := &ExecutionRun{
		ID:      newID(),
		PlanID:  planID,
		Mode:    ModeFailback,
		State:   StateRunning,
		Started: time.Now().UTC(),
	}
	if err := o.markActive(planID, run); err != nil {
		return ExecutionRun{}, err
	}
	if err := o.store.RecordRun(*run); err != nil {
		o.forgetRun(planID)
		return ExecutionRun{}, fmt.Errorf("record run: %w", err)
	}
	o.log.Info("failback started", "plan", plan.Name, "run", run.ID)

	fail := func(cause error) (ExecutionRun, error) {
		now := time.Now().UTC()
		run.State = StateFailed
		run.ErrorMessage = cause.Error()
		run.Finished = &now
		if err := o.store.RecordRun(*run); err != nil {
			o.log.Error("record failed failback run", "run", run.ID, "error", err)
		}
		o.forgetRun(planID)
		return *run, cause
	}

	// Discover disks of all VMs in the recovery plan
	groups := append([]BootGroup(nil), plan.BootGroups...)
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].Order < groups[j].Order })

	seenDisks := map[string]bool{}
	var planDisks []ReplicatedDisk

	for _, bg := range groups {
		for _, vm := range bg.VMs {
			doc, err := o.resolveDomainXMLWith(vm.VMUUID, prodVirt, drVirt)
			if err != nil {
				return fail(fmt.Errorf("resolve XML for %s: %w", vm.SourceName, err))
			}
			disks, err := groupDisks(vm.VMUUID, doc, group.TargetPool)
			if err != nil {
				return fail(err)
			}
			for _, d := range disks {
				dsKey := d.TargetPool + "/" + d.TargetVolume
				if !seenDisks[dsKey] {
					seenDisks[dsKey] = true
					planDisks = append(planDisks, d)
				}
			}
		}
	}

	// Step 1: Reverse Replication Setup (online sync pass)
	snapTime := time.Now().UTC().Format("20060102-150405")
	initialReverseSnap := fmt.Sprintf("virtomir-dr-reverse-%s", snapTime)

	if err := o.runStep(run, report, "Reverse replication setup (online sync)", "", "", func(sl *StepLog) error {
		for _, disk := range planDisks {
			targetDataset := disk.TargetPool + "/" + disk.TargetVolume
			sourceDataset := disk.SourcePool + "/" + disk.SourceVolume

			baseSnap, err := findAnyLatestSnapshot(ctx, zfs, targetDataset)
			if err != nil {
				baseSnap = "base"
			}

			if _, err := zfs.Run(ctx, "zfs", "snapshot", targetDataset+"@"+initialReverseSnap); err != nil {
				return fmt.Errorf("snapshot %s@%s: %w", targetDataset, initialReverseSnap, err)
			}

			if err := streamZFSDelta(ctx, zfs, targetDataset, disk.TargetVolume, initialReverseSnap, baseSnap, group.SourceHost, sourceDataset); err != nil {
				return fmt.Errorf("stream online delta %s: %w", targetDataset, err)
			}
		}
		sl.Output = fmt.Sprintf("Initial reverse sync completed using snapshot %s", initialReverseSnap)
		return nil
	}); err != nil {
		return fail(err)
	}

	// Step 2: Quiesce & Final Delta Sync
	finalReverseSnap := fmt.Sprintf("virtomir-dr-reverse-%s-final", snapTime)

	if err := o.runStep(run, report, "Quiesce & final delta sync", "", "", func(sl *StepLog) error {
		// Stop VMs on DR host via drVirt.StopDomain(uuid, false)
		for _, bg := range groups {
			for _, vm := range bg.VMs {
				drUUID := o.findDRDomainUUID(plan.ID, vm.VMUUID)
				if err := drVirt.StopDomain(drUUID, false); err != nil {
					o.log.Warn("stop DR VM during quiesce", "uuid", drUUID, "error", err)
				}
			}
		}

		// Takes final snapshot on DR host and executes final incremental sync pass to primary host
		for _, disk := range planDisks {
			targetDataset := disk.TargetPool + "/" + disk.TargetVolume
			sourceDataset := disk.SourcePool + "/" + disk.SourceVolume

			if _, err := zfs.Run(ctx, "zfs", "snapshot", targetDataset+"@"+finalReverseSnap); err != nil {
				return fmt.Errorf("final snapshot %s@%s: %w", targetDataset, finalReverseSnap, err)
			}

			if err := streamZFSDelta(ctx, zfs, targetDataset, disk.TargetVolume, finalReverseSnap, initialReverseSnap, group.SourceHost, sourceDataset); err != nil {
				return fmt.Errorf("stream final delta %s: %w", targetDataset, err)
			}
		}
		sl.Output = fmt.Sprintf("Quiesced DR VMs and completed final delta sync using snapshot %s", finalReverseSnap)
		return nil
	}); err != nil {
		return fail(err)
	}

	// Step 3: Restore Primary Datasets
	if err := o.runStep(run, report, "Restore primary datasets", "", "", func(sl *StepLog) error {
		for _, disk := range planDisks {
			sourceDataset := disk.SourcePool + "/" + disk.SourceVolume
			if _, err := zfs.Run(ctx, "zfs", "set", "readonly=off", sourceDataset); err != nil {
				return fmt.Errorf("set readonly=off on %s: %w", sourceDataset, err)
			}
		}
		sl.Output = fmt.Sprintf("Restored readonly=off on %d primary datasets", len(planDisks))
		return nil
	}); err != nil {
		return fail(err)
	}

	// Step 4: Pre-Failback Hook
	if plan.PreHookScript != "" {
		if hooks == nil {
			return fail(fmt.Errorf("%w: hook runner is unavailable", ErrInvalid))
		}
		if err := o.runStep(run, report, "Pre-hook", "", "", func(sl *StepLog) error {
			out, err := hooks.RunHook(ctx, plan.PreHookScript, hookEnv(plan, "failback", "pre"), hookTimeout(plan.HookTimeoutSec))
			sl.Output = string(out)
			if err != nil {
				return fmt.Errorf("pre-hook: %w", err)
			}
			return nil
		}); err != nil {
			return fail(err)
		}
	}

	// Step 5: Define and Boot on Primary Host
	for gi, bg := range groups {
		last := gi == len(groups)-1
		for _, vm := range bg.VMs {
			if err := o.runStep(run, report, "Start primary "+vm.SourceName, bg.ID, vm.VMUUID, func(sl *StepLog) error {
				doc, err := o.resolveDomainXMLWith(vm.VMUUID, prodVirt, drVirt)
				if err != nil {
					return fmt.Errorf("machine %s: %w", vm.SourceName, err)
				}
				id, err := prodVirt.DefineDomain(doc)
				if err != nil {
					return fmt.Errorf("define primary %s: %w", vm.SourceName, err)
				}
				sl.DomainUUID = id
				if err := prodVirt.StartDomain(id); err != nil {
					if !vm.StopOnError {
						sl.Status = StepFailed
						sl.Error = fmt.Sprintf("start primary %s: %v", vm.SourceName, err)
						return nil
					}
					return fmt.Errorf("start primary %s: %w", vm.SourceName, err)
				}
				sl.Output = id

				// Undefine domain on DR host
				drUUID := o.findDRDomainUUID(plan.ID, vm.VMUUID)
				if err := drVirt.UndefineDomain(drUUID); err != nil {
					o.log.Warn("undefine DR domain", "uuid", drUUID, "error", err)
				}
				return nil
			}); err != nil {
				return fail(err)
			}
		}
		if bg.DelaySeconds > 0 && !last {
			if err := o.runStep(run, report, "Waiting delay", bg.ID, "", func(sl *StepLog) error {
				sl.Output = fmt.Sprintf("%d seconds", bg.DelaySeconds)
				select {
				case <-time.After(time.Duration(bg.DelaySeconds) * time.Second):
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}); err != nil {
				return fail(err)
			}
		}
	}

	// Step 6: Post-Failback Hook
	if plan.PostHookScript != "" {
		if hooks != nil {
			if err := o.runStep(run, report, "Post-hook", "", "", func(sl *StepLog) error {
				out, err := hooks.RunHook(ctx, plan.PostHookScript, hookEnv(plan, "failback", "post"), hookTimeout(plan.HookTimeoutSec))
				sl.Output = string(out)
				if err != nil {
					return fmt.Errorf("post-hook: %w", err)
				}
				return nil
			}); err != nil {
				o.log.Warn("post-hook failed", "run", run.ID, "error", err)
			}
		} else {
			o.log.Warn("post-hook skipped: hook runner is unavailable", "run", run.ID)
		}
	}

	// Step 7: Restore Forward Replication
	if err := o.store.SetGroupActiveHost(group.ID, group.SourceHost, StatusProtected); err != nil {
		return fail(fmt.Errorf("update protection group: %w", err))
	}

	now := time.Now().UTC()
	run.State = StateSucceeded
	run.Finished = &now
	if err := o.store.RecordRun(*run); err != nil {
		o.log.Error("record succeeded failback run", "run", run.ID, "error", err)
		return *run, fmt.Errorf("record run: %w", err)
	}
	o.forgetRun(planID)
	o.log.Info("failback succeeded", "plan", plan.Name, "run", run.ID)
	return *run, nil
}

// ExecuteFailback executes reverse replication and failback on DefaultOrchestrator.
func ExecuteFailback(
	ctx context.Context,
	planID string,
	prodVirt VirtController,
	drVirt VirtController,
	zfs ZFSRunner,
	hooks HookRunner,
	report func(StepLog),
) (ExecutionRun, error) {
	if DefaultOrchestrator == nil {
		return ExecutionRun{}, errors.New("no default orchestrator configured")
	}
	return DefaultOrchestrator.ExecuteFailback(ctx, planID, prodVirt, drVirt, zfs, hooks, report)
}
