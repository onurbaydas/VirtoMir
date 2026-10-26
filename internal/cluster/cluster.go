// Package cluster lets several virtomird instances act as one (ADR-0020). Every host keeps
// running a complete VirtoMir; the only shared state is the member list, which members push
// to each other. Peers talk HTTPS pinned to each other's certificate fingerprint and prove
// who they are with a per-node key (only its SHA-256 is shared).
package cluster

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

	"github.com/onurbaydas/virtomir/internal/atomicfile"
)

// Node is one member of the cluster.
type Node struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Address     string `json:"address"`     // https://host:port, as the other members reach it
	Fingerprint string `json:"fingerprint"` // SHA-256 of its TLS certificate, hex
	KeyHash     string `json:"keyHash"`     // SHA-256 of its peer key, hex
}

// state is what a node keeps in -state-dir/cluster.json.
type state struct {
	SelfID string `json:"selfId"`
	Key    string `json:"key"` // this node's peer key (secret)
	// Version grows with every membership change; older lists are ignored.
	Version int    `json:"version"`
	Members []Node `json:"members"` // empty while standalone
}

// InviteTTL is how long a join code works.
const InviteTTL = 15 * time.Minute

type Store struct {
	path string
	// self describes this node apart from its address (known once it joins or invites).
	name, fingerprint string

	mu      sync.Mutex
	st      state
	invites map[string]time.Time // secret → expiry
	now     func() time.Time
}

// Open loads the cluster state, creating this node's identity on first use.
func Open(path, name, fingerprint string) (*Store, error) {
	s := &Store{path: path, name: name, fingerprint: fingerprint, invites: map[string]time.Time{}, now: time.Now}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		s.st = state{SelfID: randomHex(8), Key: randomHex(32)}
		return s, s.save()
	case err != nil:
		return nil, err
	}
	if err := json.Unmarshal(raw, &s.st); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if s.st.SelfID == "" || s.st.Key == "" {
		return nil, fmt.Errorf("%s: missing node identity", path)
	}
	return s, nil
}

func (s *Store) save() error {
	b, err := json.MarshalIndent(s.st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o750); err != nil {
		return err
	}
	return atomicfile.Write(s.path, b, 0o600)
}

// SelfID is this node's stable ID.
func (s *Store) SelfID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.SelfID
}

// Key is this node's secret peer key, sent to peers in the Authorization header.
func (s *Store) Key() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.Key
}

// Self describes this node with the given address.
func (s *Store) Self(address string) Node {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selfLocked(address)
}

func (s *Store) selfLocked(address string) Node {
	return Node{ID: s.st.SelfID, Name: s.name, Address: address, Fingerprint: s.fingerprint, KeyHash: hashHex(s.st.Key)}
}

// Clustered reports whether this node has peers.
func (s *Store) Clustered() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.st.Members) > 1
}

// Members returns the member list (this node included) and its version.
func (s *Store) Members() ([]Node, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.st.Members), s.st.Version
}

// Member looks up a node by ID.
func (s *Store) Member(id string) (Node, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.st.Members, func(n Node) bool { return n.ID == id })
	if i < 0 {
		return Node{}, false
	}
	return s.st.Members[i], true
}

// VerifyPeer checks "<node id>.<key>" from a peer's Authorization header.
func (s *Store) VerifyPeer(credential string) (Node, bool) {
	id, key, ok := strings.Cut(credential, ".") // both parts are hex
	if !ok {
		return Node{}, false
	}
	n, ok := s.Member(id)
	if !ok || id == s.SelfID() {
		return Node{}, false
	}
	return n, subtle.ConstantTimeCompare([]byte(hashHex(key)), []byte(n.KeyHash)) == 1
}

// Invite creates a one-time secret for a join code.
func (s *Store) Invite() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for k, exp := range s.invites {
		if now.After(exp) {
			delete(s.invites, k)
		}
	}
	secret := randomHex(16)
	s.invites[secret] = now.Add(InviteTTL)
	return secret
}

// Admit uses up an invite secret and adds n. selfAddress is how n reached this node, which
// becomes this node's address if it had none yet (first join of a standalone host).
func (s *Store) Admit(secret string, n Node, selfAddress string) ([]Node, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.invites[secret]
	if !ok || s.now().After(exp) {
		return nil, 0, errors.New("the join code is unknown or expired; create a new one")
	}
	delete(s.invites, secret)
	if n.ID == s.st.SelfID {
		return nil, 0, errors.New("a host cannot join itself")
	}
	members := slices.Clone(s.st.Members)
	if len(members) == 0 {
		members = []Node{s.selfLocked(selfAddress)}
	}
	for _, m := range members {
		if m.Name == n.Name && m.ID != n.ID {
			return nil, 0, fmt.Errorf("a host named %s is already a member", n.Name)
		}
	}
	members = slices.DeleteFunc(members, func(m Node) bool { return m.ID == n.ID }) // rejoin replaces
	members = append(members, n)
	if err := s.setLocked(members, s.st.Version+1); err != nil {
		return nil, 0, err
	}
	return slices.Clone(members), s.st.Version, nil
}

// Replace installs a member list pushed by a peer, unless it is older than ours. A list
// without this node means it was removed: it becomes standalone again.
func (s *Store) Replace(members []Node, version int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if version <= s.st.Version {
		return nil
	}
	if len(members) < 2 || !slices.ContainsFunc(members, func(n Node) bool { return n.ID == s.st.SelfID }) {
		members = nil
	}
	return s.setLocked(members, version)
}

// Remove drops a member and returns the new list for pushing to the remaining members.
func (s *Store) Remove(id string) ([]Node, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !slices.ContainsFunc(s.st.Members, func(n Node) bool { return n.ID == id }) {
		return nil, 0, errors.New("no such member")
	}
	members := slices.DeleteFunc(slices.Clone(s.st.Members), func(n Node) bool { return n.ID == id })
	if id == s.st.SelfID || len(members) < 2 {
		members = nil // leaving, or the last peer left: standalone
	}
	if err := s.setLocked(members, s.st.Version+1); err != nil {
		return nil, 0, err
	}
	return slices.Clone(members), s.st.Version, nil
}

// Rejoin is called on a joining node with the list its new cluster sent back.
func (s *Store) Rejoin(members []Node, version int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setLocked(members, version)
}

func (s *Store) setLocked(members []Node, version int) error {
	prev := s.st
	s.st.Members, s.st.Version = members, version
	if err := s.save(); err != nil {
		s.st = prev
		return err
	}
	return nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return hex.EncodeToString(b)
}

func hashHex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
