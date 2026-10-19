package api

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/onurbaydas/virtomir/internal/activity"
	"github.com/onurbaydas/virtomir/internal/auth"
	"github.com/onurbaydas/virtomir/internal/cluster"
	"github.com/onurbaydas/virtomir/internal/drs"
	"github.com/onurbaydas/virtomir/internal/placement"
	"github.com/onurbaydas/virtomir/internal/project"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// testNode is a complete virtomird API on its own TLS port with its own certificate.
type testNode struct {
	url       string
	store     *cluster.Store
	backend   *fakeBackend
	acts      *activity.Log
	lv        *fakeLive
	api       *Server
	projects  *project.Store
	placement *placement.Store
	srv       *httptest.Server
}

func (n *testNode) live() *fakeLive { return n.lv }

func newTestNode(t *testing.T, name string, domains []virt.Domain) *testNode {
	t.Helper()
	dir := t.TempDir()
	cert, err := auth.SelfSignedCert(filepath.Join(dir, "tls"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(nil)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	fp := cluster.Fingerprint(cert.Certificate[0])
	store, err := cluster.Open(filepath.Join(dir, "cluster.json"), name, fp)
	if err != nil {
		t.Fatal(err)
	}
	pj, err := project.Open(filepath.Join(dir, "projects.json"))
	if err != nil {
		t.Fatal(err)
	}
	pl, err := placement.Open(filepath.Join(dir, "placement.json"))
	if err != nil {
		t.Fatal(err)
	}
	m := auth.New(stubAuthn{}, func(u string) auth.Role {
		return map[string]auth.Role{"onur": auth.Admin, "op": auth.Operator, "view": auth.Viewer}[u]
	})
	acts, _ := activity.Open("")
	b := &fakeBackend{domains: domains}
	lv := &fakeLive{}
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	s := NewServer(Options{
		Backend: b, Live: lv, Auth: m, Activity: acts, Version: "v-" + name,
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Cluster: store, Fingerprint: fp, HostName: name, ListenPort: port,
		Projects: pj, Placement: pl,
	})
	srv.Config.Handler = s.Handler()
	return &testNode{url: srv.URL, store: store, backend: b, acts: acts, lv: lv, api: s, projects: pj, placement: pl, srv: srv}
}

// browser is a user's HTTPS client signed in on one node.
type browser struct {
	t      *testing.T
	base   string
	cookie *http.Cookie
	c      *http.Client
}

func (n *testNode) signIn(t *testing.T, user string) *browser {
	t.Helper()
	b := &browser{t: t, base: n.url, c: &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}}
	resp, _ := b.req("POST", "/api/v1/auth/login", `{"user":"`+user+`","password":"pw"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sign in %s: %d", user, resp.StatusCode)
	}
	b.cookie = resp.Cookies()[0]
	return b
}

func (b *browser) req(method, path, body string, hdr ...string) (*http.Response, string) {
	b.t.Helper()
	req, _ := http.NewRequest(method, b.base+path, strings.NewReader(body))
	if b.cookie != nil {
		req.AddCookie(b.cookie)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(raw)
}

func joinNodes(t *testing.T, a, b *testNode) {
	t.Helper()
	_, body := a.signIn(t, "onur").req("POST", "/api/v1/cluster/invite", "")
	var inv struct{ Code string }
	if err := json.Unmarshal([]byte(body), &inv); err != nil || inv.Code == "" {
		t.Fatalf("invite: %s", body)
	}
	resp, body := b.signIn(t, "onur").req("POST", "/api/v1/cluster/join", `{"code":"`+inv.Code+`"}`)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("join: %d %s", resp.StatusCode, body)
	}
}

func TestClusterJoinAndForward(t *testing.T) {
	a := newTestNode(t, "alpha", []virt.Domain{{UUID: "a1", Name: "on-alpha"}})
	b := newTestNode(t, "beta", []virt.Domain{{UUID: "b1", Name: "on-beta"}})
	joinNodes(t, a, b)
	if !a.store.Clustered() || !b.store.Clustered() {
		t.Fatal("not clustered after join")
	}

	admin := a.signIn(t, "onur")
	_, body := admin.req("GET", "/api/v1/cluster", "")
	if !strings.Contains(body, `"name":"beta"`) || strings.Count(body, `"reachable":true`) != 2 || !strings.Contains(body, "v-beta") {
		t.Errorf("cluster view: %s", body)
	}

	// The UI on alpha reads beta's machines through alpha.
	beta := "/api/v1/hosts/" + b.store.SelfID()
	if _, body := admin.req("GET", beta+"/domains", ""); !strings.Contains(body, "on-beta") || strings.Contains(body, "on-alpha") {
		t.Errorf("forwarded list: %s", body)
	}
	// Its own ID works the same, so the UI can always use the prefix.
	if _, body := admin.req("GET", "/api/v1/hosts/"+a.store.SelfID()+"/domains", ""); !strings.Contains(body, "on-alpha") {
		t.Errorf("own prefix: %s", body)
	}
	if resp, _ := admin.req("GET", "/api/v1/hosts/nope/domains", ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown host: %d", resp.StatusCode)
	}

	// Roles apply across hosts: a viewer on alpha cannot start a machine on beta.
	view := a.signIn(t, "view")
	if resp, _ := view.req("POST", beta+"/domains/b1/actions/start", ""); resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer start on beta: %d", resp.StatusCode)
	}
	op := a.signIn(t, "op")
	if resp, body := op.req("POST", beta+"/domains/b1/actions/start", ""); resp.StatusCode != http.StatusAccepted {
		t.Errorf("operator start on beta: %d %s", resp.StatusCode, body)
	}
	if b.backend.lastAction != virt.ActionStart {
		t.Errorf("beta backend action = %q", b.backend.lastAction)
	}
	// Both hosts log who did it; beta also knows it came through alpha.
	if e := b.acts.List(1, activity.Filter{}); len(e) != 1 || e[0].User != "op" {
		t.Errorf("beta activity: %+v", e)
	}
	// alpha records after the answer has been relayed, so give it a moment.
	deadline := time.Now().Add(2 * time.Second)
	for !strings.HasPrefix(a.acts.List(1, activity.Filter{})[0].Path, "/api/v1/hosts/") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if e := a.acts.List(1, activity.Filter{}); len(e) != 1 || e[0].User != "op" || !strings.HasPrefix(e[0].Path, "/api/v1/hosts/") {
		t.Errorf("alpha activity: %+v", e)
	}
}

func TestClusterForwardsLiveStream(t *testing.T) {
	a := newTestNode(t, "alpha", []virt.Domain{})
	b := newTestNode(t, "beta", []virt.Domain{})
	b.live().backlog = [][]byte{[]byte(`{"host":"beta"}`)}
	joinNodes(t, a, b)
	admin := a.signIn(t, "onur")
	req, _ := http.NewRequest("GET", a.url+"/api/v1/hosts/"+b.store.SelfID()+"/stream", nil)
	req.AddCookie(admin.cookie)
	resp, err := admin.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 0, 256)
	chunk := make([]byte, 256)
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(string(buf), `"host":"beta"`) && time.Now().Before(deadline) {
		n, err := resp.Body.Read(chunk)
		buf = append(buf, chunk[:n]...)
		if err != nil {
			break
		}
	}
	if resp.Header.Get("Content-Type") != "text/event-stream" || !strings.Contains(string(buf), `"host":"beta"`) {
		t.Errorf("forwarded stream: %s %q", resp.Header.Get("Content-Type"), buf)
	}
}

func TestClusterRefusesForgedIdentity(t *testing.T) {
	a := newTestNode(t, "alpha", []virt.Domain{})
	b := newTestNode(t, "beta", []virt.Domain{})
	joinNodes(t, a, b)
	anon := &browser{t: t, base: b.url, c: &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}}

	// Identity headers without a peer credential are ignored.
	if resp, _ := anon.req("GET", "/api/v1/domains", "", cluster.HeaderUser, "onur", cluster.HeaderRole, "admin"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("forged headers: %d", resp.StatusCode)
	}
	// A wrong peer key is refused outright.
	if resp, _ := anon.req("GET", "/api/v1/domains", "", "Authorization", cluster.AuthScheme+a.store.SelfID()+".00",
		cluster.HeaderUser, "onur", cluster.HeaderRole, "admin"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong peer key: %d", resp.StatusCode)
	}
	// A real peer without a user may only use the membership protocol.
	if resp, _ := anon.req("GET", "/api/v1/domains", "", "Authorization", a.store.Credential()); resp.StatusCode != http.StatusForbidden {
		t.Errorf("peer without user: %d", resp.StatusCode)
	}
	if resp, _ := anon.req("GET", "/api/v1/cluster/peer/members", "", "Authorization", a.store.Credential()); resp.StatusCode != http.StatusOK {
		t.Errorf("peer protocol: %d", resp.StatusCode)
	}
	// Members don't forward for each other.
	if resp, _ := anon.req("GET", "/api/v1/hosts/"+a.store.SelfID()+"/domains", "", "Authorization", a.store.Credential(),
		cluster.HeaderUser, "onur", cluster.HeaderRole, "admin"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("chained forward: %d", resp.StatusCode)
	}
	// The membership protocol needs a member.
	if resp, _ := anon.req("GET", "/api/v1/cluster/peer/members", ""); resp.StatusCode != http.StatusForbidden {
		t.Errorf("members list without peer: %d", resp.StatusCode)
	}
}

func TestClusterJoinErrors(t *testing.T) {
	a := newTestNode(t, "alpha", []virt.Domain{})
	b := newTestNode(t, "beta", []virt.Domain{})
	_, body := a.signIn(t, "onur").req("POST", "/api/v1/cluster/invite", "")
	var inv struct{ Code string }
	json.Unmarshal([]byte(body), &inv)

	if resp, body := a.signIn(t, "onur").req("POST", "/api/v1/cluster/join", `{"code":"`+inv.Code+`"}`); resp.StatusCode != http.StatusConflict || !strings.Contains(body, "made on this host") {
		t.Errorf("join self: %d %s", resp.StatusCode, body)
	}
	if resp, _ := b.signIn(t, "op").req("POST", "/api/v1/cluster/join", `{"code":"`+inv.Code+`"}`); resp.StatusCode != http.StatusForbidden {
		t.Errorf("operator may not join: %d", resp.StatusCode)
	}
	// A code is good for one host only.
	admin := b.signIn(t, "onur")
	admin.req("POST", "/api/v1/cluster/join", `{"code":"`+inv.Code+`"}`)
	c := newTestNode(t, "gamma", []virt.Domain{})
	if resp, body := c.signIn(t, "onur").req("POST", "/api/v1/cluster/join", `{"code":"`+inv.Code+`"}`); resp.StatusCode != http.StatusConflict || !strings.Contains(body, "expired") {
		t.Errorf("code reuse: %d %s", resp.StatusCode, body)
	}
	// A code for a host that presents another certificate is refused before anything is sent.
	code, _ := cluster.ParseCode(inv.Code)
	code.Address = c.url
	if resp, body := newTestNode(t, "delta", nil).signIn(t, "onur").req("POST", "/api/v1/cluster/join", `{"code":"`+code.String()+`"}`); resp.StatusCode != http.StatusConflict || !strings.Contains(body, "different certificate") {
		t.Errorf("wrong certificate: %d %s", resp.StatusCode, body)
	}
}

func TestClusterRemoveMember(t *testing.T) {
	a := newTestNode(t, "alpha", []virt.Domain{})
	b := newTestNode(t, "beta", []virt.Domain{})
	joinNodes(t, a, b)
	if resp, _ := a.signIn(t, "onur").req("DELETE", "/api/v1/cluster/members/"+b.store.SelfID(), ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("remove: %d", resp.StatusCode)
	}
	deadline := time.Now().Add(3 * time.Second)
	for b.store.Clustered() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if a.store.Clustered() || b.store.Clustered() {
		t.Errorf("still clustered: alpha=%v beta=%v", a.store.Clustered(), b.store.Clustered())
	}
	// Beta's old key no longer works on alpha.
	anon := &browser{t: t, base: a.url, c: &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}}
	if resp, _ := anon.req("GET", "/api/v1/cluster/peer/members", "", "Authorization", b.store.Credential()); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("removed member still accepted: %d", resp.StatusCode)
	}
}

func TestClusterRemovedWhileAwayLeaves(t *testing.T) {
	a := newTestNode(t, "alpha", []virt.Domain{})
	b := newTestNode(t, "beta", []virt.Domain{})
	joinNodes(t, a, b)
	// alpha forgets beta without telling it (beta was "down").
	if _, _, err := a.store.Remove(b.store.SelfID()); err != nil {
		t.Fatal(err)
	}
	if !b.store.Clustered() {
		t.Fatal("beta should still believe it is clustered")
	}
	b.api.SyncMembers(context.Background())
	if b.store.Clustered() {
		t.Error("beta did not notice it was removed")
	}
}

func TestClusterSummaryThroughPeer(t *testing.T) {
	a := newTestNode(t, "alpha", []virt.Domain{})
	b := newTestNode(t, "beta", []virt.Domain{})
	b.live().backlog = [][]byte{[]byte(`{"old":1}`), []byte(`{"host":"beta-latest"}`)}
	joinNodes(t, a, b)
	resp, body := a.signIn(t, "view").req("GET", "/api/v1/hosts/"+b.store.SelfID()+"/summary", "")
	if resp.StatusCode != http.StatusOK || body != `{"host":"beta-latest"}` {
		t.Errorf("summary: %d %s", resp.StatusCode, body)
	}
}

// A cluster-wide VXLAN network gets one gateway (the host asked) and every member the
// others as peers.
func TestClusterVXLANHasOneGateway(t *testing.T) {
	a := newTestNode(t, "alpha", nil)
	b := newTestNode(t, "beta", nil)
	joinNodes(t, a, b)
	resp, body := a.signIn(t, "onur").req("POST", "/api/v1/cluster/networks",
		`{"name":"overlay","mode":"vxlan","vni":42,"subnet":"10.42.0.0/24","dhcp":true}`, "Origin", a.url)
	if resp.StatusCode != http.StatusOK || strings.Count(body, `"ok":true`) != 2 {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if len(a.backend.networks) != 1 || len(b.backend.networks) != 1 {
		t.Fatalf("created %d on alpha, %d on beta", len(a.backend.networks), len(b.backend.networks))
	}
	ga, gb := a.backend.networks[0], b.backend.networks[0]
	if ga.Subnet != "10.42.0.0/24" || !ga.DHCP || gb.Subnet != "" || gb.DHCP {
		t.Errorf("gateway: alpha %+v, beta %+v", ga, gb)
	}
	if len(ga.Peers) != 1 || ga.Peers[0] != "127.0.0.1" || len(gb.Peers) != 1 || gb.VNI != 42 {
		t.Errorf("peers: alpha %v, beta %v", ga.Peers, gb.Peers)
	}
}

// DRS and the fleet inventory read the other members on behalf of the user; without that
// the members refused and every peer looked unreachable.
func TestDRSAndInventoryReadPeers(t *testing.T) {
	a := newTestNode(t, "alpha", []virt.Domain{{UUID: "a1", Name: "on-alpha"}})
	b := newTestNode(t, "beta", []virt.Domain{{UUID: "b1", Name: "on-beta"}})
	joinNodes(t, a, b)
	eng, err := drs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.api.drs = eng
	b.lv.backlog = [][]byte{[]byte(`{"host":{"cpuPercent":64,"memTotalKiB":1000,"memUsedKiB":500},"domains":[{"uuid":"b1","name":"on-beta","state":"running","memoryKiB":100}]}`)}

	admin := a.signIn(t, "onur")
	_, body := admin.req("GET", "/api/v1/cluster/drs", "")
	var st drs.Status
	if err := json.Unmarshal([]byte(body), &st); err != nil || len(st.Hosts) != 2 {
		t.Fatalf("drs: %s", body)
	}
	beta := st.Hosts[1]
	if !beta.Reachable || beta.CPUPct != 64 || beta.MemPct != 50 || beta.VMCount != 1 {
		t.Errorf("beta as DRS sees it: %+v", beta)
	}
	if _, body := admin.req("GET", "/api/v1/cluster/inventory/machines", ""); !strings.Contains(body, "on-beta") || !strings.Contains(body, "on-alpha") {
		t.Errorf("inventory: %s", body)
	}
}

// DRS settings and rules made on one member reach the others (one DRS per cluster).
func TestDRSChangesReachEveryMember(t *testing.T) {
	a := newTestNode(t, "alpha", nil)
	b := newTestNode(t, "beta", nil)
	joinNodes(t, a, b)
	for _, n := range []*testNode{a, b} {
		eng, err := drs.New(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		n.api.drs = eng
	}
	admin := a.signIn(t, "onur")
	if resp, body := admin.req("PUT", "/api/v1/cluster/drs/config", `{"enabled":true,"mode":"fully_automated","thresholdStdDev":9}`, "Origin", a.url); resp.StatusCode != http.StatusOK {
		t.Fatalf("config: %d %s", resp.StatusCode, body)
	}
	if c := b.api.drs.GetConfig(); !c.Enabled || c.Mode != drs.ModeFullyAutomated || c.ThresholdStdDev != 9 {
		t.Errorf("beta's config: %+v", c)
	}
	resp, body := admin.req("POST", "/api/v1/cluster/drs/rules", `{"name":"apart","kind":"vm_anti_affinity","domains":["x","y"]}`, "Origin", a.url)
	var rule drs.AffinityRule
	if resp.StatusCode != http.StatusCreated || json.Unmarshal([]byte(body), &rule) != nil {
		t.Fatalf("rule: %d %s", resp.StatusCode, body)
	}
	if rs := b.api.drs.GetConfig().Rules; len(rs) != 1 || rs[0].ID != rule.ID {
		t.Errorf("beta's rules: %+v, want ID %s", rs, rule.ID)
	}
	if resp, _ := admin.req("DELETE", "/api/v1/cluster/drs/rules/"+rule.ID, "", "Origin", a.url); resp.StatusCode != http.StatusNoContent || len(b.api.drs.GetConfig().Rules) != 0 {
		t.Errorf("delete: %d, beta keeps %+v", resp.StatusCode, b.api.drs.GetConfig().Rules)
	}
	if resp, _ := admin.req("PUT", "/api/v1/cluster/drs/config", `{"enabled":true,"mode":"sometimes"}`, "Origin", a.url); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("bad mode answered %d, want 422", resp.StatusCode)
	}
}

// Project changes made on one member reach the others (ADR-0060: the same projects on
// every host), and the answer names the members that could not be reached.
func TestProjectChangesReachEveryMember(t *testing.T) {
	a := newTestNode(t, "alpha", nil)
	b := newTestNode(t, "beta", nil)
	g := newTestNode(t, "gamma", nil)
	joinNodes(t, a, b)
	joinNodes(t, a, g)
	admin := a.signIn(t, "onur")

	// Create: every member gets the project, and each one is named in the answer.
	resp, body := admin.req("POST", "/api/v1/projects",
		`{"id":"lab","name":"Lab","members":[{"kind":"user","name":"op","role":"operator"}]}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	var made struct {
		Project project.Project `json:"project"`
		Results []HostResult    `json:"results"`
	}
	if err := json.Unmarshal([]byte(body), &made); err != nil {
		t.Fatal(err)
	}
	byHost := map[string]HostResult{}
	for _, res := range made.Results {
		byHost[res.Host] = res
	}
	if len(byHost) != 2 || !byHost["beta"].OK || !byHost["gamma"].OK || byHost["beta"].Error != "" {
		t.Fatalf("create results: %s", body)
	}
	for _, n := range []*testNode{b, g} {
		p, ok := n.projects.Get("lab")
		if !ok || p.Name != "Lab" || len(p.Members) != 1 || p.Members[0].Name != "op" {
			t.Errorf("%s's copy: %+v", n.srv.URL, p)
		}
	}

	// Update: the same end state everywhere.
	resp, body = admin.req("PUT", "/api/v1/projects/lab", `{"name":"Laboratory","limits":{"vcpus":8}}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update: %d %s", resp.StatusCode, body)
	}
	made = struct {
		Project project.Project `json:"project"`
		Results []HostResult    `json:"results"`
	}{}
	if err := json.Unmarshal([]byte(body), &made); err != nil {
		t.Fatal(err)
	}
	for _, res := range made.Results {
		if !res.OK {
			t.Errorf("update results: %s", body)
		}
	}
	for _, n := range []*testNode{b, g} {
		p, _ := n.projects.Get("lab")
		if p.Name != "Laboratory" || p.Limits.VCPUs != 8 {
			t.Errorf("update did not reach %s: %+v", p.Name, p)
		}
	}

	// Delete: gone everywhere; a full sweep stays the plain 204.
	if resp, _ := admin.req("DELETE", "/api/v1/projects/lab", ""); resp.StatusCode != http.StatusNoContent {
		t.Errorf("delete: %d", resp.StatusCode)
	}
	for _, n := range []*testNode{b, g} {
		if _, ok := n.projects.Get("lab"); ok {
			t.Errorf("%s still has the project", n.store.SelfID())
		}
	}

	// A member that is down keeps its old projects, and every answer names it.
	g.srv.Close()
	resp, body = admin.req("POST", "/api/v1/projects", `{"id":"site","name":"Site"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create with a member down: %d %s", resp.StatusCode, body)
	}
	if err := json.Unmarshal([]byte(body), &made); err != nil {
		t.Fatal(err)
	}
	byHost = map[string]HostResult{}
	for _, res := range made.Results {
		byHost[res.Host] = res
	}
	if !byHost["beta"].OK || byHost["gamma"].OK || byHost["gamma"].Error == "" {
		t.Errorf("create with a member down: %s", body)
	}
	if _, ok := b.projects.Get("site"); !ok {
		t.Error("beta did not take the change")
	}
	// The delete answers 200 with the failures, so nobody has to guess who still has it.
	resp, body = admin.req("DELETE", "/api/v1/projects/site", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete with a member down: %d %s", resp.StatusCode, body)
	}
	var del struct{ Results []HostResult }
	if err := json.Unmarshal([]byte(body), &del); err != nil {
		t.Fatal(err)
	}
	byHost = map[string]HostResult{}
	for _, res := range del.Results {
		byHost[res.Host] = res
	}
	if !byHost["beta"].OK || byHost["gamma"].OK || byHost["gamma"].Error == "" {
		t.Errorf("delete with a member down: %s", body)
	}
	if _, ok := b.projects.Get("site"); ok {
		t.Error("beta still has the project")
	}
}

// #37: a rule made while a member was down reaches it with the next member sync.
func TestSyncMembersCatchesUpPlacement(t *testing.T) {
	a := newTestNode(t, "alpha", nil)
	b := newTestNode(t, "beta", nil)
	joinNodes(t, a, b)
	// Straight into alpha's store: beta never got a copy, as if it had been down.
	if _, err := a.placement.AddRule(placement.Rule{Name: "apart", Kind: placement.VMAntiAffinity, Domains: []string{"db1", "db2"}}); err != nil {
		t.Fatal(err)
	}
	if len(b.placement.Rules()) != 0 {
		t.Fatal("beta has the rule already")
	}
	b.api.SyncMembers(t.Context())
	if r := b.placement.Rules(); len(r) != 1 || r[0].Name != "apart" {
		t.Fatalf("beta rules after sync = %+v", r)
	}
	if b.placement.Shared().Version != a.placement.Shared().Version {
		t.Error("versions differ after sync")
	}
}
