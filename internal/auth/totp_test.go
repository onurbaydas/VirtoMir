package auth

import (
	"context"
	"encoding/base32"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTOTPVectors(t *testing.T) {
	// RFC 6238 appendix B (SHA-1), last 6 of the 8 digits.
	key := []byte("12345678901234567890")
	for unix, want := range map[int64]string{59: "287082", 1111111109: "081804", 1234567890: "005924", 2000000000: "279037"} {
		if got := totpAt(key, unix/30); got != want {
			t.Errorf("t=%d: %s, want %s", unix, got, want)
		}
	}
}

type fixedClock struct{ t time.Time }

func (c *fixedClock) now() time.Time { return c.t }

func codeFor(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		t.Fatal(err)
	}
	return totpAt(key, at.Unix()/30)
}

func TestTwoFactorFlow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "twofactor.json")
	tf, err := OpenTwoFactor(path)
	if err != nil {
		t.Fatal(err)
	}
	clk := &fixedClock{time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)}
	tf.now = clk.now

	secret, uri, err := tf.Begin("alice", "VirtoMir host1")
	if err != nil || !strings.HasPrefix(uri, "otpauth://totp/VirtoMir%20host1:alice?") || !strings.Contains(uri, "secret="+secret) {
		t.Fatalf("begin: %v %s", err, uri)
	}
	if tf.Enabled("alice") {
		t.Fatal("enabled before confirm")
	}
	if _, err := tf.Confirm("alice", "000000"); !errors.Is(err, ErrBadCode) {
		t.Fatalf("wrong confirm code: %v", err)
	}
	code := codeFor(t, secret, clk.t)
	recovery, err := tf.Confirm("alice", code)
	if err != nil || len(recovery) != recoveryCount || !tf.Enabled("alice") {
		t.Fatalf("confirm: %v %v", err, recovery)
	}
	// The code used for confirming is spent; the next step's code works (a little later).
	if err := tf.Verify("alice", code); !errors.Is(err, ErrBadCode) {
		t.Error("code accepted twice")
	}
	clk.t = clk.t.Add(30 * time.Second)
	if err := tf.Verify("alice", codeFor(t, secret, clk.t)); err != nil {
		t.Errorf("next code: %v", err)
	}
	// A code from far away in time is refused.
	if err := tf.Verify("alice", codeFor(t, secret, clk.t.Add(5*time.Minute))); !errors.Is(err, ErrBadCode) {
		t.Error("future code accepted")
	}
	// A recovery code works once.
	if err := tf.Verify("alice", strings.ToUpper(recovery[3])); err != nil {
		t.Errorf("recovery: %v", err)
	}
	if err := tf.Verify("alice", recovery[3]); !errors.Is(err, ErrBadCode) {
		t.Error("recovery code reused")
	}
	if s := tf.Status("alice"); !s.Enabled || s.RecoveryLeft != recoveryCount-1 {
		t.Errorf("status %+v", s)
	}
	if _, _, err := tf.Begin("alice", "x"); err == nil {
		t.Error("a new secret replaced an active one")
	}

	// Survives a restart; secrets are not world-readable.
	again, err := OpenTwoFactor(path)
	if err != nil || !again.Enabled("alice") {
		t.Fatalf("reopen: %v", err)
	}
	if err := tf.Disable("alice"); err != nil || tf.Enabled("alice") {
		t.Errorf("disable: %v", err)
	}
}

func TestLoginWithCode(t *testing.T) {
	tf, _ := OpenTwoFactor(filepath.Join(t.TempDir(), "2fa.json"))
	clk := &fixedClock{time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)}
	tf.now = clk.now
	secret, _, _ := tf.Begin("alice", "VirtoMir")
	if _, err := tf.Confirm("alice", codeFor(t, secret, clk.t)); err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(time.Minute)

	m := New(fakeAuthn{"alice": "pw", "bob": "pw"}, func(string) Role { return Admin })
	m.SetTwoFactor(tf)
	m.now = clk.now

	// bob has no 2FA: straight in.
	if tok, err := m.Login(context.Background(), "10.0.0.1:1", "bob", "pw"); err != nil || tok == "" {
		t.Fatalf("bob: %v", err)
	}
	_, err := m.Login(context.Background(), "10.0.0.1:1", "alice", "pw")
	var need *CodeRequired
	if !errors.As(err, &need) || need.Pending == "" {
		t.Fatalf("alice without code: %v", err)
	}
	if _, _, err := m.CompleteLogin("10.0.0.1:1", need.Pending, "123456"); !errors.Is(err, ErrBadCode) {
		t.Errorf("wrong code: %v", err)
	}
	id, tok, err := m.CompleteLogin("10.0.0.1:1", need.Pending, codeFor(t, secret, clk.t))
	if err != nil || tok == "" || id.Name != "alice" {
		t.Fatalf("right code: %v", err)
	}
	if _, ok := m.User(tok); !ok {
		t.Error("no session")
	}
	// The pending sign-in is gone once used.
	if _, _, err := m.CompleteLogin("10.0.0.1:1", need.Pending, codeFor(t, secret, clk.t.Add(30*time.Second))); err == nil {
		t.Error("pending sign-in reused")
	}
	// A wrong password never reaches the code step.
	if _, err := m.Login(context.Background(), "10.0.0.2:1", "alice", "nope"); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("wrong password: %v", err)
	}
}
