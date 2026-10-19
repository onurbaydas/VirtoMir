package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/onurbaydas/virtomir/internal/auth"
	"github.com/onurbaydas/virtomir/internal/lcm"
	"github.com/onurbaydas/virtomir/internal/virt"
)

func lcmNodes(t *testing.T) (*testNode, *testNode) {
	t.Helper()
	a := newTestNode(t, "alpha", []virt.Domain{{UUID: "a1", Name: "web", State: virt.StateRunning, MaxMemoryKiB: 2 << 20}})
	b := newTestNode(t, "beta", nil)
	for _, n := range []*testNode{a, b} {
		st, err := lcm.Open("")
		if err != nil {
			t.Fatal(err)
		}
		n.api.lcm = st
		n.api.version = "0.9.0"
		n.lv.backlog = [][]byte{[]byte(`{"host":{"memTotalKiB":16777216,"memUsedKiB":4194304}}`)}
	}
	joinNodes(t, a, b)
	return a, b
}

func TestLCMDriverOverTheAPI(t *testing.T) {
	a, b := lcmNodes(t)
	d := &lcmDriver{s: a.api, id: auth.Identity{Name: "onur", Role: auth.Admin}}
	ctx := t.Context()
	members, _ := d.Members(ctx)
	if len(members) != 2 || members[0].Version == "" || members[1].Version == "" {
		t.Fatalf("members %+v", members)
	}
	for _, n := range []*testNode{a, b} { // this host in-process, the other over the peer protocol
		id := n.store.SelfID()
		free, err := d.FreeMemory(ctx, id)
		if err != nil || free != 12<<20 {
			t.Errorf("%s free %d %v", id, free, err)
		}
		if jobs, err := d.RunningJobs(ctx, id); err != nil || jobs != 0 {
			t.Errorf("%s jobs %d %v", id, jobs, err)
		}
	}
	vms, err := d.RunningVMs(ctx, a.store.SelfID())
	if err != nil || len(vms) != 1 || !vms[0].Running || vms[0].MemKiB != 2<<20 {
		t.Errorf("vms %+v %v", vms, err)
	}
	if _, _, err := d.JobState(ctx, b.store.SelfID(), "nope"); err == nil {
		t.Error("an unknown job answered")
	}
	if v, err := d.Hello(ctx, b.store.SelfID()); err != nil || v != "0.9.0" {
		t.Errorf("hello %q %v", v, err)
	}
}

func TestLCMStartAndControl(t *testing.T) {
	a, b := lcmNodes(t)
	admin := a.signIn(t, "onur")

	_, body := admin.req("POST", "/api/v1/cluster/lcm/precheck", `{}`)
	var pre struct{ Checks []lcm.HostCheck }
	if err := json.Unmarshal([]byte(body), &pre); err != nil || len(pre.Checks) != 2 {
		t.Fatalf("precheck: %s", body)
	}

	if resp, body := admin.req("POST", "/api/v1/cluster/lcm/start", `{"targetVersion":"2.0.0"}`); resp.StatusCode != http.StatusConflict ||
		!strings.Contains(body, "major") {
		t.Errorf("start across majors: %d %s", resp.StatusCode, body)
	}

	if resp, _ := a.signIn(t, "op").req("POST", "/api/v1/cluster/lcm/start", `{}`); resp.StatusCode != http.StatusForbidden {
		t.Errorf("operator started a rolling upgrade: %d", resp.StatusCode)
	}

	// A plan starts and runs on its own; this one cannot install anything (no updater
	// here), so it fails after the pre-check and the evacuation, and says why.
	resp, body := admin.req("POST", "/api/v1/cluster/lcm/start", `{}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("start: %d %s", resp.StatusCode, body)
	}
	var plan lcm.Plan
	_ = json.Unmarshal([]byte(body), &plan)
	if len(plan.HostOrder) != 2 || plan.HostOrder[1] != a.store.SelfID() || plan.StartedBy != "onur" {
		t.Errorf("plan %+v (this host last)", plan)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		p, _ := a.api.lcm.Plan(plan.ID)
		if p.State.Finished() {
			plan = p
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("plan still %s: %+v", p.State, p.Steps)
		}
		time.Sleep(20 * time.Millisecond)
	}
	last := plan.Steps[len(plan.Steps)-1]
	if plan.State != lcm.PlanFailed || last.Kind != lcm.StepUpdateVirtomir || !strings.Contains(last.Message, "updates are not available") {
		t.Errorf("plan %s, last step %+v", plan.State, last)
	}
	if resp, _ := admin.req("POST", "/api/v1/cluster/lcm/plans/"+plan.ID+"/pause", ""); resp.StatusCode != http.StatusConflict {
		t.Errorf("paused a finished plan: %d", resp.StatusCode)
	}
	if _, body := admin.req("GET", "/api/v1/cluster/lcm/plans/"+plan.ID, ""); !strings.Contains(body, `"state":"failed"`) {
		t.Errorf("plan: %s", body)
	}
	_ = b
}

func TestLCMStartNeedsEveryMember(t *testing.T) {
	a, b := lcmNodes(t)
	b.srv.Close()
	if resp, body := a.signIn(t, "onur").req("POST", "/api/v1/cluster/lcm/start", `{}`); resp.StatusCode != http.StatusConflict ||
		!strings.Contains(body, "does not answer") {
		t.Errorf("start with a member down: %d %s", resp.StatusCode, body)
	}
}

func TestClusterMixedVersions(t *testing.T) {
	a, b := lcmNodes(t)
	b.api.version = "0.9.1"
	_, body := a.signIn(t, "onur").req("GET", "/api/v1/cluster", "")
	if !strings.Contains(body, `"mixedVersions":true`) || !strings.Contains(body, `"minVersion":"0.9.0"`) {
		t.Errorf("cluster: %s", body)
	}
}

type fakeUpgrader struct{ reboot chan bool }

func (f fakeUpgrader) Upgrade(_ context.Context, reboot bool) (string, error) {
	f.reboot <- reboot
	return "virtomir-osupgrade-1", nil
}

func TestSystemUpgrade(t *testing.T) {
	n := newTestNode(t, "alpha", nil)
	up := fakeUpgrader{reboot: make(chan bool, 1)}
	n.api.upgrader = up
	resp, body := n.signIn(t, "onur").req("POST", "/api/v1/system/upgrade", `{"reboot":true}`)
	if resp.StatusCode != http.StatusAccepted || !strings.Contains(body, "and reboot") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if !<-up.reboot {
		t.Error("reboot not passed on")
	}
	if resp, _ := n.signIn(t, "op").req("POST", "/api/v1/system/upgrade", `{}`); resp.StatusCode != http.StatusForbidden {
		t.Errorf("operator upgraded the OS: %d", resp.StatusCode)
	}
}
