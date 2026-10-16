package ha

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/onurbaydas/virtomir/internal/virt"
)

func openStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "ha.json"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestStoreSettingsAndVersions(t *testing.T) {
	s := openStore(t)
	if c := s.Config(); c.HeartbeatIntervalSec != defaultHeartbeat || c.FailoverTimeoutSec != defaultTimeout || c.Version != 0 {
		t.Fatalf("defaults = %+v", c)
	}
	_, err := s.UpdateSettings(Settings{AutoFailover: true, FailoverTimeoutSec: 30, Witness: "10.0.0.9:22",
		FenceAgents: map[string]FenceAgent{"n2": {Driver: "ipmi", Endpoint: "10.0.0.102", Username: "admin", Password: "secret"}}})
	if err != nil {
		t.Fatal(err)
	}
	// The UI sends the redacted password back: the stored one stays.
	pub := s.Public()
	if pub.FenceAgents["n2"].Password != Redacted || pub.Version != 1 {
		t.Fatalf("public = %+v", pub)
	}
	if _, err := s.UpdateSettings(Settings{AutoFailover: true, FailoverTimeoutSec: 30, FenceAgents: pub.FenceAgents}); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.Agent("n2"); a.Password != "secret" || a.NodeID != "n2" {
		t.Errorf("agent = %+v", a)
	}
	for _, bad := range []Settings{
		{FailoverTimeoutSec: 5},
		{Witness: "10.0.0.9"},
		{FenceAgents: map[string]FenceAgent{"x": {Driver: "ipmi"}}},
		{FenceAgents: map[string]FenceAgent{"x": {Driver: "ssh", Endpoint: "h"}}},
	} {
		if _, err := s.UpdateSettings(bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	if _, err := s.SetPolicy(VMPolicy{UUID: "AA-BB", Name: "db", Enabled: true, Priority: "urgent"}); err == nil {
		t.Error("unknown priority accepted")
	}
	c, err := s.SetPolicy(VMPolicy{UUID: "AA-BB", Name: "db", Enabled: true})
	if err != nil || c.VMPolicies["aa-bb"].Priority != "normal" || c.VMPolicies["aa-bb"].MaxRetries != 2 {
		t.Fatalf("policy = %+v, %v", c.VMPolicies, err)
	}
	if _, ok := s.Policy("AA-BB"); !ok {
		t.Error("policy not found case-insensitively")
	}
	// A peer's older configuration is ignored, a newer one adopted.
	older := s.Config()
	older.Version, older.AutoFailover = 1, false
	if ok, _ := s.Adopt(older); ok {
		t.Error("older config adopted")
	}
	newer := s.Config()
	newer.Version += 5
	newer.Witness = "10.0.0.10:22"
	if ok, err := s.Adopt(newer); !ok || err != nil || s.Config().Witness != "10.0.0.10:22" {
		t.Errorf("newer config not adopted: %v", err)
	}
	// It survives a restart.
	s2, err := Open(s.path)
	if err != nil || s2.Config().Version != newer.Version {
		t.Fatalf("reopen: %v %+v", err, s2.Config())
	}
}

// ---- Fencing -------------------------------------------------------------------------------

func TestFenceIPMI(t *testing.T) {
	var calls []string
	var env []string
	power := "on"
	f := &Fencer{Poll: time.Millisecond, Run: func(_ context.Context, e []string, name string, args ...string) ([]byte, error) {
		env = e
		cmd := strings.Join(args[len(args)-3:], " ")
		calls = append(calls, cmd)
		switch cmd {
		case "chassis power off":
			power = "off"
			return []byte("Chassis Power Control: Down/Off\n"), nil
		case "chassis power status":
			return []byte("Chassis Power is " + power + "\n"), nil
		}
		return nil, errors.New("unexpected " + cmd)
	}}
	res, err := f.Fence(context.Background(), FenceAgent{NodeID: "n2", Driver: "ipmi", Endpoint: "10.0.0.102:623", Username: "admin", Password: "pw"}, FenceOff)
	if err != nil || res.Power != "off" {
		t.Fatalf("fence = %+v, %v", res, err)
	}
	if len(env) != 1 || env[0] != "IPMI_PASSWORD=pw" {
		t.Errorf("password not passed through the environment: %v", env)
	}
	if calls[0] != "chassis power off" || calls[1] != "chassis power status" {
		t.Errorf("calls = %v", calls)
	}
}

func TestFenceIPMIStaysOnIsAFailure(t *testing.T) {
	f := &Fencer{Poll: time.Millisecond, Run: func(_ context.Context, _ []string, _ string, args ...string) ([]byte, error) {
		return []byte("Chassis Power is on\n"), nil
	}}
	// waitOff gives up after a minute; make the context end sooner.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := f.Fence(ctx, FenceAgent{Driver: "ipmi", Endpoint: "h"}, FenceOff); err == nil {
		t.Fatal("a BMC that keeps the power on counted as fenced")
	}
}

func TestFenceRedfish(t *testing.T) {
	var mu sync.Mutex
	state := "On"
	var reset string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "root" || p != "calvin" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/redfish/v1/Systems":
			io.WriteString(w, `{"Members":[{"@odata.id":"/redfish/v1/Systems/System.Embedded.1"}]}`)
		case r.URL.Path == "/redfish/v1/Systems/System.Embedded.1":
			io.WriteString(w, `{"PowerState":"`+state+`"}`)
		case strings.HasSuffix(r.URL.Path, "/Actions/ComputerSystem.Reset") && r.Method == http.MethodPost:
			var b struct{ ResetType string }
			_ = json.NewDecoder(r.Body).Decode(&b)
			reset = b.ResetType
			state = "Off"
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	f := NewFencer()
	f.Poll = time.Millisecond
	a := FenceAgent{Driver: "redfish", Endpoint: srv.URL, Username: "root", Password: "calvin", InsecureTLS: true}
	if res, err := f.Fence(context.Background(), a, FenceStatus); err != nil || res.Power != "on" {
		t.Fatalf("status = %+v, %v", res, err)
	}
	res, err := f.Fence(context.Background(), a, FenceOff)
	if err != nil || res.Power != "off" || reset != "ForceOff" {
		t.Fatalf("off = %+v, %v, reset %q", res, err, reset)
	}
	a.InsecureTLS = false // self-signed certificate: refused unless allowed
	if _, err := f.Fence(context.Background(), a, FenceStatus); err == nil {
		t.Error("self-signed BMC accepted without InsecureTLS")
	}
}

func TestFenceManualNeedsConfirmation(t *testing.T) {
	if _, err := NewFencer().Fence(context.Background(), FenceAgent{Driver: "manual"}, FenceOff); !errors.Is(err, ErrManualFence) {
		t.Errorf("err = %v", err)
	}
}

// ---- Manager -------------------------------------------------------------------------------

// fakeCluster is three members a, b, c; down members do not answer heartbeats.
type fakeCluster struct {
	mu        sync.Mutex
	self      string
	nodes     []Node
	down      map[string]bool
	reports   map[string]Report
	recovered []Replica
	recoverTo []string
	pushed    map[string][]Replica
}

func (f *fakeCluster) SelfID() string  { return f.self }
func (f *fakeCluster) Members() []Node { return f.nodes }
func (f *fakeCluster) Heartbeat(_ context.Context, n Node, _ Report) (Report, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down[n.ID] {
		return Report{}, errors.New("no route to host")
	}
	r := f.reports[n.ID]
	r.NodeID, r.Name = n.ID, n.Name
	if r.MemTotalKiB == 0 {
		r.MemTotalKiB, r.MemFreeKiB = 64<<20, 32<<20
	}
	return r, nil
}
func (f *fakeCluster) PushReplicas(_ context.Context, n Node, reps []Replica) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pushed[n.ID] = reps
	return nil
}
func (f *fakeCluster) FetchConfig(context.Context, Node) (Config, error) {
	return Config{}, errors.New("n/a")
}
func (f *fakeCluster) Recover(_ context.Context, n Node, r Replica) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recovered = append(f.recovered, r)
	f.recoverTo = append(f.recoverTo, n.ID)
	return nil
}

