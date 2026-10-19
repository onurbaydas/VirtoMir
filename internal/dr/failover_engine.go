package dr

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// FailoverEngine executes real disaster failover and failback, holding the
// controllers and runners its steps need so callers only pass a plan, options
// and a report. It wraps the orchestrator's ExecuteRealFailover and
// ExecuteFailback with pre-bound prodVirt, drVirt, zfs, rep and hooks.
type FailoverEngine struct {
	orchestrator *Orchestrator
	prodVirt     VirtController
	drVirt       VirtController
	zfs          ZFSRunner
	rep          ReplicationSyncer
	hooks        HookRunner
	log          *slog.Logger
}

// NewFailoverEngine wires a failover engine over the orchestrator and the
// drivers its steps call: prodVirt (the production host) and drVirt (the DR
// host). When drVirt is nil it falls back to the orchestrator's own virt
// controller; when prodVirt is nil the pre-sync reachability check is skipped
// (forced failover only).
func NewFailoverEngine(o *Orchestrator, prodVirt, drVirt VirtController, zfs ZFSRunner, rep ReplicationSyncer, hooks HookRunner, log *slog.Logger) *FailoverEngine {
	if log == nil {
		log = slog.Default()
	}
	return &FailoverEngine{
		orchestrator: o,
		prodVirt:     prodVirt,
		drVirt:       drVirt,
		zfs:          zfs,
		rep:          rep,
		hooks:        hooks,
		log:          log,
	}
}

// ExecuteRealFailover runs a real failover for planID, using the engine's bound
// controllers and runners instead of accepting them per-call.
func (e *FailoverEngine) ExecuteRealFailover(ctx context.Context, planID string, opts FailoverOpts, report func(StepLog)) (ExecutionRun, error) {
	run, err := e.orchestrator.ExecuteRealFailover(ctx, planID, opts, e.prodVirt, e.drVirt, e.zfs, e.rep, e.hooks, report)
	if err != nil {
		e.log.Info("real failover job finished with error", "plan", planID, "err", err)
		return run, err
	}
	e.log.Info("real failover job succeeded", "plan", planID, "run", run.ID)
	return run, nil
}

// ExecuteFailback runs a failback for planID, using the engine's bound
// controllers and runners.
func (e *FailoverEngine) ExecuteFailback(ctx context.Context, planID string, report func(StepLog)) (ExecutionRun, error) {
	run, err := e.orchestrator.ExecuteFailback(ctx, planID, e.prodVirt, e.drVirt, e.zfs, e.hooks, report)
	if err != nil {
		e.log.Info("failback job finished with error", "plan", planID, "err", err)
		return run, err
	}
	e.log.Info("failback job succeeded", "plan", planID, "run", run.ID)
	return run, nil
}

// SyncGroup triggers an immediate replication pass for the protection group
// identified by groupID. It asks the replication layer (internal/zfsrep) to
// start a pass; an empty rep means DR sync is not configured on this host.
func (e *FailoverEngine) SyncGroup(ctx context.Context, groupID string) (time.Time, error) {
	if e.rep == nil {
		return time.Time{}, fmt.Errorf("%w: replication syncer is not available", ErrInvalid)
	}
	st, err := e.rep.Start(groupID)
	if err != nil {
		return time.Time{}, err
	}
	return st.LastSync, nil
}
