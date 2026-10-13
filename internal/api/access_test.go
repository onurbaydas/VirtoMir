package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/onurbaydas/virtomir/internal/activity"
	"github.com/onurbaydas/virtomir/internal/auth"
	"github.com/onurbaydas/virtomir/internal/virt"
)

func rbacServer(t *testing.T) (http.Handler, *fakeBackend, *activity.Log) {
	t.Helper()
	m := auth.New(stubAuthn{}, func(u string) auth.Role {
		return map[string]auth.Role{"onur": auth.Admin, "op": auth.Operator, "view": auth.Viewer}[u]
	})
	tokens, _ := auth.OpenTokens("")
	log, _ := activity.Open("")
	b := &fakeBackend{domains: []virt.Domain{}}
	h := NewServer(Options{Backend: b, Live: &fakeLive{}, Auth: m, Tokens: tokens, Activity: log, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Handler()
	return h, b, log
}

func signIn(t *testing.T, h http.Handler, user string) *http.Cookie {
	t.Helper()
	rec := do(h, "POST", "/api/v1/auth/login", `{"user":"`+user+`","password":"pw"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("sign in %s: %d", user, rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"role"`) {
		t.Errorf("session info lacks role: %s", rec.Body)
	}
	return rec.Result().Cookies()[0]
}

func TestRequiredRole(t *testing.T) {
	cases := []struct {
		method, path string
		want         auth.Role
	}{
		{"GET", "/api/v1/domains", auth.Viewer},
		{"GET", "/api/v1/domains/u1/xml", auth.Viewer},
		{"POST", "/api/v1/domains/u1/actions/start", auth.Operator},
		{"PUT", "/api/v1/domains/u1/media", auth.Operator},
		{"POST", "/api/v1/domains/u1/snapshots", auth.Operator},
		{"POST", "/api/v1/domains/u1/snapshots/s1/revert", auth.Operator},
		{"DELETE", "/api/v1/domains/u1/snapshots/s1", auth.Admin},
		{"GET", "/api/v1/domains/u1/console/vnc", auth.Operator},
		{"DELETE", "/api/v1/domains/u1", auth.Admin},
		{"PUT", "/api/v1/domains/u1/xml", auth.Admin},
		{"POST", "/api/v1/domains", auth.Admin},
		{"POST", "/api/v1/networks", auth.Admin},
		{"GET", "/api/v1/activity", auth.Admin},
		{"POST", "/api/v1/tokens", auth.Viewer},
		{"POST", "/api/v1/auth/login", auth.NoAccess},
	}
	for _, c := range cases {
		if got := requiredRole(c.method, c.path); got != c.want {
			t.Errorf("%s %s = %s, want %s", c.method, c.path, got, c.want)
		}
	}
}

func TestRolesEnforced(t *testing.T) {
	h, b, log := rbacServer(t)
	view, op := signIn(t, h, "view"), signIn(t, h, "op")

	if rec := do(h, "GET", "/api/v1/domains", "", view); rec.Code != http.StatusOK {
		t.Errorf("viewer read: %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/v1/domains/u1/actions/start", "", view); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "viewer") {
		t.Errorf("viewer power action: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/api/v1/domains/u1/actions/start", "", op); rec.Code != http.StatusAccepted || b.lastAction != virt.ActionStart {
		t.Errorf("operator power action: %d", rec.Code)
	}
	if rec := do(h, "DELETE", "/api/v1/domains/u1", "", op); rec.Code != http.StatusForbidden {
		t.Errorf("operator delete: %d", rec.Code)
	}
	if rec := do(h, "GET", "/api/v1/activity", "", op); rec.Code != http.StatusForbidden {
		t.Errorf("operator activity: %d", rec.Code)
	}

	// Changes and sign-ins are recorded, refusals included.
	entries := log.List(50, activity.Filter{})
	var start, signins int
	for _, e := range entries {
		if strings.HasSuffix(e.Path, "/actions/start") && e.User == "op" && e.Status == http.StatusAccepted {
			start++
		}
		if e.Path == "/api/v1/auth/login" {
			signins++
		}
	}
	if start != 1 || signins != 2 {
		t.Errorf("activity: start=%d signins=%d entries=%+v", start, signins, entries)
	}
}

func TestAPITokens(t *testing.T) {
	h, _, _ := rbacServer(t)
	op := signIn(t, h, "op")

	if rec := do(h, "POST", "/api/v1/tokens", `{"name":"x","role":"admin"}`, op); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("token above own role: %d", rec.Code)
	}
	rec := do(h, "POST", "/api/v1/tokens", `{"name":"ci","role":"operator"}`, op)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var created struct {
		Secret string     `json:"secret"`
		Token  auth.Token `json:"token"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	bearer := func(method, path string) *httptest.ResponseRecorder {
		return do(h, method, path, "", nil, "Authorization", "Bearer "+created.Secret, "Origin", "https://elsewhere.example")
	}
	// A token works without a cookie and without same-origin (it is not a browser credential).
	if rec := bearer("POST", "/api/v1/domains/u1/actions/start"); rec.Code != http.StatusAccepted {
		t.Errorf("token action: %d %s", rec.Code, rec.Body)
	}
	if rec := bearer("POST", "/api/v1/tokens"); rec.Code != http.StatusForbidden && rec.Code != http.StatusBadRequest {
		t.Errorf("token creating tokens: %d", rec.Code)
	}
	if rec := do(h, "GET", "/api/v1/domains", "", nil, "Authorization", "Bearer vmt_bad_bad"); rec.Code != http.StatusUnauthorized {
		t.Errorf("bad token: %d", rec.Code)
	}
	if rec := do(h, "GET", "/api/v1/tokens", "", op); !strings.Contains(rec.Body.String(), `"ci"`) || strings.Contains(rec.Body.String(), "hash") {
		t.Errorf("list: %s", rec.Body)
	}
	if rec := do(h, "DELETE", "/api/v1/tokens/"+created.Token.ID, "", op); rec.Code != http.StatusNoContent {
		t.Errorf("revoke: %d", rec.Code)
	}
	if rec := bearer("GET", "/api/v1/domains"); rec.Code != http.StatusUnauthorized {
		t.Errorf("revoked token: %d", rec.Code)
	}
}
