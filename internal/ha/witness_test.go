package ha

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// leaseVia makes a manager ask w for its lease, on the test clock.
func leaseVia(m *Manager, w *LeaseWitness) {
	m.lease = func(_ context.Context, _, group, holder string, ttl time.Duration) (bool, error) {
		return w.acquire(group, holder, ttl).Granted, nil
	}
}

// #22: two members that cannot see each other but both reach the witness. A TCP witness
// gives both its vote (split brain); a lease witness gives it to one of them only.
func TestLeaseWitnessGivesOneSideOfASplitQuorum(t *testing.T) {
	ma, fa, clk, sta := newTestManager(t, "a", &fakeHV{})
	mb, fb, _, stb := newTestManager(t, "b", &fakeHV{})
	mb.now, mb.started = clk.now, clk.t // one clock for both members and the witness
	w := NewLeaseWitness()
	w.now = clk.now
	for _, x := range []struct {
		m  *Manager
		fc *fakeCluster
		st *Store
	}{{ma, fa, sta}, {mb, fb, stb}} {
		x.fc.nodes = x.fc.nodes[:2]
		leaseVia(x.m, w)
		if _, err := x.st.UpdateSettings(Settings{Witness: "lease://10.0.0.9:8421"}); err != nil {
			t.Fatal(err)
		}
	}
	tick := func() {
		ma.Tick(context.Background())
		mb.Tick(context.Background())
	}
	tick()
	// The split: each loses sight of the other, both still reach the witness.
	fa.down["b"], fb.down["a"] = true, true
	clk.add(time.Minute)
	tick()
	qa, qb := ma.Status().Quorum, mb.Status().Quorum
	if qa.Quorate == qb.Quorate {
		t.Fatalf("split brain or no quorum at all: a %+v, b %+v", qa, qb)
	}
	holder, other := ma, mb
	if !qa.Quorate {
		holder, other = mb, ma
	}
	// The holder keeps it while it renews; the other is still refused.
	for range 5 {
		clk.add(5 * time.Second)
		tick()
		if !holder.Status().Quorum.Quorate || other.Status().Quorum.Quorate {
			t.Fatal("the lease changed sides while its holder renewed it")
		}
	}
	// The holder knows the other side cannot have the witness's vote.
	if holder.othersMayHaveQuorum(holder.store.Config(), holder.now()) {
		t.Error("the lease holder thinks the other side may have quorum")
	}
	// The holder dies: once its lease has run out the survivor gets the vote.
	clk.add(MaxLeaseTTL)
	other.Tick(context.Background())
	if !other.Status().Quorum.Quorate {
		t.Errorf("the survivor never got the witness's vote: %+v", other.Status().Quorum)
	}
}

// The same split with a TCP witness: both sides count it. This is why the lease witness
// exists; the test pins the old behaviour down so the HA page's warning stays true.
func TestTCPWitnessCountsForBothSidesOfASplit(t *testing.T) {
	ma, fa, clk, sta := newTestManager(t, "a", &fakeHV{})
	mb, fb, _, stb := newTestManager(t, "b", &fakeHV{})
	mb.now, mb.started = clk.now, clk.t
	for _, x := range []struct {
		fc *fakeCluster
		st *Store
	}{{fa, sta}, {fb, stb}} {
		x.fc.nodes = x.fc.nodes[:2]
		_, _ = x.st.UpdateSettings(Settings{Witness: "10.0.0.9:22"})
	}
	fa.down["b"], fb.down["a"] = true, true
	clk.add(time.Minute)
	ma.Tick(context.Background())
	mb.Tick(context.Background())
	if !ma.Status().Quorum.Quorate || !mb.Status().Quorum.Quorate {
		t.Fatal("expected both sides to count a TCP witness")
	}
}

func TestLeaseWitnessServer(t *testing.T) {
	w := NewLeaseWitness()
	clk := &clock{t: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)}
	w.now = clk.now
	srv := httptest.NewServer(w)
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	ctx := context.Background()
	if ok, err := acquireLease(ctx, addr, "prod", "a", 10*time.Second); !ok || err != nil {
		t.Fatalf("first request: %v %v", ok, err)
	}
	if ok, _ := acquireLease(ctx, addr, "prod", "b", 10*time.Second); ok {
		t.Fatal("a second holder got the lease")
	}
	if ok, _ := acquireLease(ctx, addr, "lab", "b", 10*time.Second); !ok {
		t.Error("another cluster's group was refused")
	}
	if ok, _ := acquireLease(ctx, addr, "prod", "a", 10*time.Second); !ok {
		t.Error("the holder could not renew")
	}
	clk.add(11 * time.Second)
	if ok, _ := acquireLease(ctx, addr, "prod", "b", 10*time.Second); !ok {
		t.Error("the lease did not run out")
	}
	// A member cannot hold it for longer than MaxLeaseTTL.
	clk.add(time.Hour)
	_, _ = acquireLease(ctx, addr, "prod", "a", 24*time.Hour)
	clk.add(MaxLeaseTTL + time.Second)
	if ok, _ := acquireLease(ctx, addr, "prod", "b", time.Second); !ok {
		t.Error("an overlong lease was granted")
	}
	for _, bad := range []string{`{}`, `{"group":"x","holder":"a","ttlMs":0}`, `nope`} {
		resp, err := http.Post(srv.URL+"/v1/lease", "application/json", strings.NewReader(bad))
		if err != nil || resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %v %v", bad, resp.Status, err)
		}
		resp.Body.Close()
	}
	resp, _ := http.Get(srv.URL + "/v1/lease")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET: %d", resp.StatusCode)
	}
	resp.Body.Close()
	var ans leaseAnswer
	resp, _ = http.Post(srv.URL+"/v1/lease", "application/json", strings.NewReader(`{"group":"prod","holder":"c","ttlMs":1000}`))
	_ = json.NewDecoder(resp.Body).Decode(&ans)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict || ans.Granted || ans.Holder != "b" {
		t.Errorf("a refusal names the holder: %d %+v", resp.StatusCode, ans)
	}
}

func TestWitnessSettings(t *testing.T) {
	for w, ok := range map[string]bool{
		"10.0.0.9:22":                  true,
		"lease://10.0.0.9:8421":        true,
		"lease://witness.lan:8421/lab": true,
		"[fd00::9]:22":                 true,
		"10.0.0.9":                     false,
		"lease://10.0.0.9":             false,
		"lease://:8421":                false,
		"lease://w:8421/a b":           false,
	} {
		if err := (Settings{Witness: w}).Validate(); (err == nil) != ok {
			t.Errorf("%q: %v", w, err)
		}
	}
	for w, want := range map[string][3]string{
		"10.0.0.9:22":           {"tcp", "10.0.0.9:22", ""},
		"lease://w:8421":        {"lease", "w:8421", "default"},
		"lease://w:8421/prod-1": {"lease", "w:8421", "prod-1"},
	} {
		if k, a, g := witnessTarget(w); [3]string{k, a, g} != want {
			t.Errorf("%q: %s %s %s", w, k, a, g)
		}
	}
	if leaseTTL(time.Second) != 10*time.Second || leaseTTL(10*time.Second) != 30*time.Second || leaseTTL(time.Hour) != MaxLeaseTTL {
		t.Error("lease TTL")
	}
}
