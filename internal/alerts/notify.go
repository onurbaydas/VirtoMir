package alerts

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"os"
	"strings"
	"time"
)

type EventType string

const (
	Firing        EventType = "firing"
	ResolvedEvent EventType = "resolved"
	TestEvent     EventType = "test"
)

// Event is one notification to deliver.
type Event struct {
	Type  EventType `json:"type"`
	Alert Alert     `json:"alert"`

	channels []Channel
	smtp     SMTP
}

// Channel is where notifications go.
type Channel struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"` // webhook or email
	Enabled bool   `json:"enabled"`
	// MinSeverity: "warning" sends everything, "critical" only critical alerts.
	MinSeverity Severity `json:"minSeverity"`
	// Webhook: URL and body format (json, slack, discord, ntfy).
	URL    string `json:"url,omitempty"`
	Format string `json:"format,omitempty"`
	// Email: recipients, comma separated.
	To string `json:"to,omitempty"`
	// Resolved also notifies when the alert clears.
	Resolved bool `json:"resolved"`
}

// SMTP is the mail server for email channels.
type SMTP struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	From     string `json:"from"`
	// TLS: "starttls" (port 587), "tls" (implicit, port 465) or "none" (local relay only).
	TLS string `json:"tls"`
}

var formats = []string{"json", "slack", "discord", "ntfy"}

func (c Channel) validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("give the channel a name")
	}
	if c.MinSeverity != Warning && c.MinSeverity != Critical {
		return errors.New("minimum severity must be warning or critical")
	}
	switch c.Kind {
	case "webhook":
		u, err := url.Parse(c.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return errors.New("webhook URL must be an http(s) URL")
		}
		ok := false
		for _, f := range formats {
			ok = ok || f == c.Format
		}
		if !ok {
			return fmt.Errorf("format must be one of %s", strings.Join(formats, ", "))
		}
	case "email":
		if _, err := mail.ParseAddressList(c.To); err != nil {
			return fmt.Errorf("recipients: %w", err)
		}
	default:
		return errors.New("kind must be webhook or email")
	}
	return nil
}

func (c Channel) wants(ev Event) bool {
	if !c.Enabled || (ev.Type == ResolvedEvent && !c.Resolved) {
		return false
	}
	return ev.Type == TestEvent || c.MinSeverity == Warning || ev.Alert.Severity == Critical
}

// send delivers an event to every channel that wants it (in the background).
func (e *Engine) send(ev Event) {
	for _, ch := range ev.channels {
		if !ch.wants(ev) {
			continue
		}
		go func() {
			if err := deliver(ch, ev.smtp, ev); err != nil && e.log != nil {
				e.log.Warn("alert notification failed", "channel", ch.Name, "err", err)
			}
		}()
	}
}

// Test sends a test notification through one channel and reports the outcome.
func (e *Engine) Test(ch Channel) error {
	if err := ch.validate(); err != nil {
		return err
	}
	e.mu.Lock()
	smtp := e.cfg.SMTP
	e.mu.Unlock()
	ch.Enabled = true
	host, _ := os.Hostname()
	return deliver(ch, smtp, Event{Type: TestEvent, Alert: Alert{
		ID: "test", Kind: "test", Severity: Warning, Subject: host, Started: time.Now().UTC(),
		Message: "Test notification from VirtoMir — if you can read this, the channel works.",
	}})
}

// Text is the one-line summary used by chat formats and email subjects.
func (ev Event) Text() string {
	host, _ := os.Hostname()
	icon := map[EventType]string{Firing: "🔴", ResolvedEvent: "✅", TestEvent: "🔔"}[ev.Type]
	if ev.Type == Firing && ev.Alert.Severity == Warning {
		icon = "🟠"
	}
	state := map[EventType]string{Firing: strings.ToUpper(string(ev.Alert.Severity)), ResolvedEvent: "RESOLVED", TestEvent: "TEST"}[ev.Type]
	return fmt.Sprintf("%s [%s] %s — %s (VirtoMir on %s)", icon, state, ev.Alert.Subject, ev.Alert.Message, host)
}

var httpClient = &http.Client{Timeout: 10 * time.Second}

