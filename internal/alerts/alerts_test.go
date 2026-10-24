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

var t0 = time.Unix(1_800_000_000, 0)

type sink struct {
	mu     sync.Mutex
	events []Event
}

func (s *sink) add(e Event) { s.mu.Lock(); s.events = append(s.events, e); s.mu.Unlock() }

func newEngine(t *testing.T) (*Engine, *sink) {
	t.Helper()
	e, err := Open("", nil)
	if err != nil {
		t.Fatal(err)
	}
	s := &sink{}
	e.notify = s.add
	return e, s
}

func obs(at time.Duration, cpu float64) Observation {
	return Observation{Time: t0.Add(at), Connected: true, HostCPU: cpu}
}

func TestFiresOnlyAfterDuration(t *testing.T) {
	e, s := newEngine(t) // host-cpu: > 90 % for 5 min
	e.Observe(obs(0, 95))
	e.Observe(obs(4*time.Minute, 95))
	if len(s.events) != 0 {
		t.Fatalf("fired too early: %+v", s.events)
	}
	e.Observe(obs(5*time.Minute, 96))
	if len(s.events) != 1 || s.events[0].Type != Firing || s.events[0].Alert.Subject != "Host" {
		t.Fatalf("events = %+v", s.events)
	}
	e.Observe(obs(6*time.Minute, 97)) // still firing: no duplicate
	if len(s.events) != 1 {
		t.Errorf("duplicate notification: %d", len(s.events))
	}
	e.Observe(obs(7*time.Minute, 20))
	if len(s.events) != 2 || s.events[1].Type != ResolvedEvent || s.events[1].Alert.Resolved == nil {
		t.Errorf("resolve = %+v", s.events)
	}
	if l := e.List(); len(l) != 1 || l[0].Resolved == nil {
		t.Errorf("list = %+v", l)
	}
}

func TestShortSpikeStaysQuiet(t *testing.T) {
	e, s := newEngine(t)
	e.Observe(obs(0, 99))
	e.Observe(obs(time.Minute, 10)) // back to normal: the timer resets
	e.Observe(obs(2*time.Minute, 99))
	e.Observe(obs(6*time.Minute, 99))
	if len(s.events) != 0 {
		t.Errorf("a spike that cleared must restart the timer: %+v", s.events)
	}
}

func TestMachineRules(t *testing.T) {
	e, s := newEngine(t)
	o := Observation{Time: t0, Connected: true, Machines: []Machine{
		{UUID: "a", Name: "web", State: "crashed"},
		{UUID: "b", Name: "db", State: "shutoff", Autostart: true},
		{UUID: "c", Name: "lab", State: "shutoff"},
	}}
	e.Observe(o)
	if len(s.events) != 1 || s.events[0].Alert.Kind != VMCrashed || s.events[0].Alert.Severity != Critical {
		t.Fatalf("crashed must fire at once: %+v", s.events)
	}
	o.Time = t0.Add(2 * time.Minute)
	e.Observe(o)
	if len(s.events) != 2 || s.events[1].Alert.Kind != VMDown || s.events[1].Alert.SubjectID != "b" {
		t.Errorf("autostart machine down: %+v", s.events)
	}
}

func TestLibvirtDownKeepsOtherAlerts(t *testing.T) {
	e, s := newEngine(t)
	crashed := Observation{Time: t0, Connected: true, Machines: []Machine{{UUID: "a", Name: "web", State: "crashed"}}}
	e.Observe(crashed)
	down := Observation{Time: t0.Add(time.Minute), Connected: false}
	e.Observe(down)
	e.Observe(Observation{Time: t0.Add(2*time.Minute + time.Second), Connected: false})
	var resolved, libvirt int
	for _, ev := range s.events {
		if ev.Type == ResolvedEvent {
			resolved++
		}
		if ev.Alert.Kind == LibvirtDown && ev.Type == Firing {
			libvirt++
		}
	}
	if resolved != 0 || libvirt != 1 {
		t.Errorf("resolved=%d libvirt=%d events=%+v", resolved, libvirt, s.events)
	}
}

func TestPoolRuleUsesLastPools(t *testing.T) {
	e, s := newEngine(t)
	e.Observe(Observation{Time: t0, Connected: true, PoolsFresh: true, Pools: []Pool{
		{Name: "default", Active: true, FreePct: 4, FreeBytes: 4 << 30, TotalBytes: 100 << 30},
		{Name: "big", Active: true, FreePct: 60, TotalBytes: 100 << 30},
	}})
	// Next round has no fresh pool data: the last known values still count (no flapping).
	e.Observe(Observation{Time: t0.Add(time.Second), Connected: true})
	if len(s.events) != 1 || !strings.Contains(s.events[0].Alert.Message, "4.0 GiB") {
		t.Errorf("events = %+v", s.events)
	}
}

