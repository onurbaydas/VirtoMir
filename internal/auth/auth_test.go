package auth

import (
	"context"
	"errors"
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
