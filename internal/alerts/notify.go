package alerts

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
)

type EventType string

const (
	Firing        EventType = "firing"
	ResolvedEvent EventType = "resolved"
	TestEvent     EventType = "test"
	// NoticeEvent is news that is not an alert: a scheduled report is ready, HA restarted a
	// machine. It goes to every enabled channel.
	NoticeEvent EventType = "notice"
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
	// Webhook: URL and body format (see formatSpecs). For services with one public address
	// (Telegram, Pushover, PagerDuty, Opsgenie) the URL may be left empty.
	URL    string `json:"url,omitempty"`
	Format string `json:"format,omitempty"`
	// Secret is the token or key a service needs (Telegram bot token, PagerDuty routing key, …);
	// like the URL it is never sent back to the browser.
	Secret string `json:"secret,omitempty"`
	// Target is where in the service the message goes (Telegram chat, Matrix room, Pushover user).
	Target string `json:"target,omitempty"`
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

// formatSpec is what a webhook format needs besides its URL.
type formatSpec struct {
	defaultURL string // used when the URL is left empty
	secret     string // name of the secret it needs ("" for none)
	target     string // name of the target it needs ("" for none)
}

var formatSpecs = map[string]formatSpec{
	"json": {}, "slack": {}, "discord": {}, "ntfy": {},
	"mattermost": {}, "rocketchat": {}, "googlechat": {}, "teams": {}, "alertmanager": {},
	"telegram":  {defaultURL: "https://api.telegram.org", secret: "bot token", target: "chat ID"},
	"matrix":    {secret: "access token", target: "room ID"},
	"gotify":    {secret: "application token"},
	"pushover":  {defaultURL: "https://api.pushover.net", secret: "application token", target: "user key"},
	"pagerduty": {defaultURL: "https://events.pagerduty.com", secret: "integration key"},
	"opsgenie":  {defaultURL: "https://api.opsgenie.com", secret: "API key"},
}

var formats = []string{"json", "slack", "discord", "ntfy", "mattermost", "rocketchat", "googlechat", "teams",
	"telegram", "matrix", "gotify", "pushover", "pagerduty", "opsgenie", "alertmanager"}

// normalize fills what VirtoMir knows itself: the address of a service that has only one.
func (c *Channel) normalize() {
	c.URL, c.Secret, c.Target = strings.TrimSpace(c.URL), strings.TrimSpace(c.Secret), strings.TrimSpace(c.Target)
	if c.Kind == "webhook" && c.URL == "" {
		c.URL = formatSpecs[c.Format].defaultURL
	}
}

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
		spec, ok := formatSpecs[c.Format]
		if !ok {
			return fmt.Errorf("format must be one of %s", strings.Join(formats, ", "))
		}
		if spec.secret != "" && c.Secret == "" {
			return fmt.Errorf("give the %s", spec.secret)
		}
		if spec.target != "" && c.Target == "" {
			return fmt.Errorf("give the %s", spec.target)
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
	return ev.Type == TestEvent || ev.Type == NoticeEvent || c.MinSeverity == Warning || ev.Alert.Severity == Critical
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

// Notice sends news (not an alert) to every enabled channel, in the background.
func (e *Engine) Notice(subject, message string) {
	if e == nil || e.notify == nil {
		return
	}
	e.mu.Lock()
	channels, smtp := slices.Clone(e.cfg.Channels), e.cfg.SMTP
	e.mu.Unlock()
	e.notify(Event{Type: NoticeEvent, channels: channels, smtp: smtp, Alert: Alert{
		ID: "notice", Kind: "notice", Severity: Warning, Subject: subject, Message: message, Started: time.Now().UTC(),
	}})
}

// Test sends a test notification through one channel and reports the outcome.
func (e *Engine) Test(ch Channel) error {
	e.mu.Lock()
	smtp := e.cfg.SMTP
	stored := slices.Clone(e.cfg.Channels)
	e.mu.Unlock()
	if err := unmaskChannel(&ch, stored); err != nil {
		return err
	}
	ch.normalize()
	if err := ch.validate(); err != nil {
		return err
	}
	ch.Enabled = true
	host, _ := os.Hostname()
	ev := Event{Type: TestEvent, Alert: Alert{
		ID: "test", Kind: "test", Severity: Warning, Subject: host, Started: time.Now().UTC(),
		Message: "Test notification from VirtoMir — if you can read this, the channel works.",
	}}
	if ch.Kind == "email" {
		// The addresses typed into an unsaved channel only receive the test mail; its To
		// header names no one, so nothing typed in the form is written into the message.
		return email(ch, smtp, ev, "undisclosed-recipients:;")
	}
	return webhook(ch, ev) // validate allows webhook or email only
}

// Text is the one-line summary used by chat formats and email subjects.
func (ev Event) Text() string {
	host, _ := os.Hostname()
	icon := map[EventType]string{Firing: "🔴", ResolvedEvent: "✅", TestEvent: "🔔", NoticeEvent: "ℹ️"}[ev.Type]
	if ev.Type == Firing && ev.Alert.Severity == Warning {
		icon = "🟠"
	}
	state := map[EventType]string{Firing: strings.ToUpper(string(ev.Alert.Severity)), ResolvedEvent: "RESOLVED", TestEvent: "TEST", NoticeEvent: "INFO"}[ev.Type]
	return fmt.Sprintf("%s [%s] %s — %s (VirtoMir on %s)", icon, state, ev.Alert.Subject, ev.Alert.Message, host)
}

var httpClient = &http.Client{Timeout: 10 * time.Second}

// webhookURL is an http(s) URL with a host and no spaces or control characters.
var webhookURL = regexp.MustCompile(`^https?://[^\s/?#]+(?:[/?#][^\s]*)?$`)

func deliver(ch Channel, s SMTP, ev Event) error {
	switch ch.Kind {
	case "webhook":
		return webhook(ch, ev)
	case "email":
		to, err := toHeader(ch.To)
		if err != nil {
			return err
		}
		return email(ch, s, ev, to)
	}
	return fmt.Errorf("unknown channel kind %s", ch.Kind)
}

func webhook(ch Channel, ev Event) error {
	method, target, contentType, body, headers := webhookRequest(ch, ev)
	if !webhookURL.MatchString(target) {
		return errors.New("webhook URL must be an http(s) URL")
	}
	req, err := http.NewRequestWithContext(context.Background(), method, target, bytes.NewReader(body))
	if err != nil {
		return redact(err, ch.Secret)
	}
	req.Header = headers
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "VirtoMir")
	resp, err := httpClient.Do(req)
	if err != nil {
		return redact(err, ch.Secret) // Telegram's token is part of its URL
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook answered %s", resp.Status)
	}
	return nil
}

// redact keeps a secret out of an error that may quote the request URL.
func redact(err error, secret string) error {
	if secret == "" || !strings.Contains(err.Error(), secret) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), secret, maskedSecret))
}