type fakeHV struct {
	mu      sync.Mutex
	doms    []virt.Domain
	actions []string
}

func (h *fakeHV) ListDomains() ([]virt.Domain, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]virt.Domain{}, h.doms...), nil
}
func (h *fakeHV) DomainXML(uuid string, _ bool) (string, error) {
	return "<domain><uuid>" + uuid + "</uuid></domain>", nil
}
func (h *fakeHV) DomainAction(uuid string, a virt.Action) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.actions = append(h.actions, string(a)+" "+uuid)
	for i := range h.doms {
		if h.doms[i].UUID == uuid {
			switch a {
			case virt.ActionForceOff:
				h.doms[i].State = virt.StateShutoff
			case virt.ActionStart:
				h.doms[i].State = virt.StateRunning
			}
		}
	}
	return nil
}
func (h *fakeHV) SetAutostart(uuid string, on bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range h.doms {
		if h.doms[i].UUID == uuid {
			h.doms[i].Autostart = on
		}
	}
	return nil
}
func (h *fakeHV) AdoptDomain(doc string) (string, error) { return "", errors.New("not here") }
func (h *fakeHV) HostStats() (virt.HostStats, error) {
	return virt.HostStats{MemTotalKiB: 64 << 20, MemUsedKiB: 16 << 20}, nil
}

