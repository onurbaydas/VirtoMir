package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 TOTP is defined on HMAC-SHA1; every authenticator app uses it
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/onurbaydas/virtomir/internal/atomicfile"
)

// Two-factor sign-in with time-based one-time codes (RFC 6238: 30 s steps, 6 digits,
// HMAC-SHA1 — what Google Authenticator, Aegis, 1Password, … expect). It applies to sign-ins
// with a password (local and LDAP); single sign-on leaves it to the identity provider, and API
// tokens are secrets of their own.

const (
	totpStep   = 30 * time.Second
	totpDigits = 6
	// totpSkew accepts the code of the step before and after (clocks drift, people are slow).
	totpSkew      = 1
	recoveryCount = 10
)

// ErrBadCode: a wrong, expired or already used code.
var ErrBadCode = errors.New("wrong or expired code")

type twoFactorUser struct {
	Secret  string `json:"secret"` // base32
	Enabled bool   `json:"enabled"`
	// Recovery codes are kept as SHA-256 hex; each works once.
	Recovery []string  `json:"recovery,omitempty"`
	LastStep int64     `json:"lastStep,omitempty"` // a code is never accepted twice
	Since    time.Time `json:"since,omitzero"`
}

// TwoFactor keeps every user's TOTP secret in -state-dir/twofactor.json (0600).
type TwoFactor struct {
	path  string
	now   func() time.Time
	mu    sync.Mutex
	users map[string]*twoFactorUser
}

func OpenTwoFactor(path string) (*TwoFactor, error) {
	t := &TwoFactor{path: path, now: time.Now, users: map[string]*twoFactorUser{}}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return t, nil
	case err != nil:
		return nil, err
	}
	if err := json.Unmarshal(b, &t.users); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return t, nil
}

func (t *TwoFactor) saveLocked() error {
	b, err := json.MarshalIndent(t.users, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(t.path), 0o750); err != nil {
		return err
	}
	return atomicfile.Write(t.path, b, 0o600)
}

// Enabled reports whether the user must give a code when signing in.
func (t *TwoFactor) Enabled(user string) bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	u := t.users[user]
	return u != nil && u.Enabled
}

// TwoFactorStatus is what the account page shows.
type TwoFactorStatus struct {
	Enabled      bool      `json:"enabled"`
	Since        time.Time `json:"since,omitzero"`
	RecoveryLeft int       `json:"recoveryLeft"`
	PendingSetup bool      `json:"pendingSetup"`
}

func (t *TwoFactor) Status(user string) TwoFactorStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	u := t.users[user]
	if u == nil {
		return TwoFactorStatus{}
	}
	return TwoFactorStatus{Enabled: u.Enabled, Since: u.Since, RecoveryLeft: len(u.Recovery), PendingSetup: !u.Enabled && u.Secret != ""}
}

// Begin creates a new secret for the user (not active until Confirm). It returns the secret
// and the otpauth:// URI for the QR code. While 2FA is on, it cannot be replaced this way.
func (t *TwoFactor) Begin(user, issuer string) (secret, uri string, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if u := t.users[user]; u != nil && u.Enabled {
		return "", "", errors.New("two-factor sign-in is already on; turn it off first")
	}
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	secret = base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
	t.users[user] = &twoFactorUser{Secret: secret}
	if err := t.saveLocked(); err != nil {
		return "", "", err
	}
	return secret, OTPAuthURI(issuer, user, secret), nil
}

// OTPAuthURI is the key-uri format authenticator apps read from the QR code.
func OTPAuthURI(issuer, user, secret string) string {
	label := url.PathEscape(issuer + ":" + user)
	q := url.Values{"secret": {secret}, "issuer": {issuer}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// Confirm turns 2FA on once the user proves their app has the secret, and returns fresh
// recovery codes (shown once).
func (t *TwoFactor) Confirm(user, code string) ([]string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	u := t.users[user]
	if u == nil || u.Secret == "" || u.Enabled {
		return nil, errors.New("start the setup first")
	}
	step, ok := checkTOTP(u.Secret, code, t.now(), u.LastStep)
	if !ok {
		return nil, ErrBadCode
	}
	codes, hashes := newRecoveryCodes()
	prev := *u
	u.Enabled, u.LastStep, u.Recovery, u.Since = true, step, hashes, t.now().UTC()
	if err := t.saveLocked(); err != nil {
		*u = prev
		return nil, err
	}
	return codes, nil
}

// Verify checks a sign-in code: a TOTP code, or one of the recovery codes (used up).
func (t *TwoFactor) Verify(user, code string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	u := t.users[user]
	if u == nil || !u.Enabled {
		return ErrBadCode
	}
	code = strings.TrimSpace(code)
	if step, ok := checkTOTP(u.Secret, code, t.now(), u.LastStep); ok {
		u.LastStep = step
		return t.saveLocked()
	}
	h := hashRecovery(code)
	for i, r := range u.Recovery {
		if subtle.ConstantTimeCompare([]byte(r), []byte(h)) == 1 {
			u.Recovery = append(u.Recovery[:i], u.Recovery[i+1:]...)
			return t.saveLocked()
		}
	}
	return ErrBadCode
}

// Disable turns 2FA off for the user (the caller checked who may do that).
func (t *TwoFactor) Disable(user string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.users[user]; !ok {
		return nil
	}
	delete(t.users, user)
	return t.saveLocked()
}

// Users lists who has 2FA on (admins see it next to their reset button).
func (t *TwoFactor) Users() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []string
	for name, u := range t.users {
		if u.Enabled {
			out = append(out, name)
		}
	}
	return out
}

// totpAt is the code for one time step (RFC 4226 dynamic truncation).
func totpAt(key []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, v%1_000_000)
}

// TOTPCode is the code an authenticator app shows for secret at time t (tests, tools).
func TOTPCode(secret string, t time.Time) (string, error) {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secret))
	if err != nil {
		return "", err
	}
	return totpAt(key, t.Unix()/int64(totpStep/time.Second)), nil
}

// checkTOTP accepts the code of the current step ±totpSkew, never one at or before lastStep.
func checkTOTP(secret, code string, now time.Time, lastStep int64) (int64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != totpDigits {
		return 0, false
	}
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secret))
	if err != nil {
		return 0, false
	}
	cur := now.Unix() / int64(totpStep/time.Second)
	for s := cur - totpSkew; s <= cur+totpSkew; s++ {
		if s <= lastStep {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(totpAt(key, s)), []byte(code)) == 1 {
			return s, true
		}
	}
	return 0, false
}

func newRecoveryCodes() (codes, hashes []string) {
	for range recoveryCount {
		b := make([]byte, 5)
		if _, err := rand.Read(b); err != nil {
			panic(err)
		}
		c := hex.EncodeToString(b) // 10 hex digits, shown as xxxxx-xxxxx
		c = c[:5] + "-" + c[5:]
		codes = append(codes, c)
		hashes = append(hashes, hashRecovery(c))
	}
	return codes, hashes
}

func hashRecovery(code string) string {
	c := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
	sum := sha256.Sum256([]byte(c))
	return hex.EncodeToString(sum[:])
}
