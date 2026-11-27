package alerts

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type seen struct {
	method, path, query string
	header              http.Header
	body                []byte
}

func recorder(t *testing.T) (*httptest.Server, func() seen) {
	t.Helper()
	var mu sync.Mutex
	var last seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		last = seen{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, r.Header.Clone(), b}
		mu.Unlock()
	}))
	t.Cleanup(srv.Close)
	return srv, func() seen { mu.Lock(); defer mu.Unlock(); return last }
}

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("body %s: %v", b, err)
	}
	return m
}

var firing = Event{Type: Firing, Alert: Alert{ID: "a1", Kind: "host.cpu", Severity: Critical, Subject: "web", Message: "CPU at 97%",
	Started: time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)}}

func resolvedEv() Event {
	ev := firing
	ev.Type = ResolvedEvent
	r := ev.Alert.Started.Add(time.Hour)
	ev.Alert.Resolved = &r
	return ev
}

func TestServiceRequests(t *testing.T) {
	srv, last := recorder(t)
	cases := []struct {
		ch    Channel
		ev    Event
		check func(t *testing.T, s seen)
	}{
		{Channel{Format: "mattermost"}, firing, func(t *testing.T, s seen) {
			if !strings.Contains(decode(t, s.body)["text"].(string), "CPU at 97%") {
				t.Errorf("body %s", s.body)
			}
		}},
		{Channel{Format: "rocketchat"}, firing, func(t *testing.T, s seen) {
			if !strings.Contains(decode(t, s.body)["text"].(string), "CRITICAL") {
				t.Errorf("body %s", s.body)
			}
		}},
		{Channel{Format: "googlechat"}, firing, func(t *testing.T, s seen) {
			if !strings.Contains(decode(t, s.body)["text"].(string), "web") {
				t.Errorf("body %s", s.body)
			}
		}},
		{Channel{Format: "teams"}, firing, func(t *testing.T, s seen) {
			if !strings.Contains(string(s.body), "application/vnd.microsoft.card.adaptive") || !strings.Contains(string(s.body), "CPU at 97%") {
				t.Errorf("body %s", s.body)
			}
		}},
		{Channel{Format: "telegram", Secret: "123:abc", Target: "-100200"}, Event{Type: Firing, Alert: Alert{Severity: Warning, Subject: "<b>", Message: "a & b"}}, func(t *testing.T, s seen) {
			m := decode(t, s.body)
			if s.path != "/bot123:abc/sendMessage" || m["chat_id"] != "-100200" || m["parse_mode"] != "HTML" {
				t.Errorf("%s %v", s.path, m)
			}
			if txt := m["text"].(string); !strings.Contains(txt, "&lt;b&gt;") || !strings.Contains(txt, "a &amp; b") {
				t.Errorf("text not escaped: %q", txt)
			}
		}},
		{Channel{Format: "matrix", Secret: "syt_tok", Target: "!room:example.org"}, firing, func(t *testing.T, s seen) {
			if s.method != http.MethodPut || !strings.HasPrefix(s.path, "/_matrix/client/v3/rooms/%21room:example.org/send/m.room.message/virtomir-") {
				t.Errorf("%s %s", s.method, s.path)
			}
			if s.header.Get("Authorization") != "Bearer syt_tok" || decode(t, s.body)["msgtype"] != "m.text" {
				t.Errorf("headers %v body %s", s.header, s.body)
			}
		}},
		{Channel{Format: "gotify", Secret: "AppTok"}, firing, func(t *testing.T, s seen) {
			m := decode(t, s.body)
			if s.path != "/message" || s.header.Get("X-Gotify-Key") != "AppTok" || m["priority"] != float64(8) {
				t.Errorf("%s %v %v", s.path, s.header, m)
			}
		}},
		{Channel{Format: "pushover", Secret: "apptoken", Target: "userkey"}, firing, func(t *testing.T, s seen) {
			m := decode(t, s.body)
			if s.path != "/1/messages.json" || m["token"] != "apptoken" || m["user"] != "userkey" || m["priority"] != float64(1) {
				t.Errorf("%s %v", s.path, m)
			}
		}},
		{Channel{Format: "pagerduty", Secret: "routing"}, firing, func(t *testing.T, s seen) {
			m := decode(t, s.body)
			p, _ := m["payload"].(map[string]any)
			if s.path != "/v2/enqueue" || m["routing_key"] != "routing" || m["event_action"] != "trigger" || p["severity"] != "critical" ||
				!strings.HasSuffix(m["dedup_key"].(string), "-a1") {
				t.Errorf("%s %v", s.path, m)
			}
		}},
		{Channel{Format: "pagerduty", Secret: "routing"}, resolvedEv(), func(t *testing.T, s seen) {
			m := decode(t, s.body)
			if m["event_action"] != "resolve" || !strings.HasSuffix(m["dedup_key"].(string), "-a1") {
				t.Errorf("%v", m)
			}
		}},
		{Channel{Format: "opsgenie", Secret: "genie"}, firing, func(t *testing.T, s seen) {
			m := decode(t, s.body)
			if s.path != "/v2/alerts" || s.header.Get("Authorization") != "GenieKey genie" || m["priority"] != "P1" || !strings.HasSuffix(m["alias"].(string), "-a1") {
				t.Errorf("%s %v %v", s.path, s.header, m)
			}
		}},
		{Channel{Format: "opsgenie", Secret: "genie"}, resolvedEv(), func(t *testing.T, s seen) {
			if !strings.HasPrefix(s.path, "/v2/alerts/virtomir-") || !strings.HasSuffix(s.path, "-a1/close") || s.query != "identifierType=alias" {
				t.Errorf("%s?%s", s.path, s.query)
			}
		}},
		{Channel{Format: "alertmanager"}, resolvedEv(), func(t *testing.T, s seen) {
			var list []map[string]any
			if err := json.Unmarshal(s.body, &list); err != nil || len(list) != 1 {
				t.Fatalf("body %s", s.body)
			}
			l, _ := list[0]["labels"].(map[string]any)
			if s.path != "/api/v2/alerts" || l["alertname"] != "VirtoMir host.cpu" || l["severity"] != "critical" ||
				list[0]["startsAt"] != "2026-11-27T10:00:00Z" || list[0]["endsAt"] != "2026-11-27T11:00:00Z" {
				t.Errorf("%s %v", s.path, list[0])
			}
		}},
	}
	for _, c := range cases {
		c.ch.Kind, c.ch.URL = "webhook", srv.URL
		if err := webhook(c.ch, c.ev); err != nil {
			t.Fatalf("%s: %v", c.ch.Format, err)
		}
		t.Run(c.ch.Format, func(t *testing.T) { c.check(t, last()) })
	}
}

