package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/onurbaydas/virtomir/internal/auth"
	"github.com/onurbaydas/virtomir/internal/virt"
)

func TestTwoFactorAPI(t *testing.T) {
	m := auth.New(stubAuthn{}, func(u string) auth.Role {
		return map[string]auth.Role{"onur": auth.Admin, "view": auth.Viewer}[u]
	})
	tf, err := auth.OpenTwoFactor(filepath.Join(t.TempDir(), "2fa.json"))
	if err != nil {
		t.Fatal(err)
	}
	m.SetTwoFactor(tf)
	tokens, _ := auth.OpenTokens("")
	h := NewServer(Options{Backend: &fakeBackend{domains: []virt.Domain{}}, Live: &fakeLive{}, Auth: m, Tokens: tokens,
		HostName: "h1", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Handler()

	viewer := signIn(t, h, "view")
	rec := do(h, "POST", "/api/v1/account/2fa/setup", "", viewer)
	var setup struct{ Secret, URI, QR string }
	_ = json.Unmarshal(rec.Body.Bytes(), &setup)
	if rec.Code != http.StatusOK || setup.Secret == "" || !strings.HasPrefix(setup.QR, "data:image/png;base64,") ||
		!strings.Contains(setup.URI, "VirtoMir%20h1:view") {
		t.Fatalf("setup: %d %s", rec.Code, rec.Body)
	}
	code, _ := auth.TOTPCode(setup.Secret, time.Now())
	rec = do(h, "POST", "/api/v1/account/2fa/confirm", `{"code":"`+code+`"}`, viewer)
	var conf struct{ Recovery []string }
	_ = json.Unmarshal(rec.Body.Bytes(), &conf)
	if rec.Code != http.StatusOK || len(conf.Recovery) != 10 {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body)
	}

	// Signing in now takes two steps.
	rec = do(h, "POST", "/api/v1/auth/login", `{"user":"view","password":"pw"}`, nil)
	var info struct {
		NeedCode bool
		Pending  string
		User     string
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &info)
	if rec.Code != http.StatusOK || !info.NeedCode || info.Pending == "" || info.User != "" || len(rec.Result().Cookies()) != 0 {
		t.Fatalf("login step 1: %d %s", rec.Code, rec.Body)
	}
	rec = do(h, "POST", "/api/v1/auth/2fa", `{"pending":"`+info.Pending+`","code":"`+conf.Recovery[0]+`"}`, nil)
	if rec.Code != http.StatusOK || len(rec.Result().Cookies()) != 1 || !strings.Contains(rec.Body.String(), `"user":"view"`) {
		t.Fatalf("login step 2: %d %s", rec.Code, rec.Body)
	}

	// Turning it off needs a code; an admin can reset it for anyone.
	if rec = do(h, "POST", "/api/v1/account/2fa/disable", `{"code":"000000"}`, viewer); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("disable with a wrong code: %d", rec.Code)
	}
	if rec = do(h, "DELETE", "/api/v1/twofactor/users/view", "", viewer); rec.Code != http.StatusForbidden {
		t.Errorf("viewer resets: %d", rec.Code)
	}
	admin := signIn(t, h, "onur")
	rec = do(h, "GET", "/api/v1/twofactor/users", "", admin)
	if !strings.Contains(rec.Body.String(), `"view"`) {
		t.Errorf("users: %s", rec.Body)
	}
	if rec = do(h, "DELETE", "/api/v1/twofactor/users/view", "", admin); rec.Code != http.StatusNoContent {
		t.Errorf("admin reset: %d", rec.Code)
	}
	if tf.Enabled("view") {
		t.Error("still on after reset")
	}
}
