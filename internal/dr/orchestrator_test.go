package dr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"libvirt.org/go/libvirtxml"
)

var discardLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

// drTestSnapshot is the replica snapshot tag the fake zfs reports.
const drTestSnapshot = "virtomir-rep-20261019-020000"

// fakeVirt is an in-memory libvirt: it serves the source XMLs from xmls, mints
// a new domain for every define (kept in docs), counts starts and records
// every operation in ops, so tests can assert what ran and when.
type fakeVirt struct {
	xmls      map[string]string
	docs      map[string]string // uuid -> xml as defined
	defined   []string
	started   []string
	stopped   []string
	undefined []string
	ops       []string
	failStart int // 1-based: this many starts fail
}

func (v *fakeVirt) GetDomainXML(uuid string) (string, error) {
	xml, ok := v.xmls[uuid]
	if !ok {
		return "", fmt.Errorf("%w: domain %q", ErrNotFound, uuid)
	}
	return xml, nil
}

func (v *fakeVirt) DefineDomain(xml string) (string, error) {
	id := fmt.Sprintf("dr-domain-%d", len(v.defined)+1)
	v.defined = append(v.defined, id)
	v.docs[id] = xml
	v.ops = append(v.ops, "define:"+id)
	return id, nil
}

func (v *fakeVirt) StartDomain(uuid string) error {
	if len(v.started)+1 == v.failStart {
		v.ops = append(v.ops, "start-fail:"+uuid)
		return errors.New("libvirt: internal error")
	}
	v.started = append(v.started, uuid)
	v.ops = append(v.ops, "start:"+uuid)
	return nil
}

func (v *fakeVirt) StopDomain(uuid string, force bool) error {
	v.stopped = append(v.stopped, uuid)
	v.ops = append(v.ops, "stop:"+uuid)
	return nil
}

func (v *fakeVirt) UndefineDomain(uuid string) error {
	v.undefined = append(v.undefined, uuid)
	v.ops = append(v.ops, "undefine:"+uuid)
	return nil
}

func (v *fakeVirt) DomainState(uuid string) (string, error) {
	return "running", nil
}

type hookCall struct {
	script  string
	env     []string
	timeout time.Duration
}

// fakeHooks answers the orchestrator's hook calls in order; errs[i] fails the
// (i+1)-th call and output is what a call that does not fail returns.
type fakeHooks struct {
	errs   []error
	output string
	calls  []hookCall
}

func (h *fakeHooks) RunHook(ctx context.Context, script string, env []string, timeout time.Duration) ([]byte, error) {
	h.calls = append(h.calls, hookCall{script, env, timeout})
	if i := len(h.calls) - 1; i < len(h.errs) && h.errs[i] != nil {
		return nil, h.errs[i]
	}
	return []byte(h.output), nil
}

// zfsSim answers the zfs calls the orchestrator makes on top of a fakeRunner:
// snapshot lists report the replica snapshot when present, clone/destroy keep
// the list of what is in the pool, and filesystem lists report that list.
type zfsSim struct {
	*fakeRunner
	clones     []string
	snapshot   string
	destroyErr error // when set, destroys fail (a pool gone dark)
}

func newZFSsim(snapshot string) *zfsSim {
	s := &zfsSim{fakeRunner: &fakeRunner{}, snapshot: snapshot}
	s.handler = s.handle
	return s
}

func (z *zfsSim) handle(_ string, args []string) ([]byte, error) {
	if len(args) == 0 {
		return nil, nil
	}
	switch args[0] {
	case "list":
		typ := ""
		recursive := false
		for i, a := range args {
			if a == "-t" && i+1 < len(args) {
				typ = args[i+1]
			}
			if a == "-r" {
				recursive = true
			}
		}
		if typ == "snapshot" {
			if z.snapshot == "" {
				return []byte(""), nil
			}
			return []byte(args[len(args)-1] + "@" + z.snapshot + "\n"), nil
		}
		target := args[len(args)-1]
		if !recursive && target != "" {
			return []byte(target + "\n"), nil
		}
		return []byte(strings.Join(z.clones, "\n")), nil
	case "clone":
		z.clones = append(z.clones, args[2])
	case "destroy":
		if z.destroyErr != nil {
			return nil, z.destroyErr
		}
		kept := z.clones[:0]
		for _, c := range z.clones {
			if c != args[2] {
				kept = append(kept, c)
			}
		}
		z.clones = kept
	case "get":
		return []byte("on\n"), nil
	}
	return nil, nil
}