func TestServiceSettings(t *testing.T) {
	base := Channel{Name: "x", Kind: "webhook", MinSeverity: Warning}
	for _, c := range []struct {
		ch   Channel
		want string // "" for valid
	}{
		{Channel{Format: "telegram", Target: "1"}, "bot token"},
		{Channel{Format: "telegram", Secret: "1:a"}, "chat ID"},
		{Channel{Format: "telegram", Secret: "1:a", Target: "1"}, ""},
		{Channel{Format: "pushover", Secret: "a"}, "user key"},
		{Channel{Format: "pagerduty", Secret: "k"}, ""},
		{Channel{Format: "matrix", Secret: "t", Target: "!r:x"}, "http(s) URL"},
		{Channel{Format: "gotify", URL: "https://push.example.org"}, "application token"},
		{Channel{Format: "carrier-pigeon", URL: "https://x"}, "format must be"},
	} {
		ch := base
		ch.Format, ch.URL, ch.Secret, ch.Target = c.ch.Format, c.ch.URL, c.ch.Secret, c.ch.Target
		ch.normalize()
		err := ch.validate()
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%s: err = %v, want %q", c.ch.Format, err, c.want)
		}
	}
	// Services with one address need none typed in.
	ch := Channel{Kind: "webhook", Format: "pagerduty"}
	ch.normalize()
	if ch.URL != "https://events.pagerduty.com" {
		t.Errorf("default URL = %q", ch.URL)
	}
}

func TestSecretsAreMaskedAndKept(t *testing.T) {
	e, err := Open(filepath.Join(t.TempDir(), "alerts.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	ch := Channel{Name: "tg", Kind: "webhook", Format: "telegram", Secret: "123:secret", Target: "42", MinSeverity: Warning, Enabled: true}
	cfg := e.Config()
	cfg.Channels = []Channel{ch}
	if err := e.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	shown := e.Config()
	if shown.Channels[0].Secret != maskedSecret {
		t.Fatalf("secret sent back: %q", shown.Channels[0].Secret)
	}
	// Saving what the browser got back keeps the stored secret.
	if err := e.SetConfig(shown); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	got := e.cfg.Channels[0].Secret
	e.mu.Unlock()
	if got != "123:secret" {
		t.Errorf("stored secret = %q", got)
	}
	// An unknown channel with a masked secret cannot borrow one.
	other := e.Config().Channels[0]
	other.ID = "someone-else"
	cfg = e.Config()
	cfg.Channels = []Channel{other}
	if err := e.SetConfig(cfg); err == nil {
		t.Error("a masked secret without a stored channel was accepted")
	}
}

func TestTelegramTokenNotInErrors(t *testing.T) {
	ch := Channel{Kind: "webhook", Format: "telegram", URL: "http://127.0.0.1:1", Secret: "123:topsecret", Target: "1"}
	err := webhook(ch, firing)
	if err == nil || strings.Contains(err.Error(), "topsecret") {
		t.Errorf("err = %v", err)
	}
}
