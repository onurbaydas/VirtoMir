package auth

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-ldap/ldap/v3" // MIT
)

// External sign-in (ADR-0023): LDAP (user name + password, checked after the local
// accounts) and OpenID Connect (a "Sign in with …" redirect). Users from there get a
// suffix — alice@ldap, alice@sso — so they can never be mistaken for a local account, and
// their role comes from their directory groups.

const (
	SuffixLDAP = "@ldap"
	SuffixSSO  = "@sso"
	redacted   = "********"
)

// GroupRoles maps directory groups (names or full DNs) to roles.
type GroupRoles struct {
	Admin    []string `json:"admin"`
	Operator []string `json:"operator"`
	Viewer   []string `json:"viewer"`
}

// RoleFor is the highest role any of groups gives. Matching ignores case and accepts a
// group's CN for a full DN ("cn=vm-admins,ou=groups,dc=x" matches "vm-admins").
func (g GroupRoles) RoleFor(groups []string) Role {
	has := func(want []string) bool {
		for _, w := range want {
			for _, have := range groups {
				if strings.EqualFold(w, have) || strings.EqualFold(w, cnOf(have)) {
					return true
				}
			}
		}
		return false
	}
	switch {
	case has(g.Admin):
		return Admin
	case has(g.Operator):
		return Operator
	case has(g.Viewer):
		return Viewer
	}
	return NoAccess
}

func cnOf(dn string) string {
	first, _, _ := strings.Cut(dn, ",")
	if k, v, ok := strings.Cut(first, "="); ok && strings.EqualFold(strings.TrimSpace(k), "cn") {
		return strings.TrimSpace(v)
	}
	return dn
}

type LDAPConfig struct {
	Enabled bool `json:"enabled"`
	// URL: ldaps://dc1.example.com or ldap://… (with StartTLS).
	URL                string `json:"url"`
	StartTLS           bool   `json:"startTLS"`
	InsecureSkipVerify bool   `json:"insecureSkipVerify"`
	// BindDN/BindPassword: a service account to search with (empty: anonymous search).
	BindDN       string `json:"bindDN"`
	BindPassword string `json:"bindPassword,omitempty"`
	// UserBase and UserFilter find the user; %s is the (escaped) user name.
	UserBase   string `json:"userBase"`
	UserFilter string `json:"userFilter"`
	// GroupAttr on the user entry lists their groups (memberOf).
	GroupAttr string     `json:"groupAttr"`
	Roles     GroupRoles `json:"roles"`
}

type OIDCConfig struct {
	Enabled      bool   `json:"enabled"`
	Label        string `json:"label"`  // button text, e.g. "Sign in with Keycloak"
	Issuer       string `json:"issuer"` // https://idp.example.com/realms/main
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret,omitempty"`
	// UsernameClaim (preferred_username) and GroupsClaim (groups) in the ID token.
	UsernameClaim string     `json:"usernameClaim"`
	GroupsClaim   string     `json:"groupsClaim"`
	Roles         GroupRoles `json:"roles"`
}

type ExternalConfig struct {
	LDAP LDAPConfig `json:"ldap"`
	OIDC OIDCConfig `json:"oidc"`
}

// externalState is the file: the configuration plus the last role each external user had,
// so their API tokens keep the right role between sign-ins and restarts.
type externalState struct {
	Config ExternalConfig  `json:"config"`
	Roles  map[string]Role `json:"roles"`
}

// External holds the external sign-in configuration (in -state-dir/signin.json, 0600).
type External struct {
	path string
	mu   sync.Mutex
	st   externalState
	// ldapAuth is replaceable in tests.
	ldapAuth func(ctx context.Context, cfg LDAPConfig, user, password string) ([]string, error)
}

func OpenExternal(path string) (*External, error) {
	e := &External{path: path, st: externalState{Roles: map[string]Role{}}, ldapAuth: ldapAuthenticate}
	e.st.Config.LDAP.UserFilter = "(uid=%s)"
	e.st.Config.LDAP.GroupAttr = "memberOf"
	e.st.Config.OIDC.UsernameClaim = "preferred_username"
	e.st.Config.OIDC.GroupsClaim = "groups"
	e.st.Config.OIDC.Label = "Sign in with single sign-on"
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return e, nil
	case err != nil:
		return nil, err
	}
	if err := json.Unmarshal(b, &e.st); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if e.st.Roles == nil {
		e.st.Roles = map[string]Role{}
	}
	return e, nil
}

func (e *External) saveLocked() error {
	b, err := json.MarshalIndent(e.st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(e.path), 0o750); err != nil {
		return err
	}
	tmp := e.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, e.path)
}

// Config returns the configuration; with redact, secrets are replaced by a placeholder.
func (e *External) Config(redact bool) ExternalConfig {
	e.mu.Lock()
	defer e.mu.Unlock()
	c := e.st.Config
	if redact {
		if c.LDAP.BindPassword != "" {
			c.LDAP.BindPassword = redacted
		}
		if c.OIDC.ClientSecret != "" {
			c.OIDC.ClientSecret = redacted
		}
	}
	return c
}