// blockDomainXML is a source-machine definition with a single replicated zvol
// disk, one installation CD and a network interface.
func blockDomainXML(name, volume string) string {
	return fmt.Sprintf(`<domain type='kvm'>
  <name>%s</name>
  <uuid>11111111-2222-4333-8444-555555555555</uuid>
  <memory unit='KiB'>1048576</memory>
  <os>
    <type arch='x86_64' machine='pc-q35-10.0'>hvm</type>
  </os>
  <devices>
    <disk type='block' device='disk'>
      <driver name='qemu' type='raw'/>
      <source dev='/dev/zvol/tank/vms/%s'/>
      <target dev='vda' bus='virtio'/>
    </disk>
    <disk type='file' device='cdrom'>
      <driver name='qemu' type='raw'/>
      <source file='/var/lib/libvirt/images/install.iso'/>
      <target dev='sda' bus='sata'/>
      <readonly/>
    </disk>
    <interface type='network'>
      <mac address='52:54:00:aa:bb:cc'/>
      <source network='default'/>
      <model type='virtio'/>
    </interface>
  </devices>
</domain>`, name, volume)
}

type drTest struct {
	store   *Store
	virt    *fakeVirt
	zfs     *zfsSim
	hooks   *fakeHooks
	orch    *Orchestrator
	state   string
	planID  string
	groupID string
}

