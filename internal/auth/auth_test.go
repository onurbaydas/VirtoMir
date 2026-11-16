package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"
)

type fakeAuthn map[string]string

func (f fakeAuthn) Authenticate(_ context.Context, u, p string) error {
	if pw, ok := f[u]; ok && pw == p {
		return nil
	}
	return ErrBadCredentials
}

func newTestManager() (*Manager, *time.Time) {
	now := time.Unix(1_000_000, 0)
	m := New(fakeAuthn{"onur": "secret", "guest": "guest"}, func(u string) Role {
		if u == "onur" {
			return Admin
		}
		return NoAccess
	})
	m.now = func() time.Time { return now }
	return m, &now
}

func TestLoginAndSession(t *testing.T) {
	m, now := newTestManager()
	tok, err := m.Login(context.Background(), "10.0.0.5:4444", "onur", "secret")
	if err != nil || len(tok) != 64 {
		t.Fatalf("login: %q %v", tok, err)
	}
	if u, ok := m.User(tok); !ok || u.Name != "onur" || u.Role != Admin {
		t.Fatalf("user = %q %v", u, ok)
	}
	// Activity keeps the session alive past the idle timeout…
	for range 3 {
		*now = now.Add(IdleTimeout - time.Minute)
		if _, ok := m.User(tok); !ok {
			t.Fatal("active session expired")
		}
	}
	// …but not past the absolute maximum.
	*now = now.Add(MaxAge)
	if _, ok := m.User(tok); ok {
		t.Error("session outlived MaxAge")
	}
}

func TestIdleTimeout(t *testing.T) {
	m, now := newTestManager()
	tok, _ := m.Login(context.Background(), "1.2.3.4:1", "onur", "secret")
	*now = now.Add(IdleTimeout + time.Second)
	if m.Valid(tok) {
		t.Error("idle session still valid")
	}
	if _, ok := m.User(tok); ok {
		t.Error("idle session still usable")
	}
}

func TestLoginFailures(t *testing.T) {
	m, now := newTestManager()
	ctx := context.Background()
	if _, err := m.Login(ctx, "1.1.1.1:1", "onur", "wrong"); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("wrong password: %v", err)
	}
	if _, err := m.Login(ctx, "1.1.1.1:1", "nobody", "x"); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("unknown user must look the same as a wrong password: %v", err)
	}
	if _, err := m.Login(ctx, "1.1.1.1:1", "guest", "guest"); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("valid but not in the group: %v", err)
	}
	if _, err := m.Login(ctx, "1.1.1.1:1", "onur", ""); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("empty password: %v", err)
	}
	// 3 counted failures so far (the empty password is rejected before PAM); 2 more lock out.
	m.Login(ctx, "1.1.1.1:1", "onur", "x")
	m.Login(ctx, "1.1.1.1:1", "onur", "x")
	if _, err := m.Login(ctx, "1.1.1.1:1", "onur", "secret"); !errors.Is(err, ErrTooManyAttempts) {
		t.Errorf("expected lockout, got %v", err)
	}
	// Another address is not affected.
	if _, err := m.Login(ctx, "2.2.2.2:1", "onur", "secret"); err != nil {
		t.Errorf("other address locked out: %v", err)
	}
	*now = now.Add(FailureWindow + time.Second)
	if _, err := m.Login(ctx, "1.1.1.1:1", "onur", "secret"); err != nil {
		t.Errorf("lockout did not expire: %v", err)
	}
}

func TestLogout(t *testing.T) {
	m, _ := newTestManager()
	tok, _ := m.Login(context.Background(), "1.2.3.4:1", "onur", "secret")
	m.Logout(tok)
	if _, ok := m.User(tok); ok {
		t.Error("session survived logout")
	}
}

func TestCookie(t *testing.T) {
	rec := httptest.NewRecorder()
	SetCookie(rec, httptest.NewRequest("POST", "https://h/api", nil), "tok")
	c := rec.Result().Cookies()[0]
	if c.Name != CookieName || !c.HttpOnly || !c.Secure || c.SameSite != 3 /* Strict */ {
		t.Errorf("cookie = %+v", c)
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(c)
	if SessionToken(req) != "tok" {
		t.Error("token not read back")
	}
}

func TestStubSaysWhy(t *testing.T) {
	if PAMAvailable {
		t.Skip("built with PAM")
	}
	if err := (PAM{}).Authenticate(context.Background(), "a", "b"); err == nil {
		t.Error("stub must refuse")
	}
}

func TestSelfSignedCert(t *testing.T) {
	dir := t.TempDir()
	c1, err := SelfSignedCert(dir)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := SelfSignedCert(dir) // second call loads the same files
	if err != nil {
		t.Fatal(err)
	}
	if string(c1.Certificate[0]) != string(c2.Certificate[0]) {
		t.Error("certificate regenerated instead of reused")
	}
}

// One IPv6 customer has a whole /64 of addresses: failed sign-ins are counted for all of it,
// so picking a new address for every guess buys nothing.
func TestLockoutCountsAnIPv6Network(t *testing.T) {
	m, _ := newTestManager()
	ctx := context.Background()
	for i := 0; i < MaxFailures; i++ {
		m.Login(ctx, fmt.Sprintf("[2001:db8:1:2::%x]:4000", i+1), "onur", "wrong")
	}
	if _, err := m.Login(ctx, "[2001:db8:1:2:aaaa:bbbb:cccc:dddd]:4000", "onur", "secret"); !errors.Is(err, ErrTooManyAttempts) {
		t.Errorf("another address of the same /64: %v", err)
	}
	if _, err := m.Login(ctx, "[2001:db8:1:3::1]:4000", "onur", "secret"); err != nil {
		t.Errorf("the neighbouring /64 is locked out: %v", err)
	}
	for addr, want := range map[string]string{
		"192.0.2.7:443":            "192.0.2.7",
		"[::ffff:192.0.2.7]:443":   "192.0.2.7",
		"[fe80::1%eth0]:443":       "fe80::/64",
		"[2001:db8:1:2::9]:443":    "2001:db8:1:2::/64",
		"2001:db8:1:2::9":          "2001:db8:1:2::/64",
		"not an address":           "not an address",
		"unix-socket-or-something": "unix-socket-or-something",
	} {
		if got := remoteIP(addr); got != want {
			t.Errorf("remoteIP(%q) = %q, want %q", addr, got, want)
		}
	}
}

// Addresses that failed once and sessions that ran out are forgotten, though nobody asks for
// them again.
func TestForgottenAddressesAndSessionsDoNotPileUp(t *testing.T) {
	m, now := newTestManager()
	ctx := context.Background()
	for i := 0; i < sweepAbove+10; i++ {
		m.Login(ctx, fmt.Sprintf("10.%d.%d.%d:1", i>>16&255, i>>8&255, i&255), "onur", "wrong")
	}
	for i := 0; i < sweepAbove+10; i++ {
		if _, err := m.NewSession(Identity{Name: "onur", Role: Admin}); err != nil {
			t.Fatal(err)
		}
	}
	*now = now.Add(MaxAge + time.Hour)
	m.Login(ctx, "192.0.2.1:1", "onur", "wrong")
	tok, err := m.NewSession(Identity{Name: "onur", Role: Admin})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	failures, sessions := len(m.failures), len(m.sessions)
	m.mu.Unlock()
	if failures != 1 || sessions != 1 {
		t.Errorf("remembered %d addresses and %d sessions, want 1 and 1", failures, sessions)
	}
	if _, ok := m.User(tok); !ok {
		t.Error("the new session went with the old ones")
	}
}