// SetConfig stores a new configuration. Secrets left as the placeholder keep their value.
func (e *External) SetConfig(c ExternalConfig) error {
	if c.LDAP.Enabled {
		if !strings.HasPrefix(c.LDAP.URL, "ldap://") && !strings.HasPrefix(c.LDAP.URL, "ldaps://") {
			return errors.New("the LDAP URL starts with ldaps:// or ldap://")
		}
		if strings.HasPrefix(c.LDAP.URL, "ldap://") && !c.LDAP.StartTLS && !loopbackURL(c.LDAP.URL) {
			return errors.New("passwords would cross the network unencrypted: use ldaps:// or StartTLS")
		}
		if c.LDAP.UserBase == "" || !strings.Contains(c.LDAP.UserFilter, "%s") {
			return errors.New("LDAP needs the base DN of the users and a filter with %s for the user name")
		}
	}
	if c.OIDC.Enabled {
		if !strings.HasPrefix(c.OIDC.Issuer, "https://") || c.OIDC.ClientID == "" {
			return errors.New("OpenID Connect needs the issuer URL (https://…) and a client ID")
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if c.LDAP.BindPassword == redacted {
		c.LDAP.BindPassword = e.st.Config.LDAP.BindPassword
	}
	if c.OIDC.ClientSecret == redacted {
		c.OIDC.ClientSecret = e.st.Config.OIDC.ClientSecret
	}
	prev := e.st.Config
	e.st.Config = c
	if err := e.saveLocked(); err != nil {
		e.st.Config = prev
		return err
	}
	return nil
}

// loopbackURL: a directory on this very host (plain LDAP never leaves the machine).
func loopbackURL(u string) bool {
	rest := strings.TrimPrefix(strings.TrimPrefix(u, "ldap://"), "ldaps://")
	host, _, _ := strings.Cut(rest, "/")
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// remember keeps an external user's role from their last sign-in.
func (e *External) remember(name string, r Role) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.st.Roles[name] == r {
		return
	}
	e.st.Roles[name] = r
	_ = e.saveLocked()
}

// RoleOf is an external user's role as of their last sign-in.
func (e *External) RoleOf(name string) Role {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.st.Roles[name]
}

// IsExternal reports whether a user name belongs to LDAP or SSO.
func IsExternal(name string) bool {
	return strings.HasSuffix(name, SuffixLDAP) || strings.HasSuffix(name, SuffixSSO)
}

// loginLDAP checks a user against LDAP; the identity is user@ldap with the role from the
// directory groups.
func (e *External) loginLDAP(ctx context.Context, user, password string) (Identity, error) {
	cfg := e.Config(false).LDAP
	if !cfg.Enabled {
		return Identity{}, ErrBadCredentials
	}
	groups, err := e.ldapAuth(ctx, cfg, user, password)
	if err != nil {
		return Identity{}, err
	}
	id := Identity{Name: user + SuffixLDAP, Role: cfg.Roles.RoleFor(groups)}
	e.remember(id.Name, id.Role)
	return id, nil
}

// ldapAuthenticate finds the user, binds as them with the password and returns their groups.
func ldapAuthenticate(ctx context.Context, cfg LDAPConfig, user, password string) ([]string, error) {
	if strings.ContainsAny(user, "()*\\\x00") || len(user) > 128 {
		return nil, ErrBadCredentials
	}
	tlsConf := &tls.Config{InsecureSkipVerify: cfg.InsecureSkipVerify, MinVersion: tls.VersionTLS12} //nolint:gosec // the admin's explicit choice for self-signed directories
	conn, err := ldap.DialURL(cfg.URL, ldap.DialWithTLSConfig(tlsConf))
	if err != nil {
		return nil, fmt.Errorf("cannot reach the LDAP server: %w", err)
	}
	defer conn.Close()
	conn.SetTimeout(10 * time.Second)
	if cfg.StartTLS {
		if err := conn.StartTLS(tlsConf); err != nil {
			return nil, fmt.Errorf("LDAP StartTLS: %w", err)
		}
	}
	if cfg.BindDN != "" {
		if err := conn.Bind(cfg.BindDN, cfg.BindPassword); err != nil {
			return nil, fmt.Errorf("the LDAP service account cannot sign in: %w", err)
		}
	}
	attr := cfg.GroupAttr
	if attr == "" {
		attr = "memberOf"
	}
	res, err := conn.Search(ldap.NewSearchRequest(cfg.UserBase, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 2, 10, false,
		fmt.Sprintf(cfg.UserFilter, ldap.EscapeFilter(user)), []string{"dn", attr}, nil))
	if err != nil {
		return nil, fmt.Errorf("LDAP search: %w", err)
	}
	if len(res.Entries) != 1 {
		return nil, ErrBadCredentials
	}
	entry := res.Entries[0]
	if err := conn.Bind(entry.DN, password); err != nil {
		return nil, ErrBadCredentials
	}
	return entry.GetAttributeValues(attr), nil
}

// OIDCEnabled and LDAPEnabled tell the sign-in page what to offer.
func (e *External) Methods() (ldapOn bool, oidcOn bool, oidcLabel string) {
	c := e.Config(false)
	return c.LDAP.Enabled, c.OIDC.Enabled, c.OIDC.Label
}

// SSOIdentity turns verified OIDC claims into an identity (user@sso) and remembers its role.
func (e *External) SSOIdentity(username string, groups []string) Identity {
	cfg := e.Config(false).OIDC
	id := Identity{Name: username + SuffixSSO, Role: cfg.Roles.RoleFor(groups)}
	e.remember(id.Name, id.Role)
	return id
}

// sortedRoles is a stable view for tests.
func (e *External) sortedRoles() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for k, v := range e.st.Roles {
		out = append(out, k+"="+v.String())
	}
	slices.Sort(out)
	return out
}