// newDRTest builds a two-machine protection group (db1, web1) with replicas
// present, over a real store on a temp file.
func newDRTest(t *testing.T) *drTest {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "dr.json"))
	if err != nil {
		t.Fatal(err)
	}
	group, err := store.CreateProtectionGroup(ProtectionGroup{
		Name: "prod-stack", SourceHost: "src-1", TargetHost: "dr-1",
		TargetPool: "tank/dr", VMUUIDs: []string{"vm-db1", "vm-web1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	d := &drTest{
		store:   store,
		virt:    &fakeVirt{xmls: map[string]string{"vm-db1": blockDomainXML("db1", "db-disk0"), "vm-web1": blockDomainXML("web1", "web-disk0")}, docs: map[string]string{}},
		zfs:     newZFSsim(drTestSnapshot),
		hooks:   &fakeHooks{output: "hook ran"},
		state:   t.TempDir(),
		groupID: group.ID,
	}
	d.orch = NewOrchestrator(store, d.zfs, d.virt, d.hooks, discardLogger, d.state)
	return d
}

// planTwoGroups is a plan whose boot groups are declared out of order (web
// first, db second): the web tier waits for the delayed db tier.
func (d *drTest) planTwoGroups() RecoveryPlan {
	return RecoveryPlan{
		Name:              "re-stack",
		ProtectionGroupID: d.groupID,
		PreHookScript:     "echo pre",
		PostHookScript:    "echo post",
		HookTimeoutSec:    5,
		BootGroups: []BootGroup{
			{
				ID:    "g-web",
				Name:  "web",
				Order: 2,
				VMs: []PlanVM{{
					VMUUID:          "vm-web1",
					SourceName:      "web1",
					DRVMName:        "web1-dr",
					NetworkMappings: []NetworkMap{{SourceNetwork: "default", TargetNetwork: "dr-net", IsolatedTestNetwork: "dr-tests"}},
					IPReaddressing:  &IPConfig{InterfaceMAC: "52:54:00:aa:bb:cc", IPv4Address: "10.20.0.5/24", IPv4Gateway: "10.20.0.1"},
				}},
			},
			{
				ID:           "g-db",
				Name:         "db",
				Order:        1,
				DelaySeconds: 1,
				VMs:          []PlanVM{{VMUUID: "vm-db1", SourceName: "db1"}},
			},
		},
	}
}

func (d *drTest) createPlan(t *testing.T) RecoveryPlan {
	t.Helper()
	plan, err := d.store.CreatePlan(d.planTwoGroups())
	if err != nil {
		t.Fatal(err)
	}
	d.planID = plan.ID
	return plan
}

func stepNames(run ExecutionRun) []string {
	names := make([]string, 0, len(run.Steps))
	for _, s := range run.Steps {
		names = append(names, s.Name)
	}
	return names
}

func stepByName(run ExecutionRun, name string) (StepLog, bool) {
	for _, s := range run.Steps {
		if s.Name == name {
			return s, true
		}
	}
	return StepLog{}, false
}

func hasEnv(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}

func opIndex(ops []string, op string) int {
	for i, o := range ops {
		if o == op {
			return i
		}
	}
	return -1
}

func TestTestFailoverSuccess(t *testing.T) {
	ctx := context.Background()
	d := newDRTest(t)
	d.createPlan(t)

	start := time.Now()
	reported := []StepLog{}
	run, err := d.orch.ExecuteTestFailover(ctx, d.planID, func(s StepLog) { reported = append(reported, s) })
	if err != nil {
		t.Fatalf("failover: %v", err)
	}
	elapsed := time.Since(start)

	// The boot groups ran in order: db (declared second) before web, with the
	// group's one-second delay in between.
	want := []string{"Pre-flight & snapshot discovery", "Create test clones", "Pre-hook", "Start db1-test", "Waiting delay", "Start web1-dr", "Post-hook"}
	if got := stepNames(run); !reflect.DeepEqual(got, want) {
		t.Fatalf("steps = %v, want %v", got, want)
	}
	if elapsed < time.Second {
		t.Errorf("the delay between the groups did not occur: ran in %s", elapsed)
	}
	for _, s := range run.Steps {
		if s.Status != StepSucceeded {
			t.Errorf("step %q = %s: %s", s.Name, s.Status, s.Error)
		}
	}
	if run.State != StateTestActive {
		t.Errorf("run state = %s, want test_active", run.State)
	}
	if len(reported) != 2*len(run.Steps) {
		t.Errorf("report got %d events, want %d (start and end of each step)", len(reported), 2*len(run.Steps))
	}

	// Both machines got test clones, from the replica snapshot.
	if _, ok := d.zfs.find("zfs clone tank/dr/db-disk0@" + drTestSnapshot + " tank/dr/virtomir-test-" + run.ID + "-db-disk0"); !ok {
		t.Errorf("db clone missing: %v", d.zfs.calls)
	}
	if _, ok := d.zfs.find("zfs clone tank/dr/web-disk0@" + drTestSnapshot + " tank/dr/virtomir-test-" + run.ID + "-web-disk0"); !ok {
		t.Errorf("web clone missing: %v", d.zfs.calls)
	}
	if len(d.zfs.clones) != 2 {
		t.Errorf("clones in the pool = %v", d.zfs.clones)
	}

	// The db domain runs on its clone, keeps its network and has the
	// installation media ejected.
	var db libvirtxml.Domain
	if err := db.Unmarshal(d.virt.docs["dr-domain-1"]); err != nil {
		t.Fatal(err)
	}
	if db.Name != "db1-test" {
		t.Errorf("db domain name = %q, want db1-test", db.Name)
	}
	dbDisk := diskByTarget(t, &db, "vda")
	wantDev := "/dev/zvol/tank/dr/virtomir-test-" + run.ID + "-db-disk0"
	if dbDisk.Source == nil || dbDisk.Source.Block == nil || dbDisk.Source.Block.Dev != wantDev {
		t.Errorf("db disk not on its clone: %+v", dbDisk)
	}
	if !ejected(diskByTarget(t, &db, "sda")) {
		t.Errorf("db installation media not ejected: %+v", dbDisk)
	}
	if db.Devices.Interfaces[0].Source.Network.Network != "default" {
		t.Errorf("db network remapped without a mapping: %+v", db.Devices.Interfaces[0].Source)
	}

	// The web domain is renamed by the plan, goes to the isolated test
	// network and carries the re-addressing seed.
	var web libvirtxml.Domain
	if err := web.Unmarshal(d.virt.docs["dr-domain-2"]); err != nil {
		t.Fatal(err)
	}
	if web.Name != "web1-dr" {
		t.Errorf("web domain name = %q, want web1-dr", web.Name)
	}
	if web.Devices.Interfaces[0].Source.Network.Network != "dr-tests" {
		t.Errorf("web network not isolated: %+v", web.Devices.Interfaces[0].Source)
	}
	seed := seedISOPath(d.state, run.ID, "vm-web1")
	if !hasSeed(&web, seed) {
		t.Errorf("web domain does not carry the seed ISO %q", seed)
	}
	if _, err := os.Stat(seed); err != nil {
		t.Errorf("seed ISO missing: %v", err)
	}

	// The steps that defined domains recorded their uuids, which the cleanup
	// later looks up.
	if s, _ := stepByName(run, "Start db1-test"); s.DomainUUID != "dr-domain-1" {
		t.Errorf("db step domain = %q", s.DomainUUID)
	}
	if s, _ := stepByName(run, "Start web1-dr"); s.DomainUUID != "dr-domain-2" {
		t.Errorf("web step domain = %q", s.DomainUUID)
	}

	// The hooks ran with the run's environment, in the plan's timeout.
	if len(d.hooks.calls) != 2 {
		t.Fatalf("hooks called %d times, want 2: %+v", len(d.hooks.calls), d.hooks.calls)
	}
	pre, post := d.hooks.calls[0], d.hooks.calls[1]
	if pre.timeout != 5*time.Second || post.timeout != 5*time.Second {
		t.Errorf("hook timeout = %s, want 5s (from the plan)", pre.timeout)
	}
	for _, want := range []string{
		"VIRTOMIR_DR_PLAN_ID=" + d.planID,
		"VIRTOMIR_DR_PLAN_NAME=re-stack",
		"VIRTOMIR_DR_GROUP_ID=" + d.groupID,
		"VIRTOMIR_DR_MODE=test",
		"VIRTOMIR_DR_STEP=pre",
	} {
		if !hasEnv(pre.env, want) {
			t.Errorf("pre-hook env missing %q: %v", want, pre.env)
		}
	}
	if !hasEnv(post.env, "VIRTOMIR_DR_STEP=post") {
		t.Errorf("post-hook env missing VIRTOMIR_DR_STEP=post: %v", post.env)
	}

	// A second run of the same plan is refused while the test is active.
	if _, err := d.orch.ExecuteTestFailover(ctx, d.planID, nil); !errors.Is(err, ErrBusy) {
		t.Errorf("second failover over an active test: %v, want ErrBusy", err)
	}
}

func TestTestFailoverPreHookFailure(t *testing.T) {
	ctx := context.Background()
	d := newDRTest(t)
	d.createPlan(t)
	d.hooks.errs = []error{errors.New("the database is on fire")}

	run, err := d.orch.ExecuteTestFailover(ctx, d.planID, nil)
	if err == nil {
		t.Fatal("the failed pre-hook must abort the run")
	}
	if run.State != StateFailed || !strings.Contains(run.ErrorMessage, "the database is on fire") {
		t.Errorf("run = %+v, want failed with the hook's error", run)
	}
	if _, started := stepByName(run, "Start db1-test"); started {
		t.Fatal("boot groups ran after the failed pre-hook")
	}
	last := run.Steps[len(run.Steps)-1]
	if last.Name != "Pre-hook" || last.Status != StepFailed {
		t.Errorf("last step = %+v, want the failed pre-hook", last)
	}
	if len(d.zfs.clones) != 0 {
		t.Errorf("test clones survived the failed run: %v", d.zfs.clones)
	}
	if len(d.virt.defined) != 0 {
		t.Errorf("domains defined despite the failed pre-hook: %v", d.virt.defined)
	}
	saved, ok := d.store.GetRun(run.ID)
	if !ok || saved.State != StateFailed {
		t.Errorf("the failed run was not recorded: %+v", saved)
	}

	// A healthy pre-hook lets a retry through: the busy flag was released.
	d.hooks.errs = nil
	if _, err := d.orch.ExecuteTestFailover(ctx, d.planID, nil); err != nil {
		t.Errorf("retry after a failed run: %v", err)
	}
}

func TestTestFailoverStopOnError(t *testing.T) {
	ctx := context.Background()
	d := newDRTest(t)
	plan, err := d.store.CreatePlan(RecoveryPlan{
		Name:              "stop-plan",
		ProtectionGroupID: d.groupID,
		BootGroups: []BootGroup{
			{ID: "g1", Name: "first", Order: 1, VMs: []PlanVM{{VMUUID: "vm-db1", SourceName: "db1", StopOnError: true}}},
			{ID: "g2", Name: "second", Order: 2, VMs: []PlanVM{{VMUUID: "vm-web1", SourceName: "web1"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	d.planID = plan.ID
	d.virt.failStart = 1

	run, err := d.orch.ExecuteTestFailover(ctx, d.planID, nil)
	if err == nil || !strings.Contains(err.Error(), "db1-test") {
		t.Fatalf("err = %v, want the failed start of db1-test", err)
	}
	if run.State != StateFailed {
		t.Errorf("run state = %s, want failed", run.State)
	}
	if s, ok := stepByName(run, "Start db1-test"); !ok || s.Status != StepFailed {
		t.Errorf("the failed start is not recorded: %+v", s)
	}
	if _, ok := stepByName(run, "Start web1-test"); ok {
		t.Error("the next boot group ran after a StopOnError failure")
	}
	// The defined machine is stopped and undefined, the clones are gone.
	if len(d.virt.defined) != 1 || len(d.virt.stopped) != 1 || len(d.virt.undefined) != 1 {
		t.Errorf("tear-down left a domain behind: defined=%v stopped=%v undefined=%v",
			d.virt.defined, d.virt.stopped, d.virt.undefined)
	}
	if len(d.zfs.clones) != 0 {
		t.Errorf("clones survived the failed run: %v", d.zfs.clones)
	}
}

func TestTestCleanupSuccess(t *testing.T) {
	ctx := context.Background()
	d := newDRTest(t)
	d.createPlan(t)
	run, err := d.orch.ExecuteTestFailover(ctx, d.planID, nil)
	if err != nil {
		t.Fatalf("failover: %v", err)
	}
	seed := seedISOPath(d.state, run.ID, "vm-web1")
	if _, err := os.Stat(seed); err != nil {
		t.Fatalf("the seed ISO is missing before the cleanup: %v", err)
	}

	cleaned, err := d.orch.ExecuteTestCleanup(ctx, d.planID, run.ID, nil)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if cleaned.State != StateTestCleaned {
		t.Errorf("state = %s, want test_cleaned", cleaned.State)
	}
	if want := []string{"Stop test VMs", "Undefine test domains", "Destroy test clones", "Remove seed ISO"}; !reflect.DeepEqual(stepNames(cleaned), want) {
		t.Errorf("steps = %v, want %v", stepNames(cleaned), want)
	}

	// Both test domains were stopped before they were undefined.
	if len(d.virt.stopped) != 2 || len(d.virt.undefined) != 2 {
		t.Fatalf("stopped=%v undefined=%v, want 2 of each", d.virt.stopped, d.virt.undefined)
	}
	for _, domain := range []string{"dr-domain-1", "dr-domain-2"} {
		stopAt, undefAt := opIndex(d.virt.ops, "stop:"+domain), opIndex(d.virt.ops, "undefine:"+domain)
		if stopAt == -1 || undefAt == -1 || stopAt > undefAt {
			t.Errorf("%s: stop at %d, undefine at %d, ops=%v", domain, stopAt, undefAt, d.virt.ops)
		}
	}

	// The clones are gone from the pool, and the seed ISO is removed.
	if _, ok := d.zfs.find("zfs destroy -r tank/dr/virtomir-test-" + run.ID + "-db-disk0"); !ok {
		t.Errorf("db clone not destroyed: %v", d.zfs.calls)
	}
	if len(d.zfs.clones) != 0 {
		t.Errorf("clones survived the cleanup: %v", d.zfs.clones)
	}
	if _, err := os.Stat(seed); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("seed ISO survived the cleanup: %v", err)
	}

	// The cleaned run is the newest record of the plan, and cleaning it again
	// is refused.
	runs := d.store.ListRuns(d.planID)
	if len(runs) == 0 || runs[0].ID != run.ID || runs[0].State != StateTestCleaned {
		t.Errorf("newest record of the plan = %+v, want the cleaned run", runs)
	}
	if _, err := d.orch.ExecuteTestCleanup(ctx, d.planID, run.ID, nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("a second cleanup must be refused, got %v", err)
	}
}

// TestTestFailoverPostHookFailure verifies a failing post-hook is logged in
// the run but does not take the test down: the machines stay up and the run
// becomes test_active, so the cleanup path stays reachable.
func TestTestFailoverPostHookFailure(t *testing.T) {
	ctx := context.Background()
	d := newDRTest(t)
	d.createPlan(t)
	d.hooks.errs = []error{nil, errors.New("post-hook exploded")}

	run, err := d.orch.ExecuteTestFailover(ctx, d.planID, nil)
	if err != nil {
		t.Fatalf("a failing post-hook must not fail the run: %v", err)
	}
	if run.State != StateTestActive {
		t.Fatalf("run state = %s, want test_active", run.State)
	}
	post, ok := stepByName(run, "Post-hook")
	if !ok || post.Status != StepFailed {
		t.Fatalf("the failed post-hook is not recorded: %+v", post)
	}
	if len(d.virt.started) != 2 {
		t.Errorf("the machines did not stay up: started=%v", d.virt.started)
	}
	// The busy test stays cleanable despite the failed post-hook step.
	cleaned, err := d.orch.ExecuteTestCleanup(ctx, d.planID, run.ID, nil)
	if err != nil || cleaned.State != StateTestCleaned {
		t.Errorf("cleanup after a failed post-hook: %v, state %s", err, cleaned.State)
	}
}

// TestResolveDomainXMLCacheFallback verifies a machine whose definition was
// seen once keeps booting from the cached copy when libvirt stops serving it.
func TestResolveDomainXMLCacheFallback(t *testing.T) {
	ctx := context.Background()
	d := newDRTest(t)
	d.createPlan(t)

	// A first run warms the cache and lands test_active.
	first, err := d.orch.ExecuteTestFailover(ctx, d.planID, nil)
	if err != nil {
		t.Fatalf("first failover: %v", err)
	}
	if _, err := d.orch.ExecuteTestCleanup(ctx, d.planID, first.ID, nil); err != nil {
		t.Fatalf("first cleanup: %v", err)
	}

	// libvirt stops serving both definitions; the cached copies take over.
	delete(d.virt.xmls, "vm-db1")
	delete(d.virt.xmls, "vm-web1")
	second, err := d.orch.ExecuteTestFailover(ctx, d.planID, nil)
	if err != nil {
		t.Fatalf("failover over the cached XML: %v", err)
	}
	if _, ok := stepByName(second, "Start db1-test"); !ok {
		t.Errorf("the cached definition did not boot: steps=%v", stepNames(second))
	}
}

// TestFailoverBusyAcrossRestart verifies the newest run record of the store
// keeps a plan busy for a fresh orchestrator (a daemon restart in between):
// a test left test_active by the previous session cannot be failed over
// again, and cleaning it releases the plan.
func TestFailoverBusyAcrossRestart(t *testing.T) {
	ctx := context.Background()
	d := newDRTest(t)
	d.createPlan(t)

	first, err := d.orch.ExecuteTestFailover(ctx, d.planID, nil)
	if err != nil {
		t.Fatalf("failover: %v", err)
	}
	restarted := NewOrchestrator(d.store, d.zfs, d.virt, d.hooks, discardLogger, d.state)
	if _, err := restarted.ExecuteTestFailover(ctx, d.planID, nil); !errors.Is(err, ErrBusy) {
		t.Errorf("failover over a previous session's active test: %v, want ErrBusy", err)
	}
	if _, err := restarted.ExecuteTestCleanup(ctx, d.planID, first.ID, nil); err != nil {
		t.Fatalf("cleanup by the restarted orchestrator: %v", err)
	}
	if _, err := restarted.ExecuteTestFailover(ctx, d.planID, nil); err != nil {
		t.Errorf("failover after cleaning the orphaned test: %v", err)
	}
}

// TestTestCleanupPartialRetry verifies a cleanup that fails half-way keeps
// the run test_active so the operator can fix the host and retry it, and a
// retry finishes what the first pass could not.
func TestTestCleanupPartialRetry(t *testing.T) {
	ctx := context.Background()
	d := newDRTest(t)
	d.createPlan(t)
	run, err := d.orch.ExecuteTestFailover(ctx, d.planID, nil)
	if err != nil {
		t.Fatalf("failover: %v", err)
	}

	// The pool stops answering destroys: the clone step fails, the run
	// stays test_active with the error recorded and the VMs released.
	d.zfs.destroyErr = errors.New("pool offline")
	partial, err := d.orch.ExecuteTestCleanup(ctx, d.planID, run.ID, nil)
	if err == nil || !strings.Contains(err.Error(), "pool offline") {
		t.Fatalf("err = %v, want the clone destroy failure", err)
	}
	if partial.State != StateTestActive {
		t.Errorf("state = %s, want test_active for the retry", partial.State)
	}
	if len(d.virt.stopped) != 2 || len(d.virt.undefined) != 2 {
		t.Errorf("the VMs were not released: stopped=%v undefined=%v", d.virt.stopped, d.virt.undefined)
	}
	if len(d.zfs.clones) != 2 {
		t.Errorf("the clones should survive the partial cleanup: %v", d.zfs.clones)
	}

	// The pool comes back; the retry finishes the cleanup.
	d.zfs.destroyErr = nil
	retry, err := d.orch.ExecuteTestCleanup(ctx, d.planID, run.ID, nil)
	if err != nil {
		t.Fatalf("cleanup retry: %v", err)
	}
	if retry.State != StateTestCleaned {
		t.Errorf("state = %s, want test_cleaned", retry.State)
	}
	if len(d.zfs.clones) != 0 {
		t.Errorf("clones survived the retry: %v", d.zfs.clones)
	}
}

func TestTestCleanupValidation(t *testing.T) {
	ctx := context.Background()
	d := newDRTest(t)
	d.createPlan(t)

	if _, err := d.orch.ExecuteTestCleanup(ctx, d.planID, "no-such-run", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown run: %v, want ErrNotFound", err)
	}

	// A run of another plan is not this plan's to clean.
	other, err := d.store.CreatePlan(RecoveryPlan{
		Name:              "other",
		ProtectionGroupID: d.groupID,
		BootGroups:        []BootGroup{{ID: "g", Name: "g", Order: 1, VMs: []PlanVM{{VMUUID: "vm-db1", SourceName: "db1"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := d.orch.ExecuteTestFailover(ctx, d.planID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.orch.ExecuteTestCleanup(ctx, other.ID, run.ID, nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("cleaning another plan's run: %v, want ErrInvalid", err)
	}

	// A run that failed during the failover is not active and cannot be cleaned.
	d2 := newDRTest(t)
	d2.createPlan(t)
	d2.hooks.errs = []error{errors.New("boom")}
	failed, err := d2.orch.ExecuteTestFailover(ctx, d2.planID, nil)
	if err == nil || failed.State != StateFailed {
		t.Fatalf("expected a failed run, got %v / %s", err, failed.State)
	}
	if _, err := d2.orch.ExecuteTestCleanup(ctx, d2.planID, failed.ID, nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("cleaning a failed run: %v, want ErrInvalid", err)
	}
}

func TestDefaultHookRunner(t *testing.T) {
	hr := NewDefaultHookRunner()
	if len(hr.Shell) == 0 {
		t.Skip("no sh or bash on this host; the hook runner cannot be tested")
	}

	out, err := hr.RunHook(context.Background(), "printf 'plan=%s step=%s' \"$VIRTOMIR_DR_PLAN_ID\" \"$VIRTOMIR_DR_STEP\"",
		[]string{"VIRTOMIR_DR_PLAN_ID=plan-1", "VIRTOMIR_DR_STEP=pre"}, 10*time.Second)
	if err != nil {
		t.Fatalf("run hook: %v", err)
	}
	if string(out) != "plan=plan-1 step=pre" {
		t.Errorf("output = %q", out)
	}

	// Output is kept within 64 KiB even when the script says much more.
	out, err = hr.RunHook(context.Background(), "i=0; while [ $i -lt 8000 ]; do printf 'zzzzzzzzzz%s\\n' $i; i=$((i+1)); done", nil, 30*time.Second)
	if err != nil {
		t.Fatalf("run hook: %v", err)
	}
	if len(out) != maxHookOutput {
		t.Errorf("output = %d bytes, want %d", len(out), maxHookOutput)
	}
}

// TestFailoverBusyAcrossRestartRunningCrash verifies that when a failover run
// is interrupted by a daemon crash while in StateRunning, a restarted orchestrator
// sees the plan as busy (crash guard), and that an operator can clean up the
// orphaned running test.
func TestFailoverBusyAcrossRestartRunningCrash(t *testing.T) {
	ctx := context.Background()
	d := newDRTest(t)
	d.createPlan(t)

	// Simulate a crash during failover: record an active running execution in store.
	crashedRun := ExecutionRun{
		ID:      newID(),
		PlanID:  d.planID,
		Mode:    ModeTestFailover,
		State:   StateRunning,
		Started: time.Now().UTC(),
	}
	if err := d.store.RecordRun(crashedRun); err != nil {
		t.Fatalf("record crashed run: %v", err)
	}

	restarted := NewOrchestrator(d.store, d.zfs, d.virt, d.hooks, discardLogger, d.state)
	// A new failover is blocked because of the crashed run in StateRunning.
	if _, err := restarted.ExecuteTestFailover(ctx, d.planID, nil); !errors.Is(err, ErrBusy) {
		t.Errorf("failover over a crashed running test: %v, want ErrBusy", err)
	}

	// The operator can clean up the orphaned crashed run.
	cleaned, err := restarted.ExecuteTestCleanup(ctx, d.planID, crashedRun.ID, nil)
	if err != nil {
		t.Fatalf("cleanup of crashed run: %v", err)
	}
	if cleaned.State != StateTestCleaned {
		t.Errorf("state = %s, want test_cleaned", cleaned.State)
	}

	// After cleanup, the plan is free for a new failover.
	if _, err := restarted.ExecuteTestFailover(ctx, d.planID, nil); err != nil {
		t.Errorf("failover after cleaning crashed run: %v", err)
	}
}

// TestTestCleanupConcurrentBusy verifies that two cleanups cannot run
// concurrently for the same plan.
func TestTestCleanupConcurrentBusy(t *testing.T) {
	ctx := context.Background()
	d := newDRTest(t)
	d.createPlan(t)

	run, err := d.orch.ExecuteTestFailover(ctx, d.planID, nil)
	if err != nil {
		t.Fatalf("failover: %v", err)
	}

	// Artificially mark cleanup active.
	cleanupRun := run
	if err := d.orch.markCleanupActive(d.planID, &cleanupRun); err != nil {
		t.Fatalf("mark cleanup active: %v", err)
	}

	// A concurrent cleanup must be rejected with ErrBusy.
	if _, err := d.orch.ExecuteTestCleanup(ctx, d.planID, run.ID, nil); !errors.Is(err, ErrBusy) {
		t.Errorf("concurrent cleanup: %v, want ErrBusy", err)
	}

	// A concurrent failover must also be rejected with ErrBusy.
	if _, err := d.orch.ExecuteTestFailover(ctx, d.planID, nil); !errors.Is(err, ErrBusy) {
		t.Errorf("failover during cleanup: %v, want ErrBusy", err)
	}

	// Once the active cleanup lock is cleared (e.g. failure rollback), cleanup succeeds.
	d.orch.forgetRun(d.planID)
	cleaned, err := d.orch.ExecuteTestCleanup(ctx, d.planID, run.ID, nil)
	if err != nil {
		t.Fatalf("cleanup after busy released: %v", err)
	}
	if cleaned.State != StateTestCleaned {
		t.Errorf("state = %s, want test_cleaned", cleaned.State)
	}
}

// TestTestCloneDatasetsRecursive verifies testCloneDatasets uses -r and properly
// lists clones under targetPool.
func TestTestCloneDatasetsRecursive(t *testing.T) {
	ctx := context.Background()
	runner := newZFSsim("")
	runID := "run-xyz"
	runner.clones = []string{
		"tank/dr/virtomir-test-" + runID + "-disk0",
		"tank/dr/virtomir-test-" + runID + "-disk1",
		"tank/dr/other-volume",
	}

	datasets, err := testCloneDatasets(ctx, runner, "tank/dr", runID)
	if err != nil {
		t.Fatalf("testCloneDatasets: %v", err)
	}
	if len(datasets) != 2 {
		t.Errorf("datasets = %v, want 2 clones matching run ID", datasets)
	}

	// Verify that the call passed -r.
	foundRecursive := false
	for _, call := range runner.calls {
		if strings.Contains(call.joined(), "zfs list") && strings.Contains(call.joined(), "-r") {
			foundRecursive = true
			break
		}
	}
	if !foundRecursive {
		t.Errorf("testCloneDatasets did not pass -r flag: calls=%v", runner.calls)
	}
}

// TestCappedBufferConcurrency verifies cappedBuffer is thread-safe under concurrent
// writes and reads.
func TestCappedBufferConcurrency(t *testing.T) {
	var buf cappedBuffer
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			_, _ = buf.Write([]byte("some data to write\n"))
		}
		close(done)
	}()

	for {
		select {
		case <-done:
			return
		default:
			_ = buf.Bytes()
		}
	}
}
