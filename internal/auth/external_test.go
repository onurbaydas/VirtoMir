package auth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestGroupRoles(t *testing.T) {
	g := GroupRoles{Admin: []string{"vm-admins"}, Operator: []string{"cn=ops,ou=groups,dc=x"}, Viewer: []string{"staff"}}
	cases := map[Role][]string{
		Admin:    {"staff", "CN=vm-admins,OU=Groups,DC=example,DC=com"}, // CN of a DN, any case
		Operator: {"cn=ops,ou=groups,dc=x"},
		Viewer:   {"Staff"},
		NoAccess: {"others"},
	}
	for want, groups := range cases {
		if got := g.RoleFor(groups); got != want {
			t.Errorf("RoleFor(%v) = %s, want %s", groups, got, want)
		}
	}
}

type localOnly struct{}

func (localOnly) Authenticate(_ context.Context, u, p string) error {
	if u == "root" && p == "rootpw" {
		return nil
	}
	return ErrBadCredentials
}

func TestLoginFallsBackToLDAP(t *testing.T) {
	e, err := OpenExternal(filepath.Join(t.TempDir(), "signin.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.SetConfig(ExternalConfig{LDAP: LDAPConfig{Enabled: true, URL: "ldaps://dc", UserBase: "dc=x", UserFilter: "(uid=%s)",
		Roles: GroupRoles{Operator: []string{"vm-ops"}}}}); err != nil {
		t.Fatal(err)
	}
	e.ldapAuth = func(_ context.Context, _ LDAPConfig, u, p string) ([]string, error) {
		switch {
		case u == "alice" && p == "secret":
			return []string{"cn=vm-ops,ou=g,dc=x"}, nil
		case u == "bob" && p == "secret":
			return []string{"cn=nobody"}, nil
		case u == "root" && p == "ldappw": // a directory user named like a local admin
			return []string{"cn=vm-ops"}, nil
		}
		return nil, ErrBadCredentials
	}
	m := New(localOnly{}, func(u string) Role {
		if u == "root" {
			return Admin
		}
		return NoAccess
	})
	m.SetExternal(e)
	ctx := context.Background()

	tok, err := m.Login(ctx, "1.2.3.4:5", "alice", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := m.User(tok); id.Name != "alice@ldap" || id.Role != Operator {
		t.Errorf("alice: %+v", id)
	}
	if m.RoleOf("alice@ldap") != Operator {
		t.Error("external role not remembered")
	}
	if _, err := m.Login(ctx, "1.2.3.5:5", "bob", "secret"); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("bob without a mapped group: %v", err)
	}
	// "root" from LDAP is root@ldap with its directory role, never the local root.
	tok, err = m.Login(ctx, "1.2.3.6:5", "root", "ldappw")
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := m.User(tok); id.Name != "root@ldap" || id.Role != Operator {
		t.Errorf("ldap root: %+v", id)
	}
	// The local root still signs in locally.
	tok, _ = m.Login(ctx, "1.2.3.7:5", "root", "rootpw")
	if id, _ := m.User(tok); id.Name != "root" || id.Role != Admin {
		t.Errorf("local root: %+v", id)
	}
	if _, err := m.Login(ctx, "1.2.3.8:5", "alice", "wrong"); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("wrong password: %v", err)
	}
}

func TestExternalConfigSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "signin.json")
	e, _ := OpenExternal(path)
	c := ExternalConfig{OIDC: OIDCConfig{Enabled: true, Issuer: "https://idp", ClientID: "virtomir", ClientSecret: "s3cret"}}
	if err := e.SetConfig(c); err != nil {
		t.Fatal(err)
	}
	pub := e.Config(true)
	if pub.OIDC.ClientSecret != redacted {
		t.Errorf("secret shown: %q", pub.OIDC.ClientSecret)
	}
	// Saving the redacted form keeps the secret.
	if err := e.SetConfig(pub); err != nil {
		t.Fatal(err)
	}
	e2, _ := OpenExternal(path)
	if e2.Config(false).OIDC.ClientSecret != "s3cret" {
		t.Error("secret lost")
	}
	if err := e.SetConfig(ExternalConfig{LDAP: LDAPConfig{Enabled: true, URL: "ldap://dc", UserBase: "dc=x", UserFilter: "(uid=%s)"}}); err == nil {
		t.Error("plain ldap:// without StartTLS accepted")
	}
	if err := e.SetConfig(ExternalConfig{LDAP: LDAPConfig{Enabled: true, URL: "ldap://127.0.0.1:389", UserBase: "dc=x", UserFilter: "(uid=%s)"}}); err != nil {
		t.Errorf("plain ldap to this host refused: %v", err)
	}
}
