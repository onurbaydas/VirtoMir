package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// server answers like a VirtoMir host; answers overrides a path's body.
func server(t *testing.T, answers map[string]string) *httptest.Server {
	t.Helper()
	body := map[string]string{
		"/api/v1/summary": `{"connected":true,"host":{"cpuPercent":42,"memTotalKiB":1000,"memUsedKiB":870},
			"domains":[{"uuid":"u1","name":"web-01","state":"running","cpuPercent":12,"vcpus":2,"memoryKiB":2048},
			           {"uuid":"u2","name":"db-01","state":"shutoff","vcpus":4}]}`,
		"/api/v1/storage/pools": `{"pools":[{"name":"default","type":"dir","active":true,"capacityBytes":100,"allocationBytes":50},
			{"name":"fast","type":"zfs","active":true,"capacityBytes":100,"allocationBytes":96},
			{"name":"san","type":"iscsi","active":true,"capacityBytes":100,"allocationBytes":100}]}`,
		"/api/v1/cluster":   `{"members":[{"name":"hv-01","reachable":true},{"name":"hv-02","reachable":true},{"name":"hv-03","reachable":false}]}`,
		"/api/v1/ha/status": `{"clustered":true,"quorum":{"quorate":true,"votes":2,"totalVotes":3,"required":2}}`,
		"/api/v1/alerts": `{"alerts":[{"severity":"warning","subject":"fast","message":"96 % full"},
			{"severity":"critical","subject":"db-01","message":"down","resolved":"2026-11-27T10:00:00Z"},
			{"severity":"critical","subject":"web-01","message":"crashed","ackedBy":"onur"}]}`,
	}
	for k, v := range answers {
		body[k] = v
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"sign in first"}`))
			return
		}
		b, ok := body[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(b))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func check(t *testing.T, srv *httptest.Server, args ...string) (int, string) {
	t.Helper()
	sum := sha256.Sum256(srv.Certificate().Raw)
	var out bytes.Buffer
	code := run(append([]string{"--url", srv.URL, "--token", "tok", "--fingerprint", hex.EncodeToString(sum[:])}, args...), &out)
	return code, out.String()
}

func TestModes(t *testing.T) {
	srv := server(t, nil)
	for _, c := range []struct {
		args []string
		code int
		want []string
	}{
		{[]string{"--host", "-w", "80", "-c", "95"}, Warning, []string{"CPU 42 %, memory 87 %", "1 of 2 machines running", "'memory'=87.0%;80;95;0;100"}},
		{[]string{"--host"}, OK, []string{"VIRTOMIR OK"}},
		{[]string{"--machine", "web-01", "-w", "10"}, Warning, []string{"web-01 is running", "'cpu'=12.0%"}},
		{[]string{"--machine", "db-01"}, Critical, []string{"db-01 is shutoff"}},
		{[]string{"--machine", "nope"}, Unknown, []string{`no machine "nope"`}},
		{[]string{"--pools"}, Critical, []string{"fast 96 % full", "'default'=50.0%;85;95;0;100"}},
		{[]string{"--cluster"}, Warning, []string{"2 of 3 members up, HA quorum 2 of 3 votes", "unreachable: hv-03", "members_up=2;;;0;3"}},
		{[]string{"--alerts"}, Warning, []string{"0 critical, 1 warning alerts open", "warning: fast — 96 % full"}},
		{nil, Unknown, []string{"choose a check"}},
	} {
		code, out := check(t, srv, c.args...)
		if code != c.code {
			t.Errorf("%v: code %d, want %d\n%s", c.args, code, c.code, out)
		}
		for _, w := range c.want {
			if !strings.Contains(out, w) {
				t.Errorf("%v: output lacks %q:\n%s", c.args, w, out)
			}
		}
	}
}

func TestProblems(t *testing.T) {
	srv := server(t, map[string]string{
		"/api/v1/summary":   `{"connected":false}`,
		"/api/v1/ha/status": `{"clustered":true,"quorum":{"quorate":false,"votes":1,"totalVotes":3,"required":2}}`,
	})
	if code, out := check(t, srv, "--host"); code != Critical || !strings.Contains(out, "does not reach libvirt") {
		t.Errorf("host: %d %s", code, out)
	}
	if code, out := check(t, srv, "--cluster"); code != Critical || !strings.Contains(out, "HA has no quorum") {
		t.Errorf("cluster: %d %s", code, out)
	}
	// A wrong token, a wrong certificate: UNKNOWN, never OK.
	var out bytes.Buffer
	if code := run([]string{"--url", srv.URL, "--token", "bad", "--insecure", "--alerts"}, &out); code != Unknown || !strings.Contains(out.String(), "sign in first") {
		t.Errorf("bad token: %d %s", code, out.String())
	}
	out.Reset()
	if code := run([]string{"--url", srv.URL, "--token", "tok", "--fingerprint", strings.Repeat("ab", 32), "--alerts"}, &out); code != Unknown {
		t.Errorf("wrong fingerprint: %d %s", code, out.String())
	}
}

func TestCheckmk(t *testing.T) {
	srv := server(t, nil)
	code, out := check(t, srv, "--checkmk")
	if code != 0 {
		t.Fatalf("code %d: %s", code, out)
	}
	for _, w := range []string{"<<<local:sep(0)>>>", `"VirtoMir pools"`, "<<<<db-01>>>>", `1 "VirtoMir machine" cpu=0.0;;;0;100|vcpus=4 shutoff`, "<<<<web-01>>>>", "<<<<>>>>"} {
		if !strings.Contains(out, w) {
			t.Errorf("lacks %q:\n%s", w, out)
		}
	}
}