type fakeWatchdog struct{ armed, petted int }

func (w *fakeWatchdog) Arm(int) error { w.armed++; return nil }
func (w *fakeWatchdog) Pet() error    { w.petted++; return nil }
func (w *fakeWatchdog) Disarm() error { w.armed = 0; return nil }
func (w *fakeWatchdog) Armed() bool   { return w.armed > 0 }

type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }

func newTestManager(t *testing.T, self string, hv *fakeHV) (*Manager, *fakeCluster, *clock, *Store) {
	t.Helper()
	st := openStore(t)
	fc := &fakeCluster{self: self, nodes: []Node{{"a", "alpha"}, {"b", "bravo"}, {"c", "charlie"}},
		down: map[string]bool{}, reports: map[string]Report{}, pushed: map[string][]Replica{}}
	m := NewManager(st, fc, hv, filepath.Join(t.TempDir(), "ha"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	clk := &clock{t: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)}
	m.now, m.started = clk.now, clk.t
	m.watchdog = &fakeWatchdog{}
	m.dial = func(context.Context, string) error { return nil }
	return m, fc, clk, st
}

func (m *Manager) waitRecovery(t *testing.T, node, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		rc := m.recoveries[node]
		state := ""
		if rc != nil {
			state = rc.state
		}
		m.mu.Unlock()
		if state == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("recovery of %s never reached %q; events: %+v", node, want, m.Status().Events)
}

func TestQuorumAndCoordinator(t *testing.T) {
	m, fc, _, _ := newTestManager(t, "b", &fakeHV{})
	m.Tick(context.Background())
	q := m.Status().Quorum
	if !q.Quorate || q.Votes != 3 || q.TotalVotes != 3 || q.Required != 2 || q.Coordinator != "a" {
		t.Fatalf("quorum = %+v", q)
	}
	fc.down["a"] = true
	m.store.UpdateSettings(Settings{FailoverTimeoutSec: 10})
	m.seen["a"] = m.now().Add(-time.Minute)
	m.Tick(context.Background())
	if q := m.Status().Quorum; !q.Quorate || q.Votes != 2 || q.Coordinator != "b" {
		t.Fatalf("with a down: %+v", q)
	}
}

func TestTwoMembersNeedTheWitness(t *testing.T) {
	m, fc, clk, st := newTestManager(t, "a", &fakeHV{})
	fc.nodes = fc.nodes[:2]
	m.Tick(context.Background())
	fc.down["b"] = true
	clk.add(time.Minute)
	m.Tick(context.Background())
	if m.Status().Quorum.Quorate {
		t.Fatal("one of two members has quorum without a witness")
	}
	st.UpdateSettings(Settings{Witness: "10.0.0.9:22"})
	m.Tick(context.Background())
	if q := m.Status().Quorum; !q.Quorate || q.Votes != 2 || q.TotalVotes != 3 {
		t.Fatalf("with witness: %+v", q)
	}
}

func TestReplicationTurnsOffAutostart(t *testing.T) {
	hv := &fakeHV{doms: []virt.Domain{
		{UUID: "u1", Name: "db", State: virt.StateRunning, Persistent: true, Autostart: true, VCPUs: 2, MaxMemoryKiB: 4 << 20},
		{UUID: "u2", Name: "web", State: virt.StateRunning, Persistent: true},
	}}
	m, fc, _, st := newTestManager(t, "a", hv)
	st.SetPolicy(VMPolicy{UUID: "u1", Name: "db", Enabled: true})
	m.Tick(context.Background())
	time.Sleep(20 * time.Millisecond) // pushes run in the background
	fc.mu.Lock()
	reps := fc.pushed["b"]
	fc.mu.Unlock()
	if len(reps) != 1 || reps[0].UUID != "u1" || reps[0].Owner != "a" || !reps[0].Running || reps[0].XML == "" {
		t.Fatalf("pushed = %+v", reps)
	}
	if hv.doms[0].Autostart {
		t.Error("autostart of a protected machine left on")
	}
}

func TestFailoverWithBMC(t *testing.T) {
	m, fc, clk, st := newTestManager(t, "a", &fakeHV{})
	st.UpdateSettings(Settings{AutoFailover: true, FailoverTimeoutSec: 10,
		FenceAgents: map[string]FenceAgent{"c": {Driver: "ipmi", Endpoint: "bmc-c"}}})
	st.SetPolicy(VMPolicy{UUID: "v1", Name: "db", Enabled: true, Priority: "critical"})
	st.SetPolicy(VMPolicy{UUID: "v2", Name: "cache", Enabled: true, Priority: "low", TargetNode: "a"})
	st.SetPolicy(VMPolicy{UUID: "v3", Name: "stopped", Enabled: true})
	m.Accept([]Replica{
		{UUID: "v1", Name: "db", Owner: "c", OwnerName: "charlie", Running: true, XML: "<domain/>", MemoryKiB: 4 << 20, Epoch: 3},
		{UUID: "v2", Name: "cache", Owner: "c", OwnerName: "charlie", Running: true, XML: "<domain/>", MemoryKiB: 1 << 20},
		{UUID: "v3", Name: "stopped", Owner: "c", Running: false, XML: "<domain/>"},
	})
	var fenced []string
	m.fencer = &Fencer{Poll: time.Millisecond, Run: func(_ context.Context, _ []string, _ string, args ...string) ([]byte, error) {
		cmd := strings.Join(args[len(args)-3:], " ")
		fenced = append(fenced, cmd)
		return []byte("Chassis Power is off"), nil
	}}
	m.Tick(context.Background()) // everyone answers
	fc.down["c"] = true
	clk.add(5 * time.Second)
	m.Tick(context.Background())
	if len(fenced) != 0 {
		t.Fatal("fenced before the timeout")
	}
	clk.add(10 * time.Second)
	m.Tick(context.Background())
	m.waitRecovery(t, "c", "done")
	if len(fenced) == 0 || fenced[0] != "chassis power off" {
		t.Fatalf("fence calls = %v", fenced)
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if len(fc.recovered) != 1 {
		// v2 prefers a (this member) and is recovered locally, which fails in the fake HV.
		t.Logf("recovered remotely: %+v", fc.recovered)
	}
	if fc.recovered[0].UUID != "v1" || fc.recovered[0].Epoch != 4 || fc.recovered[0].Owner == "c" {
		t.Errorf("first recovered = %+v (critical first, epoch+1, new owner)", fc.recovered[0])
	}
	for _, r := range fc.recovered {
		if r.UUID == "v3" {
			t.Error("a machine that was stopped got started")
		}
	}
}

func TestFailoverWithoutAgentWaitsForConfirmation(t *testing.T) {
	m, fc, clk, st := newTestManager(t, "a", &fakeHV{})
	st.UpdateSettings(Settings{AutoFailover: true, FailoverTimeoutSec: 10})
	st.SetPolicy(VMPolicy{UUID: "v1", Name: "db", Enabled: true, TargetNode: "b"})
	m.Accept([]Replica{{UUID: "v1", Name: "db", Owner: "c", Running: true, XML: "<domain/>"}})
	m.Tick(context.Background())
	if err := m.ConfirmOff("c", "onur"); err == nil {
		t.Fatal("confirmation accepted while the host answers")
	}
	fc.down["c"] = true
	clk.add(30 * time.Second)
	m.Tick(context.Background())
	m.waitRecovery(t, "c", "waiting-confirmation")
	if len(fc.recovered) != 0 {
		t.Fatal("restarted without fencing")
	}
	if err := m.ConfirmOff("c", "onur"); err != nil {
		t.Fatal(err)
	}
	m.Tick(context.Background())
	m.waitRecovery(t, "c", "done")
	if len(fc.recoverTo) != 1 || fc.recoverTo[0] != "b" {
		t.Errorf("recovered to %v, want the preferred member b", fc.recoverTo)
	}
}

func TestFailoverAfterWatchdog(t *testing.T) {
	m, fc, clk, st := newTestManager(t, "a", &fakeHV{})
	st.UpdateSettings(Settings{AutoFailover: true, FailoverTimeoutSec: 10})
	st.SetPolicy(VMPolicy{UUID: "v1", Name: "db", Enabled: true})
	m.Accept([]Replica{{UUID: "v1", Name: "db", Owner: "c", Running: true, XML: "<domain/>"}})
	fc.reports["c"] = Report{WatchdogArmed: true, WatchdogTimeoutSec: 60}
	m.Tick(context.Background())
	fc.down["c"] = true
	clk.add(20 * time.Second)
	m.Tick(context.Background())
	m.waitRecovery(t, "c", "waiting-watchdog")
	clk.add(80 * time.Second) // timeout 10 + watchdog 60 + margin 15 since last seen
	m.Tick(context.Background())
	m.waitRecovery(t, "c", "done")
}

func TestNoFailoverWhenAutoOffOrNothingProtected(t *testing.T) {
	m, fc, clk, st := newTestManager(t, "a", &fakeHV{})
	st.UpdateSettings(Settings{FailoverTimeoutSec: 10, FenceAgents: map[string]FenceAgent{"c": {Driver: "ipmi", Endpoint: "x"}}})
	called := false
	m.fencer = &Fencer{Run: func(context.Context, []string, string, ...string) ([]byte, error) { called = true; return nil, nil }}
	m.Tick(context.Background())
	fc.down["c"] = true
	clk.add(time.Minute)
	m.Tick(context.Background()) // nothing protected on c: no fencing at all
	if called {
		t.Fatal("a host without protected machines was fenced")
	}
	st.SetPolicy(VMPolicy{UUID: "v1", Name: "db", Enabled: true})
	m.Accept([]Replica{{UUID: "v1", Name: "db", Owner: "c", Running: true, XML: "<domain/>"}})
	m.Tick(context.Background())
	if called || len(fc.recovered) > 0 {
		t.Fatal("acted with automatic failover off")
	}
	m.waitRecovery(t, "c", "waiting-auto")
}

func TestSelfFenceWithoutQuorum(t *testing.T) {
	hv := &fakeHV{doms: []virt.Domain{{UUID: "v1", Name: "db", State: virt.StateRunning, Persistent: true}}}
	m, fc, clk, st := newTestManager(t, "c", hv)
	st.UpdateSettings(Settings{FailoverTimeoutSec: 10, Watchdog: true})
	st.SetPolicy(VMPolicy{UUID: "v1", Name: "db", Enabled: true})
	m.Tick(context.Background())
	wd := m.watchdog.(*fakeWatchdog)
	if wd.armed == 0 || wd.petted == 0 {
		t.Fatalf("watchdog not armed/petted: %+v", wd)
	}
	fc.down["a"], fc.down["b"] = true, true
	clk.add(5 * time.Second)
	m.Tick(context.Background())
	if hv.doms[0].State != virt.StateRunning {
		t.Fatal("stopped before the timeout")
	}
	clk.add(15 * time.Second)
	m.Tick(context.Background())
	if hv.doms[0].State != virt.StateShutoff {
		t.Fatalf("protected machine still running without quorum; actions %v", hv.actions)
	}
}

func TestStaleCopyIsStopped(t *testing.T) {
	hv := &fakeHV{doms: []virt.Domain{{UUID: "v1", Name: "db", State: virt.StateRunning, Persistent: true}}}
	m, _, _, st := newTestManager(t, "c", hv)
	st.SetPolicy(VMPolicy{UUID: "v1", Name: "db", Enabled: true})
	// While c was away, a restarted db on b (epoch 1).
	m.Accept([]Replica{{UUID: "v1", Name: "db", Owner: "b", OwnerName: "bravo", Running: true, Epoch: 1, XML: "<domain/>"}})
	m.Tick(context.Background())
	if hv.doms[0].State != virt.StateShutoff {
		t.Fatal("stale copy kept running next to the recovered one")
	}
}

func TestResumeAfterBoot(t *testing.T) {
	hv := &fakeHV{doms: []virt.Domain{{UUID: "v1", Name: "db", State: virt.StateShutoff, Persistent: true}}}
	m, _, _, st := newTestManager(t, "a", hv)
	st.SetPolicy(VMPolicy{UUID: "v1", Name: "db", Enabled: true})
	m.Accept([]Replica{{UUID: "v1", Name: "db", Owner: "a", Running: true, XML: "<domain/>"}})
	m.Tick(context.Background())
	if hv.doms[0].State != virt.StateRunning {
		t.Fatalf("protected machine not started after boot: %v", hv.actions)
	}
}

func TestSimulate(t *testing.T) {
	m, _, _, st := newTestManager(t, "a", &fakeHV{})
	st.UpdateSettings(Settings{AutoFailover: true, FenceAgents: map[string]FenceAgent{"c": {Driver: "redfish", Endpoint: "x"}}})
	st.SetPolicy(VMPolicy{UUID: "v1", Name: "db", Enabled: true, Priority: "high"})
	st.SetPolicy(VMPolicy{UUID: "v2", Name: "huge", Enabled: true})
	m.Accept([]Replica{
		{UUID: "v1", Name: "db", Owner: "c", Running: true, MemoryKiB: 1 << 20},
		{UUID: "v2", Name: "huge", Owner: "c", Running: true, MemoryKiB: 1 << 40},
	})
	m.Tick(context.Background())
	sim, err := m.Simulate("c")
	if err != nil {
		t.Fatal(err)
	}
	if !sim.Quorate || !strings.Contains(sim.Fencing, "redfish") || len(sim.Steps) != 2 {
		t.Fatalf("sim = %+v", sim)
	}
	if sim.Steps[0].Name != "db" || sim.Steps[0].Target == "" || sim.Steps[1].Problem == "" || sim.CanRecover {
		t.Errorf("steps = %+v", sim.Steps)
	}
}

func TestMachineDeletedOutsideIsNotRecovered(t *testing.T) {
	hv := &fakeHV{} // v1 was deleted with virsh: libvirt no longer has it
	m, fc, _, st := newTestManager(t, "a", hv)
	st.SetPolicy(VMPolicy{UUID: "v1", Name: "db", Enabled: true})
	m.Accept([]Replica{{UUID: "v1", Name: "db", Owner: "a", Running: true, XML: "<domain/>"}})
	m.Tick(context.Background())
	time.Sleep(20 * time.Millisecond)
	fc.mu.Lock()
	reps := fc.pushed["b"]
	fc.mu.Unlock()
	if len(reps) != 1 || reps[0].Running {
		t.Fatalf("pushed = %+v, want v1 marked not running", reps)
	}
	if m.Status().Machines[0].Running {
		t.Error("local copy still says running")
	}
}
