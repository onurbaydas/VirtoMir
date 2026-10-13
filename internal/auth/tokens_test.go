package auth

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTokens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	ts, err := OpenTokens(path)
	if err != nil {
		t.Fatal(err)
	}
	onur := Identity{Name: "onur", Role: Operator}

	if _, _, err := ts.Create(onur, "too strong", Admin, 0); !errors.Is(err, ErrTokenRole) {
		t.Errorf("token above owner's role must be refused: %v", err)
	}
	secret, tok, err := ts.Create(onur, "ci", Viewer, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, TokenPrefix) || tok.Hash != "" {
		t.Errorf("secret %q, returned hash %q", secret, tok.Hash)
	}

	// Survives a restart; only the hash is on disk.
	ts2, _ := OpenTokens(path)
	got, ok := ts2.Verify(secret)
	if !ok || got.Owner != "onur" || got.Role != Viewer || got.Name != "ci" {
		t.Fatalf("verify = %+v %v", got, ok)
	}
	if _, ok := ts2.Verify(secret + "x"); ok {
		t.Error("wrong secret accepted")
	}
	if _, ok := ts2.Verify("vmt_nope_nope"); ok {
		t.Error("unknown id accepted")
	}

	// Others cannot revoke it, the owner and admins can.
	if err := ts2.Revoke(Identity{Name: "eve", Role: Operator}, tok.ID); err == nil {
		t.Error("another user revoked the token")
	}
	if err := ts2.Revoke(onur, tok.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := ts2.Verify(secret); ok {
		t.Error("revoked token still works")
	}
}

func TestTokenExpiry(t *testing.T) {
	ts, _ := OpenTokens("")
	now := time.Unix(1_000_000, 0)
	ts.now = func() time.Time { return now }
	secret, _, err := ts.Create(Identity{Name: "a", Role: Admin}, "short", Viewer, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ts.Verify(secret); !ok {
		t.Fatal("fresh token refused")
	}
	now = now.Add(2 * time.Hour)
	if _, ok := ts.Verify(secret); ok {
		t.Error("expired token accepted")
	}
}

func TestParseRole(t *testing.T) {
	for _, s := range []string{"viewer", "operator", "admin"} {
		r, err := ParseRole(s)
		if err != nil || r.String() != s {
			t.Errorf("%s → %v %v", s, r, err)
		}
	}
	if _, err := ParseRole("none"); err == nil {
		t.Error("none is not a grantable role")
	}
	if !(Viewer < Operator && Operator < Admin) {
		t.Error("role order")
	}
}
