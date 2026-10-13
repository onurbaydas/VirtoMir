package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Token is an API token for scripts and tools (Terraform, CI, monitoring). Only a hash of
// the secret is stored; the secret is shown once, when the token is created.
type Token struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Owner    string     `json:"owner"`
	Role     Role       `json:"role"`
	Created  time.Time  `json:"created"`
	Expires  *time.Time `json:"expires,omitempty"`
	LastUsed *time.Time `json:"lastUsed,omitempty"`
	Hash     string     `json:"hash,omitempty"`
}

// TokenPrefix makes leaked tokens easy to recognize (e.g. by secret scanners).
const TokenPrefix = "vmt_"

var (
	ErrTokenRole = errors.New("a token cannot have more rights than its owner")
	errTokenName = errors.New("give the token a name (1–64 characters)")
)

// Tokens stores API tokens in a JSON file (0600).
type Tokens struct {
	path string
	now  func() time.Time

	mu     sync.Mutex
	tokens []Token
}

// OpenTokens loads path; a missing file means no tokens yet.
func OpenTokens(path string) (*Tokens, error) {
	t := &Tokens{path: path, now: time.Now}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return t, nil
	}
	if err != nil {
		return t, err
	}
	if err := json.Unmarshal(b, &t.tokens); err != nil {
		return t, fmt.Errorf("tokens file %s: %w", path, err)
	}
	return t, nil
}

func (t *Tokens) saveLocked() error {
	if t.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(t.tokens, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(t.path), 0o750); err != nil {
		return err
	}
	tmp := t.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, t.path)
}

func hashSecret(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// Create makes a token for owner. role may not exceed the owner's role.
func (t *Tokens) Create(owner Identity, name string, role Role, ttl time.Duration) (string, Token, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		return "", Token{}, errTokenName
	}
	if role == NoAccess || role > owner.Role {
		return "", Token{}, ErrTokenRole
	}
	id := randHex(6)
	secret := randHex(24)
	tok := Token{ID: id, Name: name, Owner: owner.Name, Role: role, Created: t.now().UTC(), Hash: hashSecret(secret)}
	if ttl > 0 {
		exp := tok.Created.Add(ttl)
		tok.Expires = &exp
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.tokens = append(t.tokens, tok)
	if err := t.saveLocked(); err != nil {
		t.tokens = t.tokens[:len(t.tokens)-1]
		return "", Token{}, err
	}
	tok.Hash = ""
	return TokenPrefix + id + "_" + secret, tok, nil
}

// Verify checks a presented token and returns its owner and role (without the secret hash).
func (t *Tokens) Verify(presented string) (Token, bool) {
	rest, ok := strings.CutPrefix(presented, TokenPrefix)
	if !ok {
		return Token{}, false
	}
	id, secret, ok := strings.Cut(rest, "_")
	if !ok {
		return Token{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := range t.tokens {
		tok := &t.tokens[i]
		if tok.ID != id {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(tok.Hash), []byte(hashSecret(secret))) != 1 {
			return Token{}, false
		}
		now := t.now().UTC()
		if tok.Expires != nil && now.After(*tok.Expires) {
			return Token{}, false
		}
		// Record use at most once a minute to keep disk writes rare.
		if tok.LastUsed == nil || now.Sub(*tok.LastUsed) > time.Minute {
			tok.LastUsed = &now
			_ = t.saveLocked()
		}
		out := *tok
		out.Hash = ""
		return out, true
	}
	return Token{}, false
}

// List returns the tokens of owner, or all tokens when owner is "".
func (t *Tokens) List(owner string) []Token {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := []Token{}
	for _, tok := range t.tokens {
		if owner == "" || tok.Owner == owner {
			tok.Hash = ""
			out = append(out, tok)
		}
	}
	return out
}

// Revoke deletes token id. Non-admins may only revoke their own tokens.
func (t *Tokens) Revoke(by Identity, id string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	i := slices.IndexFunc(t.tokens, func(tok Token) bool { return tok.ID == id })
	if i < 0 || (by.Role < Admin && t.tokens[i].Owner != by.Name) {
		return fmt.Errorf("no token %s", id)
	}
	removed := t.tokens[i]
	t.tokens = slices.Delete(t.tokens, i, i+1)
	if err := t.saveLocked(); err != nil {
		t.tokens = slices.Insert(t.tokens, i, removed)
		return err
	}
	return nil
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
