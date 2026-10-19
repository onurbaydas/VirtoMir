package dr

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/onurbaydas/virtomir/internal/zfsrep"
)

type unreachableVirt struct {
	fakeVirt
}

func (u *unreachableVirt) DomainState(uuid string) (string, error) {
	return "", errors.New("connection refused: primary host is down")
}

func (u *unreachableVirt) StopDomain(uuid string, force bool) error {
	return errors.New("connection refused: primary host is down")
}

func (u *unreachableVirt) GetDomainXML(uuid string) (string, error) {
	return "", errors.New("connection refused: primary host is down")
}

type fakeSyncer struct {
	started []string
	err     error
}

func (s *fakeSyncer) Start(id string) (zfsrep.Status, error) {
	s.started = append(s.started, id)
	if s.err != nil {
		return zfsrep.Status{State: "failed", Error: s.err.Error()}, s.err
	}
	return zfsrep.Status{State: "syncing", LastSync: time.Now().UTC()}, nil
}

// TestRealFailoverGraceful asserts that when primary host is reachable, graceful power-off
// of primary VMs runs, a final replication pass is triggered, DR datasets are promoted,
// and DR VMs are booted in proper group order.
func TestRealFailoverGraceful(t *testing.T) {
	ctx := context.Background()
	d := newDRTest(t)
	d.createPlan(t)

	// Primary virt controller (production host)
	prodVirt := &fakeVirt{
		xmls: map[string]string{
			"vm-db1":  blockDomainXML("db1", "db-disk0"),
			"vm-web1": blockDomainXML("web1", "web-disk0"),
		},
		docs: map[string]string{},
	}
	// DR virt controller (target host)
	drVirt := &fakeVirt{
		xmls: map[string]string{},
		docs: map[string]string{},
	}

	syncer := &fakeSyncer{}
	reported := []StepLog{}

	run, err := d.orch.ExecuteRealFailover(
		ctx,
		d.planID,
		FailoverOpts{Force: false, SkipPreSync: false},
		prodVirt,
		drVirt,
		d.zfs,
		syncer,
		d.hooks,
		func(s StepLog) { reported = append(reported, s) },
	)
	if err != nil {
		t.Fatalf("real failover failed: %v", err)
	}

	// 1. Assert ExecutionRun state and mode
	if run.Mode != ModeRealFailover {
		t.Errorf("run.Mode = %v, want %v", run.Mode, ModeRealFailover)
	}
	if run.State != StateSucceeded {
		t.Errorf("run.State = %v, want %v", run.State, StateSucceeded)
	}

	// 2. Assert graceful power off of primary VMs
	if len(prodVirt.stopped) != 2 {
		t.Errorf("prodVirt.stopped = %v, want 2 VMs stopped", prodVirt.stopped)
	}

	// 3. Assert final replication pass was triggered
	if len(syncer.started) != 1 || syncer.started[0] != d.groupID {
		t.Errorf("syncer.started = %v, want [%s]", syncer.started, d.groupID)
	}

	// 4. Assert replica datasets were promoted (readonly=off)
	if _, ok := d.zfs.find("zfs set readonly=off tank/dr/db-disk0"); !ok {
		t.Errorf("zfs set readonly=off not called for db-disk0: %v", d.zfs.calls)
	}
	if _, ok := d.zfs.find("zfs set readonly=off tank/dr/web-disk0"); !ok {
		t.Errorf("zfs set readonly=off not called for web-disk0: %v", d.zfs.calls)
	}

	// 5. Assert DR VMs were defined and started in boot order (db1 before web1)
	if len(drVirt.started) != 2 {
		t.Fatalf("drVirt.started = %v, want 2 VMs started", drVirt.started)
	}
	dbIdx := opIndex(drVirt.ops, "start:"+drVirt.started[0])
	webIdx := opIndex(drVirt.ops, "start:"+drVirt.started[1])
	if dbIdx >= webIdx {
		t.Errorf("boot order violated: db at %d, web at %d (ops: %v)", dbIdx, webIdx, drVirt.ops)
	}

	// 6. Assert ProtectionGroup status and active host updated
	group, ok := d.store.GetProtectionGroup(d.groupID)
	if !ok {
		t.Fatal("group not found in store")
	}
	if group.Status != StatusFailedOver {
		t.Errorf("group.Status = %q, want %q", group.Status, StatusFailedOver)
	}
	if group.ActiveHost != "dr-1" {
		t.Errorf("group.ActiveHost = %q, want dr-1", group.ActiveHost)
	}
}