func TestVMAnomaly(t *testing.T) {
	e, _ := Open("", nil)
	e.notify = func(Event) {}
	if e.ruleEnabled("vm-anomaly") {
		t.Fatal("the anomaly rule must be off by default")
	}
	cfg := e.Config()
	for i := range cfg.Rules {
		if cfg.Rules[i].ID == "vm-anomaly" {
			cfg.Rules[i].Enabled = true
		}
	}
	if err := e.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	odd := Observation{Time: t0, Connected: true, Machines: []Machine{{UUID: "a", Name: "web", State: "running", Anomaly: "web: disk.latency 120 (usually 10)"}}}
	e.Observe(odd)
	if l := e.List(); len(l) != 1 || l[0].Kind != VMAnomaly || l[0].Message != "web: disk.latency 120 (usually 10)" {
		t.Fatalf("alerts = %+v", l)
	}
	e.Observe(Observation{Time: t0.Add(time.Minute), Connected: true, Machines: []Machine{{UUID: "a", Name: "web", State: "running"}}})
	if l := e.List(); l[0].Resolved == nil {
		t.Error("anomaly over: the alert resolves")
	}
}

func TestAckAndConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alerts.json")
	e, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.notify = func(Event) {}
	e.Observe(Observation{Time: t0, Connected: true, Machines: []Machine{{UUID: "a", Name: "web", State: "crashed"}}})
	id := e.List()[0].ID
	if err := e.Ack(id, "onur"); err != nil {
		t.Fatal(err)
	}

	cfg := e.Config()
	cfg.SMTP = SMTP{Host: "mail", From: "v@x", Password: "secret", TLS: "starttls"}
	cfg.Channels = append(cfg.Channels, Channel{Name: "ops", Kind: "webhook", URL: "https://hooks.example/x", Format: "slack", MinSeverity: Warning, Enabled: true})
	if err := e.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if got := e.Config().SMTP.Password; got != maskedSecret {
		t.Errorf("password must be masked, got %q", got)
	}
	// Saving the masked config back keeps the real password.
	if err := e.SetConfig(e.Config()); err != nil {
		t.Fatal(err)
	}
	e2, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if e2.cfg.SMTP.Password != "secret" || len(e2.cfg.Channels) != 1 || e2.List()[0].AckedBy != "onur" {
		t.Errorf("not persisted: %+v %+v", e2.cfg, e2.List())
	}

	bad := e.Config()
	bad.Rules[0].Threshold = 150
	if err := e.SetConfig(bad); err == nil {
		t.Error("threshold 150 % must be refused")
	}
	bad = e.Config()
	bad.Channels = []Channel{{Name: "x", Kind: "webhook", URL: "ftp://nope", Format: "json", MinSeverity: Warning}}
	if err := e.SetConfig(bad); err == nil {
		t.Error("ftp webhook must be refused")
	}
}

func TestWebhookFormats(t *testing.T) {
	var got []map[string]string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		m := map[string]string{"ct": r.Header.Get("Content-Type"), "title": r.Header.Get("Title"), "body": string(b)}
		mu.Lock()
		got = append(got, m)
		mu.Unlock()
	}))
	defer srv.Close()
	ev := Event{Type: Firing, Alert: Alert{Severity: Critical, Subject: "web", Message: "web crashed"}}
	for _, f := range []string{"json", "slack", "discord", "ntfy"} {
		if err := webhook(Channel{URL: srv.URL, Format: f}, ev); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
	var slack, discord map[string]string
	_ = json.Unmarshal([]byte(got[1]["body"]), &slack)
	_ = json.Unmarshal([]byte(got[2]["body"]), &discord)
	if !strings.Contains(slack["text"], "web crashed") || !strings.Contains(discord["content"], "CRITICAL") {
		t.Errorf("slack=%v discord=%v", slack, discord)
	}
	if got[3]["body"] != "web crashed" || !strings.Contains(got[3]["title"], "web") || !strings.HasPrefix(got[3]["ct"], "text/plain") {
		t.Errorf("ntfy = %+v", got[3])
	}
	if !strings.Contains(got[0]["body"], `"event":"firing"`) {
		t.Errorf("json = %s", got[0]["body"])
	}
}