func deliver(ch Channel, s SMTP, ev Event) error {
	switch ch.Kind {
	case "webhook":
		return webhook(ch, ev)
	case "email":
		return email(ch, s, ev)
	}
	return fmt.Errorf("unknown channel kind %s", ch.Kind)
}

func webhook(ch Channel, ev Event) error {
	var body []byte
	contentType := "application/json"
	headers := http.Header{}
	switch ch.Format {
	case "slack":
		body, _ = json.Marshal(map[string]string{"text": ev.Text()})
	case "discord":
		body, _ = json.Marshal(map[string]string{"content": ev.Text()})
	case "ntfy":
		// ntfy.sh: plain text body, metadata in headers.
		body, contentType = []byte(ev.Alert.Message), "text/plain; charset=utf-8"
		headers.Set("Title", fmt.Sprintf("VirtoMir: %s %s", ev.Alert.Subject, ev.Type))
		prio := map[Severity]string{Warning: "default", Critical: "urgent"}[ev.Alert.Severity]
		if ev.Type != Firing {
			prio = "low"
		}
		headers.Set("Priority", prio)
		headers.Set("Tags", map[EventType]string{Firing: "rotating_light", ResolvedEvent: "white_check_mark", TestEvent: "bell"}[ev.Type])
	default:
		body, _ = json.Marshal(map[string]any{"event": ev.Type, "alert": ev.Alert, "text": ev.Text()})
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, ch.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header = headers
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "VirtoMir")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook answered %s", resp.Status)
	}
	return nil
}

func email(ch Channel, s SMTP, ev Event) error {
	if s.Host == "" || s.From == "" {
		return errors.New("set up the mail server (SMTP) first")
	}
	to, err := mail.ParseAddressList(ch.To)
	if err != nil {
		return err
	}
	port := s.Port
	if port == 0 {
		port = map[string]int{"tls": 465, "starttls": 587, "none": 25}[s.TLS]
	}
	addr := net.JoinHostPort(s.Host, fmt.Sprint(port))
	var msg bytes.Buffer
	fmt.Fprintf(&msg, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n",
		s.From, ch.To, mimeHeader(ev.Text()), time.Now().Format(time.RFC1123Z))
	fmt.Fprintf(&msg, "%s\r\n\r\nSubject: %s\r\nSeverity: %s\r\nSince: %s\r\n", ev.Alert.Message, ev.Alert.Subject, ev.Alert.Severity, ev.Alert.Started.Local().Format(time.RFC1123))
	if ev.Alert.Resolved != nil {
		fmt.Fprintf(&msg, "Resolved: %s\r\n", ev.Alert.Resolved.Local().Format(time.RFC1123))
	}

	var c *smtp.Client
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	if s.TLS == "tls" {
		conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12})
		if err != nil {
			return err
		}
		c, err = smtp.NewClient(conn, s.Host)
		if err != nil {
			return err
		}
	} else {
		conn, err := dialer.Dial("tcp", addr)
		if err != nil {
			return err
		}
		c, err = smtp.NewClient(conn, s.Host)
		if err != nil {
			return err
		}
		if s.TLS == "starttls" {
			if err := c.StartTLS(&tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}); err != nil {
				c.Close()
				return err
			}
		}
	}
	defer c.Close()
	if s.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", s.Username, s.Password, s.Host)); err != nil {
			return err
		}
	}
	from, err := mail.ParseAddress(s.From)
	if err != nil {
		return fmt.Errorf("sender: %w", err)
	}
	if err := c.Mail(from.Address); err != nil {
		return err
	}
	for _, r := range to {
		if err := c.Rcpt(r.Address); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg.Bytes()); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// mimeHeader encodes non-ASCII subjects (emoji) as RFC 2047.
func mimeHeader(s string) string {
	for _, r := range s {
		if r > 127 {
			return "=?utf-8?q?" + qEncode(s) + "?="
		}
	}
	return s
}

func qEncode(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		switch {
		case c == ' ':
			b.WriteByte('_')
		case c >= '0' && c <= '9', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '-', c == '.':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "=%02X", c)
		}
	}
	return b.String()
}
