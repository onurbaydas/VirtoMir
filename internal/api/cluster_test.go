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
	"github.com/onurbaydas/virtomir/internal/virt"
)

// testNode is a complete virtomird API on its own TLS port with its own certificate.
type testNode struct {
	url     string
	store   *cluster.Store
	backend *fakeBackend
	acts    *activity.Log
	lv      *fakeLive
	api     *Server
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
	})
	srv.Config.Handler = s.Handler()
	return &testNode{url: srv.URL, store: store, backend: b, acts: acts, lv: lv, api: s}
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