// webhookRequest builds the request each format expects.
func webhookRequest(ch Channel, ev Event) (method, target, contentType string, body []byte, headers http.Header) {
	method, target, contentType, headers = http.MethodPost, ch.URL, "application/json", http.Header{}
	base := strings.TrimRight(ch.URL, "/")
	host, _ := os.Hostname()
	title := fmt.Sprintf("VirtoMir: %s %s", ev.Alert.Subject, ev.Type)
	text := ev.Text()
	critical := ev.Type == Firing && ev.Alert.Severity == Critical
	switch ch.Format {
	case "slack", "mattermost", "rocketchat", "googlechat":
		body, _ = json.Marshal(map[string]string{"text": text})
	case "discord":
		body, _ = json.Marshal(map[string]string{"content": text})
	case "ntfy":
		// ntfy.sh: plain text body, metadata in headers.
		msg := ev.Alert.Message
		if ev.Type == ResolvedEvent {
			msg = "Resolved — " + msg // the message still describes the firing value
		}
		body, contentType = []byte(msg), "text/plain; charset=utf-8"
		headers.Set("Title", title)
		prio := map[Severity]string{Warning: "default", Critical: "urgent"}[ev.Alert.Severity]
		if ev.Type != Firing {
			prio = "low"
		}
		headers.Set("Priority", prio)
		headers.Set("Tags", map[EventType]string{Firing: "rotating_light", ResolvedEvent: "white_check_mark", TestEvent: "bell", NoticeEvent: "information_source"}[ev.Type])
	case "teams":
		// A Teams workflow ("Post to a channel when a webhook request is received") takes an
		// Adaptive Card.
		body, _ = json.Marshal(map[string]any{"type": "message", "attachments": []any{map[string]any{
			"contentType": "application/vnd.microsoft.card.adaptive",
			"content": map[string]any{
				"$schema": "http://adaptivecards.io/schemas/adaptive-card.json", "type": "AdaptiveCard", "version": "1.4",
				"body": []any{
					map[string]any{"type": "TextBlock", "text": title, "weight": "Bolder", "wrap": true},
					map[string]any{"type": "TextBlock", "text": text, "wrap": true},
				},
			},
		}}})
	case "telegram":
		target = base + "/bot" + ch.Secret + "/sendMessage"
		body, _ = json.Marshal(map[string]string{"chat_id": ch.Target, "text": html.EscapeString(text), "parse_mode": "HTML"})
	case "matrix":
		method = http.MethodPut
		target = base + "/_matrix/client/v3/rooms/" + url.PathEscape(ch.Target) + "/send/m.room.message/virtomir-" + newID()
		headers.Set("Authorization", "Bearer "+ch.Secret)
		body, _ = json.Marshal(map[string]string{"msgtype": "m.text", "body": text})
	case "gotify":
		target = base + "/message"
		headers.Set("X-Gotify-Key", ch.Secret)
		prio := 5
		if critical {
			prio = 8
		} else if ev.Type != Firing {
			prio = 2
		}
		body, _ = json.Marshal(map[string]any{"title": title, "message": text, "priority": prio})
	case "pushover":
		target = base + "/1/messages.json"
		prio := 0
		if critical {
			prio = 1
		} else if ev.Type != Firing {
			prio = -1
		}
		body, _ = json.Marshal(map[string]any{"token": ch.Secret, "user": ch.Target, "title": title, "message": text, "priority": prio})
	case "pagerduty":
		// Events API v2: the alert's ID ties its resolve to its trigger.
		target = base + "/v2/enqueue"
		ev2 := map[string]any{"routing_key": ch.Secret, "event_action": "trigger", "dedup_key": "virtomir-" + host + "-" + ev.Alert.ID}
		if ev.Type == ResolvedEvent {
			ev2["event_action"] = "resolve"
		} else {
			sev := map[Severity]string{Warning: "warning", Critical: "critical"}[ev.Alert.Severity]
			if ev.Type != Firing {
				sev = "info"
			}
			ev2["payload"] = map[string]any{"summary": text, "source": host, "severity": sev, "component": ev.Alert.Subject, "class": ev.Alert.Kind}
		}
		body, _ = json.Marshal(ev2)
	case "opsgenie":
		headers.Set("Authorization", "GenieKey "+ch.Secret)
		alias := "virtomir-" + host + "-" + ev.Alert.ID
		if ev.Type == ResolvedEvent {
			target = base + "/v2/alerts/" + url.PathEscape(alias) + "/close?identifierType=alias"
			body, _ = json.Marshal(map[string]string{"source": "VirtoMir", "note": text})
			break
		}
		prio := map[Severity]string{Warning: "P3", Critical: "P1"}[ev.Alert.Severity]
		if ev.Type != Firing {
			prio = "P5"
		}
		msg := title
		if len(msg) > 130 { // Opsgenie's limit for the message
			msg = msg[:130]
		}
		target = base + "/v2/alerts"
		body, _ = json.Marshal(map[string]any{"message": msg, "alias": alias, "description": text, "priority": prio, "source": "VirtoMir", "entity": ev.Alert.Subject})
	case "alertmanager":
		target = base + "/api/v2/alerts"
		a := map[string]any{
			"labels": map[string]string{"alertname": "VirtoMir " + string(ev.Alert.Kind), "severity": string(ev.Alert.Severity),
				"instance": host, "subject": ev.Alert.Subject, "alert_id": ev.Alert.ID},
			"annotations": map[string]string{"summary": text, "description": ev.Alert.Message},
			"startsAt":    ev.Alert.Started.UTC().Format(time.RFC3339),
		}
		if ev.Alert.Resolved != nil {
			a["endsAt"] = ev.Alert.Resolved.UTC().Format(time.RFC3339)
		}
		body, _ = json.Marshal([]any{a})
	default:
		body, _ = json.Marshal(map[string]any{"event": ev.Type, "alert": ev.Alert, "text": text})
	}
	return
}

// toHeader is the To header for recipients: the parsed addresses, re-encoded, so a line
// break typed into a recipient can never start a header of its own.
func toHeader(recipients string) (string, error) {
	to, err := mail.ParseAddressList(recipients)
	if err != nil {
		return "", err
	}
	out := make([]string, len(to))
	for i, r := range to {
		out[i] = r.String()
	}
	return strings.Join(out, ", "), nil
}

// email sends ev to the channel's recipients with toHdr as the message's To header.
func email(ch Channel, s SMTP, ev Event, toHdr string) error {
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
	from, err := mail.ParseAddress(s.From)
	if err != nil {
		return fmt.Errorf("sender: %w", err)
	}
	var msg bytes.Buffer
	fmt.Fprintf(&msg, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n",
		from.String(), toHdr, mimeHeader(ev.Text()), time.Now().Format(time.RFC1123Z))
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
