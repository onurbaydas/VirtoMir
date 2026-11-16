// Package vmware reads the inventory of a VMware environment — a vCenter or a
// standalone ESXi host — so its machines can be migrated to VirtoMir (ADR-0061).
// Inventory is taken with govmomi; conversion (convert.go) executes the real
// virt-v2v binary — experimental, not tested on hardware.
//
// The vCenter/ESXi password stays on the host (<state-dir>/vmware.json, 0600) and never
// reaches a log: Connection's String method redacts it (slog and fmt print a Connection
// through it), and nothing in this package logs the password either. The API only ever
// shows that it is set.
package vmware

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sync"

	"github.com/onurbaydas/virtomir/internal/atomicfile"
)

var (
	ErrInvalid = errors.New("invalid")
	ErrRefused = errors.New("refused")
)

// classed is a sentence for the user that errors.Is matches to its class.
type classed struct {
	class error
	msg   string
}

func (e classed) Error() string        { return e.msg }
func (e classed) Is(target error) bool { return target == e.class }

func invalidf(format string, a ...any) error {
	return classed{ErrInvalid, fmt.Sprintf(format, a...)}
}

// Secret is what the API shows instead of the stored password; sending it back keeps it.
const Secret = "••••••••"

// Connection is how VirtoMir reaches a VMware environment: vCenter or standalone ESXi
// (ADR-0061).
type Connection struct {
	// URL is the HTTPS address of the vCenter or ESXi host, e.g. https://vcenter.example.com.
	URL string `json:"url,omitempty"`
	// Username signs in to vCenter/ESXi, e.g. administrator@vsphere.local or root.
	Username string `json:"username,omitempty"`
	// Password of that user; it never leaves the host and never reaches a log.
	Password string `json:"password,omitempty"`
	// Thumbprint pins the host's TLS certificate: its SHA-1 or SHA-256 fingerprint in
	// hex, with or without colons. With Insecure it takes the place of CA verification.
	Thumbprint string `json:"thumbprint,omitempty"`
	// Insecure skips CA verification, but only together with a thumbprint — the
	// connection is then pinned to exactly that certificate instead.
	Insecure bool `json:"insecure,omitempty"`
	// ESXi marks a standalone ESXi host rather than a vCenter; conversion then talks to
	// the host itself (esx://) instead of through vCenter (vpx://).
	ESXi bool `json:"esxi,omitempty"`
}

// String redacts the password, so a Connection printed with slog (or fmt) can never
// show it.
func (c Connection) String() string {
	p := c.Password
	if p != "" {
		p = Secret
	}
	return fmt.Sprintf("vmware.Connection{URL:%q Username:%q Password:%q Thumbprint:%q Insecure:%t ESXi:%t}",
		c.URL, c.Username, p, c.Thumbprint, c.Insecure, c.ESXi)
}

// Configured reports whether a connection is fully set.
func (c Connection) Configured() bool { return c.URL != "" && c.Password != "" }

var thumbRE = regexp.MustCompile(
	`^(?:[0-9a-fA-F]{2}:){19}[0-9a-fA-F]{2}$|^(?:[0-9a-fA-F]{2}:){31}[0-9a-fA-F]{2}$|^[0-9a-fA-F]{40}$|^[0-9a-fA-F]{64}$`)

// validate checks the form of a connection whose password is already resolved.
func (c Connection) validate() error {
	u, err := url.Parse(c.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.RawFragment != "" {
		return invalidf("The address is the HTTPS URL of the vCenter or ESXi host, e.g. https://vcenter.example.com — without user:password@, a path or a query.")
	}
	if c.Username == "" {
		return invalidf("Enter the user VirtoMir signs in as, e.g. administrator@vsphere.local or root.")
	}
	if c.Thumbprint != "" && !thumbRE.MatchString(c.Thumbprint) {
		return invalidf("The thumbprint is the host certificate's SHA-1 or SHA-256 fingerprint in hex, e.g. C9:8B:2A:…:A1.")
	}
	if c.Insecure && c.Thumbprint == "" {
		return invalidf("VirtoMir does not connect to a host it cannot verify; paste the host's certificate thumbprint instead — the connection is then pinned to exactly that certificate.")
	}
	return nil
}

// Store holds the connection settings in <state-dir>/vmware.json.
type Store struct {
	path string

	mu  sync.Mutex
	cfg Connection
}

// Open loads <path>; a missing file is an empty configuration, not an error.
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.cfg); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) current() Connection {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}

// Config returns the connection with the password replaced by Secret.
func (s *Store) Config() Connection {
	c := s.current()
	if c.Password != "" {
		c.Password = Secret
	}
	return c
}

// Connection returns the active connection settings, including the password.
// The password stays on the host and Connection.String redacts it.
func (s *Store) Connection() Connection {
	return s.current()
}

// Configured reports whether a connection is stored.
func (s *Store) Configured() bool { return s.current().Configured() }

// Validate checks a connection as SetConfig would, without storing it. It rejects an
// empty URL (SetConfig with an empty URL clears the configuration instead) and an
// empty password — including one sent as an empty string over a stored one.
func (s *Store) Validate(c Connection) error {
	if c.URL == "" {
		return invalidf("Enter the address of the vCenter or ESXi host, e.g. https://vcenter.example.com.")
	}
	if c.Password == Secret {
		c.Password = s.current().Password
	}
	if c.Password == "" {
		return invalidf("Enter the password of the vCenter or ESXi user.")
	}
	return c.validate()
}

// SetConfig stores the connection. A password sent back as Secret keeps its stored
// value; a connection with an empty URL clears the stored one entirely.
func (s *Store) SetConfig(c Connection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.URL != "" {
		if c.Password == Secret {
			c.Password = s.cfg.Password
		}
		if c.Password == "" {
			return invalidf("Enter the password of the vCenter or ESXi user.")
		}
		if err := c.validate(); err != nil {
			return err
		}
	} else {
		c = Connection{}
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	if err := atomicfile.Write(s.path, b, 0o600); err != nil {
		return err
	}
	s.cfg = c
	return nil
}
