// Package auth signs people in with their Linux account (PAM, like Cockpit) and keeps
// browser sessions. Only root and members of the admin group (default "libvirt", the group
// that may manage VMs anyway) are let in.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"os/user"
	"slices"
	"sync"
	"time"
)

// ErrBadCredentials is the only failure a login form shows, so it never reveals whether
// the user exists.
var ErrBadCredentials = errors.New("wrong user name or password")

// ErrNotAllowed: the password was right but the account may not manage VMs.
var ErrNotAllowed = errors.New("this account is not allowed to manage virtual machines")

// Authenticator checks a user name and password.
type Authenticator interface {
	Authenticate(ctx context.Context, user, password string) error
}

// GroupCheck allows root and members of group.
func GroupCheck(group string) func(name string) bool {
	return func(name string) bool {
		u, err := user.Lookup(name)
		if err != nil {
			return false
		}
		if u.Uid == "0" {
			return true
		}
		g, err := user.LookupGroup(group)
		if err != nil {
			return false
		}
		ids, err := u.GroupIds()
		return err == nil && slices.Contains(ids, g.Gid)
	}
}

const (
	CookieName = "virtomir_session"
	// IdleTimeout ends a session nobody used for a while; MaxAge ends any session.
	IdleTimeout = 2 * time.Hour
	MaxAge      = 24 * time.Hour
)

type session struct {
	user     string
	created  time.Time
	lastSeen time.Time
}

// Manager owns sessions and the login rate limit. The zero value is not usable; use New.
type Manager struct {
	authn   Authenticator
	allowed func(user string) bool
	now     func() time.Time

	mu       sync.Mutex
	sessions map[string]*session
	failures map[string][]time.Time // remote IP → recent failed logins
}

func New(authn Authenticator, allowed func(string) bool) *Manager {
	return &Manager{
		authn: authn, allowed: allowed, now: time.Now,
		sessions: map[string]*session{}, failures: map[string][]time.Time{},
	}
}

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
	if err := m.authn.Authenticate(ctx, name, password); err != nil {
		m.fail(ip)
		if errors.Is(err, ErrBadCredentials) {
			return "", ErrBadCredentials
		}
		return "", err
	}
	if !m.allowed(name) {
		m.fail(ip)
		return "", ErrNotAllowed
	}
	token := newToken()
	now := m.now()
	m.mu.Lock()
	m.sessions[token] = &session{user: name, created: now, lastSeen: now}
	delete(m.failures, ip)
	m.mu.Unlock()
	return token, nil
}

// User returns the session's user and refreshes its idle timer.
func (m *Manager) User(token string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[token]
	if !ok {
		return "", false
	}
	now := m.now()
	if now.Sub(s.lastSeen) > IdleTimeout || now.Sub(s.created) > MaxAge {
		delete(m.sessions, token)
		return "", false
	}
	s.lastSeen = now
	return s.user, true
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

// Token returns the session token of a request ("" when none).
func Token(r *http.Request) string {
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
