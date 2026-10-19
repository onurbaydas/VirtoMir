package dr

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"libvirt.org/go/libvirtxml"

	"github.com/onurbaydas/virtomir/internal/zfsrep"
)

// VirtController is the seam the orchestrator uses to read source definitions
// and run the test domains on a host; internal/api wires it to libvirt,
// cluster forwarding included.
type VirtController interface {
	GetDomainXML(uuid string) (string, error)
	DefineDomain(xmlDoc string) (string, error)
	StartDomain(uuid string) error
	StopDomain(uuid string, force bool) error
	UndefineDomain(uuid string) error
	DomainState(uuid string) (string, error)
}

// Orchestrator executes recovery plans on the DR host: a test failover runs
// the plan's machines from fresh test clones in isolated networks until its
// cleanup releases them. One run may be active per plan (activeRuns), and
// xmlCache keeps the last source definition seen of each machine so a failed
// live fetch can still fall back to it.
type Orchestrator struct {
	store      *Store
	zfs        ZFSRunner
	virt       VirtController
	hooks      HookRunner
	log        *slog.Logger
	activeRuns map[string]*ExecutionRun
	mu         sync.Mutex
	stateDir   string
	xmlCache   map[string]string
}

// DefaultOrchestrator is the orchestrator used by package-level ExecuteRealFailover and ExecuteFailback.
var DefaultOrchestrator *Orchestrator

// NewOrchestrator wires an orchestrator over the store and its ZFS, libvirt
// and hook runners; stateDir is where a run's seed ISO lands ("" if nowhere).
func NewOrchestrator(store *Store, zfs ZFSRunner, virt VirtController, hooks HookRunner, log *slog.Logger, stateDir string) *Orchestrator {
	if log == nil {
		log = slog.Default()
	}
	o := &Orchestrator{
		store:      store,
		zfs:        zfs,
		virt:       virt,
		hooks:      hooks,
		log:        log,
		activeRuns: map[string]*ExecutionRun{},
		stateDir:   stateDir,
		xmlCache:   map[string]string{},
	}
	DefaultOrchestrator = o
	return o
}

