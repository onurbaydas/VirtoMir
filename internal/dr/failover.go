package dr

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/onurbaydas/virtomir/internal/zfsrep"
)

// FailoverOpts configures real disaster failover execution.
type FailoverOpts struct {
	Force       bool `json:"force"`
	SkipPreSync bool `json:"skipPreSync"`
}

// ReplicationSyncer triggers immediate replication passes (internal/zfsrep).
type ReplicationSyncer interface {
	Start(id string) (zfsrep.Status, error)
}

// realDRVMName returns the domain name on the DR host for real failover:
// the plan's DRVMName when configured, or the original source name.
func realDRVMName(vm PlanVM) string {
	if vm.DRVMName != "" {
		return vm.DRVMName
	}
	return vm.SourceName
}

// ExecuteRealFailover executes real disaster failover (ADR-0069).
func (o *Orchestrator) ExecuteRealFailover(
	ctx context.Context,
	planID string,
	opts FailoverOpts,
	prodVirt VirtController,
	drVirt VirtController,
	zfs ZFSRunner,
	rep ReplicationSyncer,
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
	if err := validatePlanAgainstGroup(plan, group); err != nil {
		return ExecutionRun{}, err
	}

	run := &ExecutionRun{
		ID:      newID(),
		PlanID:  planID,
		Mode:    ModeRealFailover,
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
	o.log.Info("real failover started", "plan", plan.Name, "run", run.ID, "force", opts.Force)

	fail := func(cause error) (ExecutionRun, error) {
		now := time.Now().UTC()
		run.State = StateFailed
		run.ErrorMessage = cause.Error()
		run.Finished = &now
		if err := o.store.RecordRun(*run); err != nil {
			o.log.Error("record failed failover run", "run", run.ID, "error", err)
		}
		o.forgetRun(planID)
		return *run, cause
	}

	// Step 1: Production Host Pre-Sync (Zero-RPO check)
	if err := o.runStep(run, report, "Production host pre-sync", "", "", func(sl *StepLog) error {
		if opts.SkipPreSync {
			sl.Status = StepSkipped
			sl.Output = "Pre-sync skipped by options"
			return nil
		}

		reachable := false
		if prodVirt != nil {
			var checkErr error
			for _, uuid := range group.VMUUIDs {
				if _, err := prodVirt.DomainState(uuid); err != nil {
					checkErr = err
					break
				}
			}
			if checkErr == nil {
				reachable = true
			}
		}

		if reachable && !opts.Force {
			// Contact successful: gracefully power off primary VMs to ensure clean filesystem state
			var stopped []string
			for _, uuid := range group.VMUUIDs {
				if err := prodVirt.StopDomain(uuid, false); err != nil {
					o.log.Warn("stop primary domain during pre-sync", "uuid", uuid, "error", err)
				} else {
					stopped = append(stopped, uuid)
				}
			}

			// Immediate final replication pass via rep.Start to sync last delta
			if rep != nil {
				st, err := rep.Start(group.ID)
				if err != nil {
					return fmt.Errorf("final replication pass: %w", err)
				}
				sl.Output = fmt.Sprintf("Gracefully stopped %d primary VMs; final replication pass triggered (%s)", len(stopped), st.State)
			} else {
				sl.Output = fmt.Sprintf("Gracefully stopped %d primary VMs", len(stopped))
			}
			return nil
		}

		// The primary did not answer (or no controller was wired): without
		// Force that is as far as a failover may go.
		if !opts.Force {
			return fmt.Errorf("%w: primary host is unreachable; aborting failover (confirm forced failover to proceed with latest replica snapshot)", ErrBusy)
		}

		if reachable {
			// The primary answers but the operator forced the failover; the
			// graceful pre-sync is skipped by design, and the run says so
			// instead of claiming a disaster it did not detect.
			const forcedWarn = "Forced failover requested with the primary host reachable; skipping graceful pre-sync and failing over from the latest replica snapshot"
			o.log.Warn(forcedWarn, "plan", plan.Name, "run", run.ID)
			sl.Output = forcedWarn
			return nil
		}

		const disasterWarn = "Primary host unreachable; proceeding with forced failover from latest replica snapshot"
		o.log.Warn(disasterWarn, "plan", plan.Name, "run", run.ID)
		sl.Output = disasterWarn
		return nil
	}); err != nil {
		return fail(err)
	}

	// Discover disks of all VMs in the recovery plan
	groups := append([]BootGroup(nil), plan.BootGroups...)
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].Order < groups[j].Order })

	seenDisks := map[string]bool{}
	var toPromote []ReplicatedDisk
	vmDisks := map[string][]ReplicatedDisk{}
	vmXMLDocs := map[string]string{}

	for _, bg := range groups {
		for _, vm := range bg.VMs {
			if doc, ok := vmXMLDocs[vm.VMUUID]; ok {
				disks, err := groupDisks(vm.VMUUID, doc, group.TargetPool)
				if err != nil {
					return fail(err)
				}
				vmDisks[vm.VMUUID] = disks
				for _, d := range disks {
					dsKey := d.TargetPool + "/" + d.TargetVolume
					if !seenDisks[dsKey] {
						seenDisks[dsKey] = true
						toPromote = append(toPromote, d)
					}
				}
				continue
			}
			doc, err := o.resolveDomainXMLWith(vm.VMUUID, prodVirt, drVirt)
			if err != nil {
				return fail(fmt.Errorf("resolve domain XML for %s: %w", vm.SourceName, err))
			}
			vmXMLDocs[vm.VMUUID] = doc
			disks, err := groupDisks(vm.VMUUID, doc, group.TargetPool)
			if err != nil {
				return fail(err)
			}
			vmDisks[vm.VMUUID] = disks
			for _, d := range disks {
				dsKey := d.TargetPool + "/" + d.TargetVolume
				if !seenDisks[dsKey] {
					seenDisks[dsKey] = true
					toPromote = append(toPromote, d)
				}
			}
		}
	}

	// Step 2: Promote Replica Datasets
	if err := o.runStep(run, report, "Promote replica datasets", "", "", func(sl *StepLog) error {
		if err := PromoteReplica(ctx, zfs, toPromote); err != nil {
			return fmt.Errorf("promote replica datasets: %w", err)
		}
		sl.Output = fmt.Sprintf("Promoted %d replica datasets (readonly=off)", len(toPromote))
		return nil
	}); err != nil {
		return fail(err)
	}

	// Step 3: Run Pre-Failover Hooks
	if plan.PreHookScript != "" {
		if hooks == nil {
			return fail(fmt.Errorf("%w: hook runner is unavailable", ErrInvalid))
		}
		if err := o.runStep(run, report, "Pre-hook", "", "", func(sl *StepLog) error {
			out, err := hooks.RunHook(ctx, plan.PreHookScript, hookEnv(plan, "real_failover", "pre"), hookTimeout(plan.HookTimeoutSec))
			sl.Output = string(out)
			if err != nil {
				return fmt.Errorf("pre-hook: %w", err)
			}
			return nil
		}); err != nil {
			return fail(err)
		}
	}

	// Step 4: Boot Groups Execution
	for gi, bg := range groups {
		last := gi == len(groups)-1
		for _, vm := range bg.VMs {
			name := realDRVMName(vm)
			if err := o.runStep(run, report, "Start "+name, bg.ID, vm.VMUUID, func(sl *StepLog) error {
				doc, ok := vmXMLDocs[vm.VMUUID]
				if !ok {
					return fmt.Errorf("machine %s: no cached domain XML from pre-sync", vm.SourceName)
				}
				disks := vmDisks[vm.VMUUID]
				diskMap := make(map[string]string, len(disks))
				for _, d := range disks {
					diskMap[d.Dev] = "/dev/zvol/" + d.TargetPool + "/" + d.TargetVolume
				}
				netMap := map[string]string{}
				for _, nm := range vm.NetworkMappings {
					if nm.TargetNetwork != "" {
						netMap[nm.SourceNetwork] = nm.TargetNetwork
					}
				}
				seed := ""
				if vm.IPReaddressing != nil {
					if o.stateDir == "" {
						return fmt.Errorf("machine %s is re-addressed but the orchestrator has no state directory for the seed", vm.SourceName)
					}
					seed = seedISOPath(o.stateDir, run.ID, vm.VMUUID)
					if err := os.MkdirAll(o.stateDir, 0o750); err != nil {
						return err
					}
					if err := GenerateCloudInitNetworkISO(name, *vm.IPReaddressing, seed); err != nil {
						return fmt.Errorf("seed for %s: %w", vm.SourceName, err)
					}
				}
				xmlDoc, err := BuildDRDomainXML(doc, name, diskMap, netMap, seed)
				if err != nil {
					return err
				}
				id, err := drVirt.DefineDomain(xmlDoc)
				if err != nil {
					return fmt.Errorf("define %s: %w", name, err)
				}
				sl.DomainUUID = id
				if err := drVirt.StartDomain(id); err != nil {
					if !vm.StopOnError {
						sl.Status = StepFailed
						sl.Error = fmt.Sprintf("start %s: %v", name, err)
						return nil
					}
					return fmt.Errorf("start %s: %w", name, err)
				}
				sl.Output = id
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

	// Step 5: Run Post-Failover Hooks
	if plan.PostHookScript != "" {
		if hooks != nil {
			if err := o.runStep(run, report, "Post-hook", "", "", func(sl *StepLog) error {
				out, err := hooks.RunHook(ctx, plan.PostHookScript, hookEnv(plan, "real_failover", "post"), hookTimeout(plan.HookTimeoutSec))
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

	// Step 6: Updates ProtectionGroup and marks run as succeeded
	if err := o.store.SetGroupActiveHost(group.ID, group.TargetHost, StatusFailedOver); err != nil {
		return fail(fmt.Errorf("update protection group: %w", err))
	}

	now := time.Now().UTC()
	run.State = StateSucceeded
	run.Finished = &now
	if err := o.store.RecordRun(*run); err != nil {
		o.log.Error("record succeeded failover run", "run", run.ID, "error", err)
		return *run, fmt.Errorf("record run: %w", err)
	}
	o.forgetRun(planID)
	o.log.Info("real failover succeeded", "plan", plan.Name, "run", run.ID)
	return *run, nil
}

// ExecuteRealFailover executes real disaster failover on DefaultOrchestrator.
func ExecuteRealFailover(
	ctx context.Context,
	planID string,
	opts FailoverOpts,
	prodVirt VirtController,
	drVirt VirtController,
	zfs ZFSRunner,
	rep ReplicationSyncer,
	hooks HookRunner,
	report func(StepLog),
) (ExecutionRun, error) {
	if DefaultOrchestrator == nil {
		return ExecutionRun{}, errors.New("no default orchestrator configured")
	}
	return DefaultOrchestrator.ExecuteRealFailover(ctx, planID, opts, prodVirt, drVirt, zfs, rep, hooks, report)
}
