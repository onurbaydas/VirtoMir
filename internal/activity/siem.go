package activity

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/onurbaydas/virtomir/internal/atomicfile"
)

// SIEMConfig configures remote syslog / SIEM log forwarding.
type SIEMConfig struct {
	Enabled     bool   `json:"enabled"`
	Target      string `json:"target"`      // e.g. "udp://siem.example.com:514", "tcp://...", "tls://..."
	Facility    int    `json:"facility"`    // 1 = user-level, 16 = local0
	Tag         string `json:"tag"`         // e.g. "virtomir"
	Hostname    string `json:"hostname"`    // host name or empty for os.Hostname
	TLSInsecure bool   `json:"tlsInsecure"` // skip cert verification if self-signed
	// LastError is the outcome of the latest delivery ("" when it worked); set on reads only.
	LastError string `json:"lastError,omitempty"`
}

// withDefaults fills in the facility, tag and host name the receiver sees.
func (c SIEMConfig) withDefaults() SIEMConfig {
	if c.Facility <= 0 {
		c.Facility = 1 // user-level
	}
	if c.Tag == "" {
		c.Tag = "virtomir"
	}
	if c.Hostname == "" {
		if h, err := os.Hostname(); err == nil {
			c.Hostname = h
		} else {
			c.Hostname = "localhost"
		}
	}
	c.LastError = ""
	return c
}

// Validate checks the target is udp://, tcp:// or tls:// with a host and port.
func (c SIEMConfig) Validate() error {
	if c.Facility < 0 || c.Facility > 23 {
		return fmt.Errorf("facility must be between 0 and 23")
	}
	if !c.Enabled && c.Target == "" {
		return nil
	}
	u, err := url.Parse(c.Target)
	if err != nil || (u.Scheme != "udp" && u.Scheme != "tcp" && u.Scheme != "tls") {
		return fmt.Errorf("target must look like udp://host:514, tcp://host:514 or tls://host:6514")
	}
	if _, port, err := net.SplitHostPort(u.Host); err != nil || port == "" {
		return fmt.Errorf("target %q needs a host and a port", c.Target)
	}
	return nil
}

// LoadSIEMConfig reads the saved forwarding settings; ok is false when none were saved.
func LoadSIEMConfig(path string) (cfg SIEMConfig, ok bool, err error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return SIEMConfig{}, false, nil
	}
	if err != nil {
		return SIEMConfig{}, false, err
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return SIEMConfig{}, false, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, true, nil
}

// SaveSIEMConfig writes the settings so forwarding survives a restart.
func SaveSIEMConfig(path string, cfg SIEMConfig) error {
	cfg.LastError = ""
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path, raw, 0o600)
}

// Forwarder streams audit logs to a remote syslog / SIEM receiver.
type Forwarder struct {
	mu      sync.RWMutex
	cfg     SIEMConfig
	lastErr string
	queue   chan Entry
	closed  chan struct{}
}

// NewForwarder initializes a background SIEM forwarder.
func NewForwarder(cfg SIEMConfig) *Forwarder {
	f := &Forwarder{
		cfg:    cfg.withDefaults(),
		queue:  make(chan Entry, 1000),
		closed: make(chan struct{}),
	}
	go f.worker()
	return f
}

// Send queues an entry for remote SIEM forwarding without blocking.
func (f *Forwarder) Send(e Entry) {
	if f == nil {
		return
	}
	f.mu.RLock()
	enabled := f.cfg.Enabled && f.cfg.Target != ""
	f.mu.RUnlock()

	if !enabled {
		return
	}

	select {
	case f.queue <- e:
	default:
		// Queue full, drop rather than blocking core API execution
	}
}

// SetConfig updates the SIEM forwarding configuration.
func (f *Forwarder) SetConfig(cfg SIEMConfig) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cfg, f.lastErr = cfg.withDefaults(), ""
}

// Config returns a copy of current SIEM configuration.
func (f *Forwarder) Config() SIEMConfig {
	if f == nil {
		return SIEMConfig{}
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	c := f.cfg
	c.LastError = f.lastErr
	return c
}

// Close terminates the background forwarder worker.
func (f *Forwarder) Close() {
	if f == nil {
		return
	}
	close(f.closed)
}

// FormatRFC5424 formats an audit entry into a standard RFC 5424 syslog frame.
func FormatRFC5424(e Entry, cfg SIEMConfig) string {
	facility := cfg.Facility
	if facility <= 0 {
		facility = 1
	}
	severity := 6 // Informational
	if !e.OK() {
		severity = 3 // Error
	}
	pri := facility*8 + severity

	tag := cfg.Tag
	if tag == "" {
		tag = "virtomir"
	}
	hostname := cfg.Hostname
	if hostname == "" {
		hostname = "-"
	}

	ts := e.Time.UTC().Format("2006-01-02T15:04:05.000000Z07:00")
	if e.Time.IsZero() {
		ts = time.Now().UTC().Format("2006-01-02T15:04:05.000000Z07:00")
	}

	sd := fmt.Sprintf(`[audit@virtomir user="%s" remote="%s" method="%s" path="%s" status="%d"]`,
		escapeSD(e.User), escapeSD(e.Remote), escapeSD(e.Method), escapeSD(e.Path), e.Status)

	msg := fmt.Sprintf("%s %s by %s: %d", e.Method, e.Path, e.User, e.Status)
	if e.Error != "" {
		msg += fmt.Sprintf(" (%s)", e.Error)
	}

	return fmt.Sprintf("<%d>1 %s %s %s - audit %s %s\n", pri, ts, hostname, tag, sd, msg)
}

func escapeSD(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, `]`, `\]`)
	return s
}

func (f *Forwarder) worker() {
	for {
		select {
		case <-f.closed:
			return
		case e := <-f.queue:
			f.mu.RLock()
			cfg := f.cfg
			f.mu.RUnlock()

			if !cfg.Enabled || cfg.Target == "" {
				continue
			}

			msg := FormatRFC5424(e, cfg)
			err := sendToTarget(cfg.Target, []byte(msg), cfg.TLSInsecure)
			f.mu.Lock()
			f.lastErr = ""
			if err != nil {
				f.lastErr = err.Error()
			}
			f.mu.Unlock()
		}
	}
}

func sendToTarget(rawURL string, data []byte, tlsInsecure bool) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}

	switch u.Scheme {
	case "udp":
		conn, err := net.DialTimeout("udp", u.Host, 3*time.Second)
		if err != nil {
			return err
		}
		defer conn.Close()
		_, err = conn.Write(data)
		return err

	case "tcp":
		conn, err := net.DialTimeout("tcp", u.Host, 3*time.Second)
		if err != nil {
			return err
		}
		defer conn.Close()
		_, err = conn.Write(data)
		return err

	case "tls":
		conf := &tls.Config{
			InsecureSkipVerify: tlsInsecure,
		}
		conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 3 * time.Second}, "tcp", u.Host, conf)
		if err != nil {
			return err
		}
		defer conn.Close()
		_, err = conn.Write(data)
		return err

	default:
		return fmt.Errorf("unsupported SIEM transport scheme: %s", u.Scheme)
	}
}