// ExecuteTestFailover runs the plan as a test: it verifies every machine of
// the protection group has a fresh replica snapshot, clones those replicas,
// defines throwaway domains on the clones in the plan's boot order (isolated
// networks and re-addressing applied) and runs the plan's hooks around it. The
// run stays active — with the test domains still running — until
// ExecuteTestCleanup releases it.
func (o *Orchestrator) ExecuteTestFailover(ctx context.Context, planID string, report func(StepLog)) (ExecutionRun, error) {
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
		PlanID:  plan.ID,
		Mode:    ModeTestFailover,
		State:   StateRunning,
		Started: time.Now().UTC(),
	}
	if err := o.markActive(plan.ID, run); err != nil {
		return ExecutionRun{}, err
	}
	if err := o.store.RecordRun(*run); err != nil {
		o.forgetRun(plan.ID)
		return ExecutionRun{}, fmt.Errorf("record run: %w", err)
	}
	o.log.Info("test failover started", "plan", plan.Name, "run", run.ID)

	var (
		defined []string
		clones  []CloneInfo
	)
	fail := func(cause error) (ExecutionRun, error) {
		o.tearDown(run, defined, clones)
		now := time.Now().UTC()
		run.State = StateFailed
		run.ErrorMessage = cause.Error()
		run.Finished = &now
		if err := o.store.RecordRun(*run); err != nil {
			o.log.Error("record failed run", "run", run.ID, "error", err)
		}
		o.forgetRun(plan.ID)
		return *run, cause
	}

	// Step 1: the pre-flight reads every machine of the group and verifies its
	// replica has a snapshot to clone from; the disks it finds drive step 2.
	vmDisks := map[string][]ReplicatedDisk{}
	if err := o.runStep(run, report, "Pre-flight & snapshot discovery", "", "", func(sl *StepLog) error {
		replicas := 0
		for _, uuid := range group.VMUUIDs {
			doc, err := o.resolveDomainXML(uuid)
			if err != nil {
				return fmt.Errorf("machine %s: %w", uuid, err)
			}
			disks, err := groupDisks(uuid, doc, group.TargetPool)
			if err != nil {
				return err
			}
			for i := range disks {
				snap, err := FindLatestReplicaSnapshot(ctx, o.zfs, disks[i].TargetPool+"/"+disks[i].TargetVolume, zfsrep.SnapPrefix)
				if err != nil {
					return fmt.Errorf("machine %s, disk %s: %w", uuid, disks[i].Dev, err)
				}
				disks[i].LatestSnapshot = snap
			}
			vmDisks[uuid] = disks
			replicas += len(disks)
		}
		sl.Output = fmt.Sprintf("%d machines, %d replicas", len(group.VMUUIDs), replicas)
		return nil
	}); err != nil {
		return fail(err)
	}

	// Step 2: the test clones of every machine the plan starts, so the
	// replicas themselves (and production) stay untouched.
	groups := append([]BootGroup(nil), plan.BootGroups...)
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].Order < groups[j].Order })
	toClone := []ReplicatedDisk{}
	{
		started := map[string]bool{}
		for _, bg := range groups {
			for _, vm := range bg.VMs {
				if started[vm.VMUUID] {
					continue
				}
				started[vm.VMUUID] = true
				toClone = append(toClone, vmDisks[vm.VMUUID]...)
			}
		}
	}
	if err := o.runStep(run, report, "Create test clones", "", "", func(sl *StepLog) error {
		var err error
		clones, err = CreateTestClones(ctx, o.zfs, group.TargetPool, toClone, run.ID)
		if err != nil {
			return err
		}
		names := make([]string, 0, len(clones))
		for _, c := range clones {
			names = append(names, c.CloneDataset)
		}
		sl.Output = strings.Join(names, ", ")
		return nil
	}); err != nil {
		return fail(err)
	}

	// Step 3: the pre-hook runs before anything boots; it failing means the
	// test would start on a bad premise, so the run aborts and tears down.
	if plan.PreHookScript != "" {
		if err := o.runStep(run, report, "Pre-hook", "", "", func(sl *StepLog) error {
			out, err := o.hooks.RunHook(ctx, plan.PreHookScript, hookEnv(plan, "test", "pre"), hookTimeout(plan.HookTimeoutSec))
			sl.Output = string(out)
			if err != nil {
				return fmt.Errorf("pre-hook: %w", err)
			}
			return nil
		}); err != nil {
			return fail(err)
		}
	}

	// Step 4: the boot groups in ascending order, each machine on its test
	// clone, then the group's delay before the next one boots.
	for gi, bg := range groups {
		last := gi == len(groups)-1
		for _, vm := range bg.VMs {
			name := drVMName(vm)
			if err := o.runStep(run, report, "Start "+name, bg.ID, vm.VMUUID, func(sl *StepLog) error {
				doc, err := o.resolveDomainXML(vm.VMUUID)
				if err != nil {
					return fmt.Errorf("machine %s: %w", vm.SourceName, err)
				}
				diskMap, err := diskCloneMap(vmDisks[vm.VMUUID], clones)
				if err != nil {
					return err
				}
				netMap := map[string]string{}
				for _, nm := range vm.NetworkMappings {
					target := nm.IsolatedTestNetwork
					if target == "" {
						target = nm.TargetNetwork
					}
					if target != "" {
						netMap[nm.SourceNetwork] = target
					}
				}
				seed := ""
				if vm.IPReaddressing != nil {
					if o.stateDir == "" {
						return fmt.Errorf("machine %s is re-addressed but the orchestrator has no state directory for the seed", vm.SourceName)
					}
					// One seed per machine: two re-addressed machines in one run
					// must not overwrite each other's cloud-init.
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
				id, err := o.virt.DefineDomain(xmlDoc)
				if err != nil {
					return fmt.Errorf("define %s: %w", name, err)
				}
				sl.DomainUUID = id
				defined = append(defined, id)
				if err := o.virt.StartDomain(id); err != nil {
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

	// Step 5: the post-hook runs and is only logged: by now everything is up,
	// and a failing script must not take the test down with it.
	if plan.PostHookScript != "" {
		if err := o.runStep(run, report, "Post-hook", "", "", func(sl *StepLog) error {
			out, err := o.hooks.RunHook(ctx, plan.PostHookScript, hookEnv(plan, "test", "post"), hookTimeout(plan.HookTimeoutSec))
			sl.Output = string(out)
			if err != nil {
				return fmt.Errorf("post-hook: %w", err)
			}
			return nil
		}); err != nil {
			o.log.Warn("post-hook failed", "run", run.ID, "error", err)
		}
	}

	// Step 6: the test is live; the run stays in the active set until its
	// cleanup, when the test domains and clones are released.
	now := time.Now().UTC()
	run.State = StateTestActive
	run.Finished = &now
	if err := o.store.RecordRun(*run); err != nil {
		o.log.Error("record test failover run", "run", run.ID, "error", err)
		return *run, fmt.Errorf("record run: %w", err)
	}
	o.log.Info("test failover active", "run", run.ID)
	return *run, nil
}

// ExecuteTestCleanup stops and undefines the test domains of an active test
// failover, destroys its clones and removes its seed ISO, closing the run as
// test_cleaned. The newest record of the run decides whether it is active, so
// a retry after a partial cleanup still sees the right state.
func (o *Orchestrator) ExecuteTestCleanup(ctx context.Context, planID, runID string, report func(StepLog)) (ExecutionRun, error) {
	first, ok := o.store.GetRun(runID)
	if !ok {
		return ExecutionRun{}, fmt.Errorf("%w: execution run %q", ErrNotFound, runID)
	}
	if first.PlanID != planID {
		return ExecutionRun{}, fmt.Errorf("%w: run %q belongs to another plan", ErrInvalid, runID)
	}
	run := first
	for _, r := range o.store.ListRuns(planID) { // newest first
		if r.ID == runID {
			run = r
			break
		}
	}
	if run.State != StateTestActive && run.State != StateRunning {
		return ExecutionRun{}, fmt.Errorf("%w: run %q is %s, only an active test can be cleaned", ErrInvalid, runID, run.State)
	}
	plan, ok := o.store.GetPlan(planID)
	if !ok {
		return ExecutionRun{}, fmt.Errorf("%w: recovery plan %q", ErrNotFound, planID)
	}
	group, ok := o.store.GetProtectionGroup(plan.ProtectionGroupID)
	if !ok {
		return ExecutionRun{}, fmt.Errorf("%w: protection group %q", ErrNotFound, plan.ProtectionGroupID)
	}
	if err := o.markCleanupActive(planID, &run); err != nil {
		return ExecutionRun{}, err
	}
	o.log.Info("test cleanup started", "run", runID)

	// The test's domains are the ones its steps defined, in definition order.
	domains := []string{}
	{
		seen := map[string]bool{}
		for _, r := range o.store.ListRuns(planID) {
			if r.ID != runID {
				continue
			}
			for _, s := range r.Steps {
				if s.VMUUID == "" || s.DomainUUID == "" || seen[s.DomainUUID] {
					continue
				}
				seen[s.DomainUUID] = true
				domains = append(domains, s.DomainUUID)
			}
		}
	}

	// This record documents the cleanup only; the failover's steps stay in
	// the record the failover phase saved under the same run ID.
	run.Steps = nil

	// A step that fails keeps the run test_active (with its error recorded),
	// so the operator can fix the host and retry the cleanup.
	fail := func(cause error) (ExecutionRun, error) {
		now := time.Now().UTC()
		run.State = StateTestActive
		run.ErrorMessage = cause.Error()
		run.Finished = &now
		if err := o.store.RecordRun(run); err != nil {
			o.log.Error("record failed cleanup", "run", runID, "error", err)
		}
		o.mu.Lock()
		active := run
		active.State = StateTestActive
		active.Mode = ModeTestFailover
		o.activeRuns[planID] = &active
		o.mu.Unlock()
		return run, cause
	}

	if err := o.runStep(&run, report, "Stop test VMs", "", "", func(sl *StepLog) error {
		var errs []error
		for _, id := range domains {
			if err := o.stopTestDomain(id); err != nil {
				errs = append(errs, fmt.Errorf("stop %s: %w", id, err))
			}
		}
		sl.Output = fmt.Sprintf("%d test VMs", len(domains))
		return errors.Join(errs...)
	}); err != nil {
		return fail(err)
	}

	if err := o.runStep(&run, report, "Undefine test domains", "", "", func(sl *StepLog) error {
		var errs []error
		for _, id := range domains {
			if err := o.virt.UndefineDomain(id); err != nil {
				errs = append(errs, fmt.Errorf("undefine %s: %w", id, err))
			}
		}
		sl.Output = fmt.Sprintf("%d domains", len(domains))
		return errors.Join(errs...)
	}); err != nil {
		return fail(err)
	}

	if err := o.runStep(&run, report, "Destroy test clones", "", "", func(sl *StepLog) error {
		datasets, err := testCloneDatasets(ctx, o.zfs, group.TargetPool, run.ID)
		if err != nil {
			return err
		}
		sl.Output = fmt.Sprintf("%d clones", len(datasets))
		clones := make([]CloneInfo, 0, len(datasets))
		for _, ds := range datasets {
			clones = append(clones, CloneInfo{CloneDataset: ds})
		}
		return DestroyTestClones(ctx, o.zfs, clones)
	}); err != nil {
		return fail(err)
	}

	if err := o.runStep(&run, report, "Remove seed ISO", "", "", func(sl *StepLog) error {
		// Every re-addressed machine of the run has its own seed; remove
		// them all. A leftover seed must not block the clean-up; only note it.
		seeds, err := seedISOsOf(o.stateDir, run.ID)
		if err != nil {
			o.log.Warn("cleanup: find seed ISOs", "run", runID, "error", err)
			return nil
		}
		var errs []error
		for _, seed := range seeds {
			if err := os.Remove(seed); err != nil && !errors.Is(err, os.ErrNotExist) {
				o.log.Warn("cleanup: remove seed ISO", "run", runID, "path", seed, "error", err)
				errs = append(errs, err)
			} else {
				sl.Output = seed
			}
		}
		return errors.Join(errs...)
	}); err != nil {
		return fail(err)
	}

	now := time.Now().UTC()
	run.State = StateTestCleaned
	run.Finished = &now
	if err := o.store.RecordRun(run); err != nil {
		o.log.Error("record cleaned run", "run", runID, "error", err)
		return run, fmt.Errorf("record run: %w", err)
	}
	o.forgetRun(planID)
	o.log.Info("test cleaned", "run", runID)
	return run, nil
}

// markActive registers a plan's active run; a plan that already has one is
// busy. The check covers both this daemon's runs and the ones a previous
// daemon left behind: if the store's newest record of the plan is still
// running or test_active, that run owns the plan.
func (o *Orchestrator) markActive(planID string, run *ExecutionRun) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if cur, busy := o.activeRuns[planID]; busy {
		return fmt.Errorf("%w: plan %q already has an active run %s", ErrBusy, planID, cur.ID)
	}
	for _, r := range o.store.ListRuns(planID) { // newest first
		if r.State == StateRunning || r.State == StateTestActive {
			return fmt.Errorf("%w: plan %q has run %s recorded %s by an earlier session", ErrBusy, planID, r.ID, r.State)
		}
		break
	}
	o.activeRuns[planID] = run
	return nil
}

// markCleanupActive locks the plan for cleanup. A plan is busy if an operation
// is already running on it in this session.
func (o *Orchestrator) markCleanupActive(planID string, run *ExecutionRun) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if cur, busy := o.activeRuns[planID]; busy {
		if cur.State == StateRunning || cur.Mode == ModeTestCleanup {
			return fmt.Errorf("%w: plan %q already has an active operation (%s)", ErrBusy, planID, cur.ID)
		}
	}
	cleanupRun := *run
	cleanupRun.Mode = ModeTestCleanup
	cleanupRun.State = StateRunning
	o.activeRuns[planID] = &cleanupRun
	return nil
}

// forgetRun drops a plan's active run once it is no longer active.
func (o *Orchestrator) forgetRun(planID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.activeRuns, planID)
}

// runStep appends a step to the run, reports it while it runs, runs fill and
// reports the outcome; a nil report keeps the history quiet for callers that
// only want the run itself.
func (o *Orchestrator) runStep(run *ExecutionRun, report func(StepLog), name, bootGroupID, vmUUID string, fill func(*StepLog) error) error {
	step := StepLog{
		ID:          newID(),
		Name:        name,
		BootGroupID: bootGroupID,
		VMUUID:      vmUUID,
		Status:      StepRunning,
		Started:     time.Now().UTC(),
	}
	run.Steps = append(run.Steps, step)
	if report != nil {
		report(run.Steps[len(run.Steps)-1])
	}
	err := fill(&step)
	now := time.Now().UTC()
	step.Finished = &now
	if err != nil {
		step.Status = StepFailed
		step.Error = err.Error()
		o.log.Warn("step failed", "run", run.ID, "step", name, "error", err)
	} else if step.Status == StepRunning {
		step.Status = StepSucceeded
	}
	run.Steps[len(run.Steps)-1] = step
	if report != nil {
		report(step)
	}
	return err
}

// tearDown stops and undefines the test domains, destroys the test clones and
// removes the run's seed ISOs — every re-addressed machine has its own; none
// of its errors may mask the run's own error.
func (o *Orchestrator) tearDown(run *ExecutionRun, defined []string, clones []CloneInfo) {
	for _, id := range defined {
		if err := o.stopTestDomain(id); err != nil {
			o.log.Warn("tear-down: stop domain", "run", run.ID, "domain", id, "error", err)
		}
		if err := o.virt.UndefineDomain(id); err != nil {
			o.log.Warn("tear-down: undefine domain", "run", run.ID, "domain", id, "error", err)
		}
	}
	if len(clones) > 0 {
		if err := DestroyTestClones(context.Background(), o.zfs, clones); err != nil {
			o.log.Warn("tear-down: destroy clones", "run", run.ID, "error", err)
		}
	}
	seeds, err := seedISOsOf(o.stateDir, run.ID)
	if err != nil {
		o.log.Warn("tear-down: find seed ISOs", "run", run.ID, "error", err)
		return
	}
	for _, seed := range seeds {
		if err := os.Remove(seed); err != nil && !errors.Is(err, os.ErrNotExist) {
			o.log.Warn("tear-down: remove seed ISO", "run", run.ID, "path", seed, "error", err)
		}
	}
}

// stopTestDomain stops a test domain that is not off yet; the state strings
// are those of internal/virt, which the UI and the libvirt layer share.
func (o *Orchestrator) stopTestDomain(uuid string) error {
	state, err := o.virt.DomainState(uuid)
	if err != nil {
		return err
	}
	if state == "shutoff" {
		return nil
	}
	return o.virt.StopDomain(uuid, true)
}

// resolveDomainXML returns a machine's source definition, falling back to the
// last copy this orchestrator saw of it when the live fetch fails. The cache
// is guarded by o.mu: concurrent runs of different plans share it.
func (o *Orchestrator) resolveDomainXML(uuid string) (string, error) {
	doc, err := o.virt.GetDomainXML(uuid)
	if err == nil {
		o.mu.Lock()
		o.xmlCache[uuid] = doc
		o.mu.Unlock()
		return doc, nil
	}
	o.mu.Lock()
	cached, ok := o.xmlCache[uuid]
	o.mu.Unlock()
	if ok {
		return cached, nil
	}
	return "", err
}

// resolveDomainXMLWith fetches a domain's XML using preferred virt controller,
// falls back to xmlCache, then fallback virt controller, and finally o.resolveDomainXML.
func (o *Orchestrator) resolveDomainXMLWith(uuid string, preferred, fallback VirtController) (string, error) {
	if preferred != nil {
		if doc, err := preferred.GetDomainXML(uuid); err == nil {
			o.mu.Lock()
			o.xmlCache[uuid] = doc
			o.mu.Unlock()
			return doc, nil
		}
	}
	o.mu.Lock()
	cached, ok := o.xmlCache[uuid]
	o.mu.Unlock()
	if ok {
		return cached, nil
	}
	if fallback != nil {
		if doc, err := fallback.GetDomainXML(uuid); err == nil {
			o.mu.Lock()
			o.xmlCache[uuid] = doc
			o.mu.Unlock()
			return doc, nil
		}
	}
	if o.virt != nil {
		return o.resolveDomainXML(uuid)
	}
	return "", fmt.Errorf("%w: domain XML for %s", ErrNotFound, uuid)
}

// validatePlanAgainstGroup rejects plans whose machines are not all in their
// protection group or that start one machine in two boot groups.
func validatePlanAgainstGroup(plan RecoveryPlan, group ProtectionGroup) error {
	inGroup := make(map[string]bool, len(group.VMUUIDs))
	for _, id := range group.VMUUIDs {
		inGroup[id] = true
	}
	inBootGroup := map[string]string{}
	for _, bg := range plan.BootGroups {
		for _, vm := range bg.VMs {
			if !inGroup[vm.VMUUID] {
				return fmt.Errorf("%w: machine %q of plan %q is not in protection group %q", ErrInvalid, vm.SourceName, plan.Name, group.Name)
			}
			if prev, dup := inBootGroup[vm.VMUUID]; dup {
				return fmt.Errorf("%w: machine %q of plan %q starts in two boot groups (%q and %q)", ErrInvalid, vm.SourceName, plan.Name, prev, bg.ID)
			}
			inBootGroup[vm.VMUUID] = bg.ID
		}
	}
	return nil
}

// groupDisks reads a machine's source definition and returns the disks to
// replicate: every non-removable disk must be a zvol, or the machine cannot
// be replicated and the pre-flight fails with where it stands.
func groupDisks(uuid, doc, targetPool string) ([]ReplicatedDisk, error) {
	var d libvirtxml.Domain
	if err := d.Unmarshal(doc); err != nil {
		return nil, fmt.Errorf("machine %s: parse domain XML: %w", uuid, err)
	}
	if d.Devices == nil {
		return nil, fmt.Errorf("machine %s: no devices in the domain XML", uuid)
	}
	disks := []ReplicatedDisk{}
	for i := range d.Devices.Disks {
		dk := &d.Devices.Disks[i]
		if dk.Device == "cdrom" || dk.Device == "floppy" {
			continue
		}
		dev, src := "", ""
		if dk.Target != nil {
			dev = dk.Target.Dev
		}
		if dk.Source != nil {
			if dk.Source.Block != nil {
				src = dk.Source.Block.Dev
			} else if dk.Source.File != nil {
				src = dk.Source.File.File
			}
		}
		if !strings.HasPrefix(src, "/dev/zvol/") {
			return nil, fmt.Errorf("machine %s: disk %s is not a replicated zvol (%s)", uuid, dev, src)
		}
		volume := path.Base(src)
		disks = append(disks, ReplicatedDisk{
			Dev:          dev,
			SourcePool:   path.Dir(strings.TrimPrefix(src, "/dev/zvol/")),
			SourceVolume: volume,
			TargetPool:   targetPool,
			TargetVolume: volume,
		})
	}
	if len(disks) == 0 {
		return nil, fmt.Errorf("machine %s: no disks to replicate", uuid)
	}
	return disks, nil
}

// diskCloneMap points every disk of a machine (by its target device) at the
// test clone its replica was cloned into.
func diskCloneMap(disks []ReplicatedDisk, clones []CloneInfo) (map[string]string, error) {
	bySource := make(map[string]string, len(clones))
	for _, c := range clones {
		bySource[c.SourceDataset] = c.CloneDataset
	}
	m := make(map[string]string, len(disks))
	for _, d := range disks {
		clone, ok := bySource[d.TargetPool+"/"+d.TargetVolume]
		if !ok {
			return nil, fmt.Errorf("no test clone for disk %s of %s", d.Dev, d.SourceVolume)
		}
		m[d.Dev] = "/dev/zvol/" + clone
	}
	return m, nil
}

// testCloneDatasets lists the test clones a run left in the target pool, by
// their virtomir-test-<runID> name, so the clean-up trusts the pool, not the
// run history alone. The clones are volumes (block devices for the test
// domains), so the list covers volumes and filesystems both.
func testCloneDatasets(ctx context.Context, zfs ZFSRunner, targetPool, runID string) ([]string, error) {
	out, err := zfs.Run(ctx, "zfs", "list", "-r", "-H", "-t", "volume,filesystem", "-o", "name", targetPool)
	if err != nil {
		return nil, err
	}
	marker := "/virtomir-test-" + runID + "-"
	var datasets []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && strings.Contains(line, marker) {
			datasets = append(datasets, line)
		}
	}
	return datasets, nil
}

// drVMName is what a test domain is called: the plan's DR name for the machine
// or, without one, the source name with a -test suffix.
func drVMName(vm PlanVM) string {
	if vm.DRVMName != "" {
		return vm.DRVMName
	}
	return vm.SourceName + "-test"
}

// GetDiskMappings reads every machine of the protection group and returns its
// replicated disk mapping: which source volume maps to which target volume,
// the pool and volume names, and the latest replica snapshot found, so the UI
// can show the group's VM disk mappings on the group details page.
func (o *Orchestrator) GetDiskMappings(ctx context.Context, group ProtectionGroup) ([]VMDiskMapping, error) {
	mappings := make([]VMDiskMapping, 0, len(group.VMUUIDs))
	for _, uuid := range group.VMUUIDs {
		doc, err := o.resolveDomainXML(uuid)
		if err != nil {
			return nil, fmt.Errorf("machine %s: %w", uuid, err)
		}
		var d libvirtxml.Domain
		if err := d.Unmarshal(doc); err != nil {
			return nil, fmt.Errorf("machine %s: parse domain XML: %w", uuid, err)
		}
		disks, err := groupDisks(uuid, doc, group.TargetPool)
		if err != nil {
			return nil, err
		}
		mappings = append(mappings, VMDiskMapping{
			VMUUID:     uuid,
			DomainName: d.Name,
			Disks:      disks,
		})
	}
	return mappings, nil
}

// hookEnv is the VIRTOMIR_DR_* variables a hook script sees for one run.
func hookEnv(plan RecoveryPlan, mode, step string) []string {
	return []string{
		"VIRTOMIR_DR_PLAN_ID=" + plan.ID,
		"VIRTOMIR_DR_PLAN_NAME=" + plan.Name,
		"VIRTOMIR_DR_GROUP_ID=" + plan.ProtectionGroupID,
		"VIRTOMIR_DR_MODE=" + mode,
		"VIRTOMIR_DR_STEP=" + step,
	}
}

// hookTimeout is the plan's hook timeout or the default without one.
func hookTimeout(sec int) time.Duration {
	if sec <= 0 {
		return defaultHookTimeout
	}
	return time.Duration(sec) * time.Second
}

// seedISOPath is where one machine's cloud-init seed ISO of a run lands in
// the state dir, one per re-addressed machine of the run.
func seedISOPath(stateDir, runID, vmUUID string) string {
	return filepath.Join(stateDir, fmt.Sprintf("dr-seed-%s-%s.iso", runID, vmUUID))
}

// seedISOsOf lists the cloud-init seed ISOs a run left in the state dir, one
// per re-addressed machine it got as far as creating. An empty state dir (the
// orchestrator was wired without one) has none by definition.
func seedISOsOf(stateDir, runID string) ([]string, error) {
	if stateDir == "" {
		return nil, nil
	}
	return filepath.Glob(filepath.Join(stateDir, "dr-seed-"+runID+"*.iso"))
}