func TestChannelFilter(t *testing.T) {
	ch := Channel{Enabled: true, MinSeverity: Critical}
	if ch.wants(Event{Type: Firing, Alert: Alert{Severity: Warning}}) {
		t.Error("critical-only channel got a warning")
	}
	if !ch.wants(Event{Type: Firing, Alert: Alert{Severity: Critical}}) {
		t.Error("critical alert filtered")
	}
	if ch.wants(Event{Type: ResolvedEvent, Alert: Alert{Severity: Critical}}) {
		t.Error("resolved sent although the channel did not ask for it")
	}
}

// A webhook URL is the credential (Slack, Discord, ntfy topics): viewers read the
// configuration, so it is masked there, and sending the masked form back keeps the real one.
func TestWebhookURLsAreMasked(t *testing.T) {
	e, err := Open(filepath.Join(t.TempDir(), "alerts.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	const secret = "https://hooks.slack.com/services/T000/B000/XXXXSECRET"
	cfg := e.Config()
	cfg.Channels = []Channel{{Name: "ops", Kind: "webhook", URL: secret, Format: "slack", MinSeverity: Warning, Enabled: true}}
	if err := e.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	shown := e.Config().Channels[0].URL
	if strings.Contains(shown, "XXXXSECRET") || !strings.HasPrefix(shown, "https://hooks.slack.com/") {
		t.Fatalf("shown URL %q: want the host only", shown)
	}

	// Saved back unchanged (e.g. after editing a rule): the real URL stays.
	if err := e.SetConfig(e.Config()); err != nil {
		t.Fatal(err)
	}
	if e.cfg.Channels[0].URL != secret {
		t.Fatalf("stored URL became %q", e.cfg.Channels[0].URL)
	}

	// A new URL replaces it.
	c := e.Config()
	c.Channels[0].URL = "https://ntfy.sh/new-topic"
	if err := e.SetConfig(c); err != nil {
		t.Fatal(err)
	}
	if e.cfg.Channels[0].URL != "https://ntfy.sh/new-topic" {
		t.Fatalf("new URL not saved: %q", e.cfg.Channels[0].URL)
	}

	// A masked URL this host does not know (another cluster member's settings) is refused,
	// rather than saved as a broken webhook.
	other := e.Config()
	other.Channels[0].ID = "elsewhere"
	if err := e.SetConfig(other); err == nil {
		t.Error("an unknown masked URL was accepted")
	}
}

func TestTestChannelUsesStoredURL(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hook/secret-part" {
			hits++
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	e, err := Open(filepath.Join(t.TempDir(), "alerts.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := e.Config()
	cfg.Channels = []Channel{{Name: "hook", Kind: "webhook", URL: srv.URL + "/hook/secret-part", Format: "json", MinSeverity: Warning, Enabled: true}}
	if err := e.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	// The UI sends the channel as it shows it: masked.
	if err := e.Test(e.Config().Channels[0]); err != nil {
		t.Fatal(err)
	}
	if hits != 1 {
		t.Errorf("the test went to the masked URL, not the stored one (hits=%d)", hits)
	}
}

func TestCephHealth(t *testing.T) {
	e, s := newEngine(t) // ceph-health: not HEALTH_OK for 60 s
	at := func(d time.Duration, status, msg string) Observation {
		return Observation{Time: t0.Add(d), Connected: true, Ceph: &Ceph{Status: status, Message: msg}}
	}
	e.Observe(at(0, "HEALTH_WARN", "1 osds down"))
	e.Observe(Observation{Time: t0.Add(61 * time.Second), Connected: true}) // not read this round: still WARN
	if len(s.events) != 1 || s.events[0].Alert.Severity != Warning || s.events[0].Alert.Message != "Ceph health is HEALTH_WARN: 1 osds down" {
		t.Fatalf("events %+v", s.events)
	}
	e.Observe(at(2*time.Minute, "HEALTH_ERR", "64 pgs inactive"))
	e.Observe(at(3*time.Minute+time.Second, "HEALTH_ERR", "64 pgs inactive"))
	if len(s.events) != 3 || s.events[1].Type != ResolvedEvent || s.events[2].Alert.Severity != Critical {
		t.Fatalf("escalation %+v", s.events)
	}
	e.Observe(at(4*time.Minute, "HEALTH_OK", ""))
	if len(s.events) != 4 || s.events[3].Type != ResolvedEvent {
		t.Errorf("resolution %+v", s.events)
	}
}