// TestRealFailoverForced asserts that when primary host is unreachable, failover fails
// when Force=false with ErrBusy, and succeeds when Force=true using the latest replica snapshot.
func TestRealFailoverForced(t *testing.T) {
	ctx := context.Background()

	t.Run("FailsWhenNotForced", func(t *testing.T) {
		d := newDRTest(t)
		d.createPlan(t)

		unreachableProd := &unreachableVirt{}
		drVirt := &fakeVirt{
			xmls: map[string]string{
				"vm-db1":  blockDomainXML("db1", "db-disk0"),
				"vm-web1": blockDomainXML("web1", "web-disk0"),
			},
			docs: map[string]string{},
		}

		syncer := &fakeSyncer{}
		_, err := d.orch.ExecuteRealFailover(
			ctx,
			d.planID,
			FailoverOpts{Force: false, SkipPreSync: false},
			unreachableProd,
			drVirt,
			d.zfs,
			syncer,
			d.hooks,
			nil,
		)
		if err == nil {
			t.Fatal("expected failover to fail when primary host unreachable and Force=false, but got nil")
		}
		if !errors.Is(err, ErrBusy) {
			t.Errorf("expected error wrapping ErrBusy, got: %v", err)
		}

		// ProtectionGroup must remain protected
		group, _ := d.store.GetProtectionGroup(d.groupID)
		if group.Status != StatusProtected {
			t.Errorf("group.Status = %q, want %q", group.Status, StatusProtected)
		}
	})

	t.Run("SucceedsWhenForced", func(t *testing.T) {
		d := newDRTest(t)
		d.createPlan(t)

		unreachableProd := &unreachableVirt{}
		drVirt := &fakeVirt{
			xmls: map[string]string{
				"vm-db1":  blockDomainXML("db1", "db-disk0"),
				"vm-web1": blockDomainXML("web1", "web-disk0"),
			},
			docs: map[string]string{},
		}

		syncer := &fakeSyncer{}
		run, err := d.orch.ExecuteRealFailover(
			ctx,
			d.planID,
			FailoverOpts{Force: true, SkipPreSync: false},
			unreachableProd,
			drVirt,
			d.zfs,
			syncer,
			d.hooks,
			nil,
		)
		if err != nil {
			t.Fatalf("forced failover failed: %v", err)
		}
		if run.State != StateSucceeded {
			t.Errorf("run.State = %v, want %v", run.State, StateSucceeded)
		}

		// Disaster warning logged in step output
		preStep, ok := stepByName(run, "Production host pre-sync")
		if !ok {
			t.Fatal("pre-sync step not found")
		}
		const wantWarn = "Primary host unreachable; proceeding with forced failover from latest replica snapshot"
		if preStep.Output != wantWarn {
			t.Errorf("pre-sync step output = %q, want exact disaster warning %q", preStep.Output, wantWarn)
		}

		// DR VMs defined and started
		if len(drVirt.started) != 2 {
			t.Errorf("drVirt.started = %v, want 2 VMs started", drVirt.started)
		}

		// ProtectionGroup updated to failed_over
		group, _ := d.store.GetProtectionGroup(d.groupID)
		if group.Status != StatusFailedOver {
			t.Errorf("group.Status = %q, want %q", group.Status, StatusFailedOver)
		}
		if group.ActiveHost != "dr-1" {
			t.Errorf("group.ActiveHost = %q, want dr-1", group.ActiveHost)
		}
	})
}

