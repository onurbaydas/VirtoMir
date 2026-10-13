// Package auth signs people in with their Linux account (PAM, like Cockpit), keeps browser
// sessions and API tokens, and decides roles: root and the admin group (default "libvirt",
// the group that may manage VMs anyway) are admins; optional operator and viewer groups get
// less (see roles.go).
package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"
)

// ErrBadCredentials is the only failure a login form shows, so it never reveals whether
// the user exists.
var ErrBadCredentials = errors.New("wrong user name or password")

// ErrNotAllowed: the password was right but the account has no VirtoMir role.
var ErrNotAllowed = errors.New("this account is not allowed to use VirtoMir")

// Authenticator checks a user name and password.
type Authenticator interface {
	Authenticate(ctx context.Context, user, password string) error
}

const (
	CookieName = "virtomir_session"
	// IdleTimeout ends a session nobody used for a while; MaxAge ends any session.
	IdleTimeout = 2 * time.Hour
	MaxAge      = 24 * time.Hour
)

type session struct {
	user     string
	role     Role
	created  time.Time
	lastSeen time.Time
}

// Manager owns sessions and the login rate limit. The zero value is not usable; use New.
type Manager struct {
	authn  Authenticator
	roleOf func(user string) Role
	now    func() time.Time
	// ext: LDAP and OpenID Connect (nil: local accounts only).
	ext *External

	mu       sync.Mutex
	sessions map[string]*session
	failures map[string][]time.Time // remote IP → recent failed logins
}

// New returns a manager. roleOf decides each user's role at sign-in (NoAccess refuses).
func New(authn Authenticator, roleOf func(string) Role) *Manager {
	return &Manager{
		authn: authn, roleOf: roleOf, now: time.Now,
		sessions: map[string]*session{}, failures: map[string][]time.Time{},
	}
}

// SetExternal adds LDAP / OpenID Connect sign-in (local accounts keep working first).
func (m *Manager) SetExternal(e *External) { m.ext = e }

// External is the external sign-in configuration (nil when not set up).
func (m *Manager) External() *External { return m.ext }

// Login rate limit: after MaxFailures failed attempts from one address within
// FailureWindow, further attempts are refused until the oldest one ages out.
const (
	MaxFailures   = 5
	FailureWindow = 5 * time.Minute
)

// ErrTooManyAttempts means the address is temporarily locked out.
var ErrTooManyAttempts = errors.New("too many failed sign-in attempts, try again in a few minutes")

// Login checks the credentials and returns a new session token.
func (m *Manager) Login(ctx context.Context, remote, name, password string) (string, error) {
	ip := remoteIP(remote)
	if m.lockedOut(ip) {
		return "", ErrTooManyAttempts
	}
	if name == "" || password == "" {
		return "", ErrBadCredentials
	}
	id := Identity{Name: name}
	// Local accounts first (an admin can always get in), then LDAP.
	if err := m.authn.Authenticate(ctx, name, password); err == nil && !IsExternal(name) {
		id.Role = m.roleOf(name)
	} else if m.ext != nil {
		ext, lerr := m.ext.loginLDAP(ctx, name, password)
		if lerr != nil {
			m.fail(ip)
			if errors.Is(lerr, ErrBadCredentials) || errors.Is(err, ErrBadCredentials) {
				return "", ErrBadCredentials
			}
			return "", lerr
		}
		id = ext
	} else {
		m.fail(ip)
		if errors.Is(err, ErrBadCredentials) || err == nil {
			return "", ErrBadCredentials
		}
		return "", err
	}
	if id.Role == NoAccess {
		m.fail(ip)
		return "", ErrNotAllowed
	}
	m.mu.Lock()
	delete(m.failures, ip)
	m.mu.Unlock()
	return m.NewSession(id)
}

// NewSession starts a session for someone already authenticated (OpenID Connect).
func (m *Manager) NewSession(id Identity) (string, error) {
	if id.Role == NoAccess {
		return "", ErrNotAllowed
	}
	token := newToken()
	now := m.now()
	m.mu.Lock()
	m.sessions[token] = &session{user: id.Name, role: id.Role, created: now, lastSeen: now}
	m.mu.Unlock()
	return token, nil
}

// User returns the session's identity and refreshes its idle timer.
func (m *Manager) User(token string) (Identity, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[token]
	if !ok {
		return Identity{}, false
	}
	now := m.now()
	if now.Sub(s.lastSeen) > IdleTimeout || now.Sub(s.created) > MaxAge {
		delete(m.sessions, token)
		return Identity{}, false
	}
	s.lastSeen = now
	return Identity{Name: s.user, Role: s.role}, true
}

// RoleOf is the user's current role: Linux groups are re-read; LDAP/SSO users keep the role
// of their last sign-in.
func (m *Manager) RoleOf(user string) Role {
	if IsExternal(user) {
		if m.ext == nil {
			return NoAccess
		}
		return m.ext.RoleOf(user)
	}
	return m.roleOf(user)
}

// Valid reports whether the session still exists, without refreshing it (for long streams).
func (m *Manager) Valid(token string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[token]
	if !ok {
		return false
	}
	now := m.now()
	return now.Sub(s.lastSeen) <= IdleTimeout && now.Sub(s.created) <= MaxAge
}

func (m *Manager) Logout(token string) {
	m.mu.Lock()
	delete(m.sessions, token)
	m.mu.Unlock()
}

func (m *Manager) lockedOut(ip string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	recent := m.recentFailures(ip)
	return len(recent) >= MaxFailures
}

func (m *Manager) fail(ip string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failures[ip] = append(m.recentFailures(ip), m.now())
}

func (m *Manager) recentFailures(ip string) []time.Time {
	cutoff := m.now().Add(-FailureWindow)
	kept := m.failures[ip][:0]
	for _, t := range m.failures[ip] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(m.failures, ip)
		return nil
	}
	m.failures[ip] = kept
	return kept
}

// SessionToken returns the session cookie of a request ("" when none).
func SessionToken(r *http.Request) string {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// SetCookie stores the session in the browser: HttpOnly (no script access) and
// SameSite=Strict (not sent with cross-site requests, which blocks CSRF).
func SetCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: isHTTPS(r), MaxAge: int(MaxAge / time.Second),
	})
}

func ClearCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: isHTTPS(r), MaxAge: -1})
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

func remoteIP(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

func newToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return hex.EncodeToString(b)
}