// TestFailbackFullCycle asserts that failback executes reverse sync while DR VMs run,
// stops DR VMs, performs final delta sync, restores VMs on primary host, and flips status
// back to "protected".
func TestFailbackFullCycle(t *testing.T) {
	ctx := context.Background()
	d := newDRTest(t)
	d.createPlan(t)

	// Set protection group to failed_over state initially
	if err := d.store.SetGroupActiveHost(d.groupID, "dr-1", StatusFailedOver); err != nil {
		t.Fatalf("set failed_over: %v", err)
	}

	prodVirt := &fakeVirt{
		xmls: map[string]string{
			"vm-db1":  blockDomainXML("db1", "db-disk0"),
			"vm-web1": blockDomainXML("web1", "web-disk0"),
		},
		docs: map[string]string{},
	}
	drVirt := &fakeVirt{
		xmls: map[string]string{
			"vm-db1":  blockDomainXML("db1", "db-disk0"),
			"vm-web1": blockDomainXML("web1", "web-disk0"),
		},
		docs: map[string]string{},
	}

	reported := []StepLog{}
	run, err := d.orch.ExecuteFailback(
		ctx,
		d.planID,
		prodVirt,
		drVirt,
		d.zfs,
		d.hooks,
		func(s StepLog) { reported = append(reported, s) },
	)
	if err != nil {
		t.Fatalf("failback failed: %v", err)
	}

	if run.Mode != ModeFailback {
		t.Errorf("run.Mode = %v, want %v", run.Mode, ModeFailback)
	}
	if run.State != StateSucceeded {
		t.Errorf("run.State = %v, want %v", run.State, StateSucceeded)
	}

	// 1. Assert reverse sync while DR VMs run (snapshot + stream)
	if _, ok := d.zfs.find("zfs snapshot tank/dr/db-disk0@virtomir-dr-reverse-"); !ok {
		t.Errorf("initial reverse snapshot not taken: %v", d.zfs.calls)
	}
	if _, ok := d.zfs.find("zfs send -i @"); !ok {
		t.Errorf("zfs send not called: %v", d.zfs.calls)
	}

	// 2. Assert DR VMs stopped during quiesce
	if len(drVirt.stopped) != 2 {
		t.Errorf("drVirt.stopped = %v, want 2 VMs stopped during quiesce", drVirt.stopped)
	}

	// 3. Assert final delta snapshot taken
	if _, ok := d.zfs.find("-final"); !ok {
		t.Errorf("final reverse snapshot not taken: %v", d.zfs.calls)
	}

	// 4. Assert primary datasets restored (readonly=off)
	if _, ok := d.zfs.find("zfs set readonly=off tank/vms/db-disk0"); !ok {
		t.Errorf("zfs set readonly=off not called on primary db-disk0: %v", d.zfs.calls)
	}
	if _, ok := d.zfs.find("zfs set readonly=off tank/vms/web-disk0"); !ok {
		t.Errorf("zfs set readonly=off not called on primary web-disk0: %v", d.zfs.calls)
	}

	// 5. Assert primary VMs defined and started
	if len(prodVirt.started) != 2 {
		t.Errorf("prodVirt.started = %v, want 2 primary VMs started", prodVirt.started)
	}

	// 6. Assert DR VMs undefined on DR host
	if len(drVirt.undefined) != 2 {
		t.Errorf("drVirt.undefined = %v, want 2 DR VMs undefined", drVirt.undefined)
	}

	// 7. Assert ProtectionGroup restored to protected and primary activeHost
	group, _ := d.store.GetProtectionGroup(d.groupID)
	if group.Status != StatusProtected {
		t.Errorf("group.Status = %q, want %q", group.Status, StatusProtected)
	}
	if group.ActiveHost != "src-1" {
		t.Errorf("group.ActiveHost = %q, want src-1", group.ActiveHost)
	}
}
