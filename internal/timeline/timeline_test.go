package timeline

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)

func at(sec int) time.Time { return t0.Add(time.Duration(sec) * time.Second) }

func action(sec int, user, method, path string, status int) Event {
	return Event{Time: at(sec), Kind: KindAction, User: user, Method: method, Path: "/api/v1/domains/u" + path, Status: status}
}

func line(e Event) string {
	s := e.Kind
	for _, f := range []string{e.What, e.Detail, e.User, e.Method, strings.TrimPrefix(e.Path, "/api/v1/domains/u"), e.Version, e.Ref} {
		if f != "" {
			s += " " + f
		}
	}
	return s
}

func lines(evs []Event) string {
	var out []string
	for _, e := range evs {
		out = append(out, line(e))
	}
	return strings.Join(out, "\n")
}

// A day in the life of a machine, as five sources tell it, comes out as one story: one line
// for one thing, newest first.
func TestFoldTellsOneStory(t *testing.T) {
	got := Fold([]Event{
		// onur added a disk: the action, then the definition and the running configuration it changed.
		action(100, "onur", "POST", "/disks", 201),
		{Time: at(101), Kind: KindDefinition, What: "virtomir", User: "onur", Version: "v2"},
		{Time: at(101), Kind: KindRunning, What: "virtomir", User: "onur", Version: "live-2"},
		// Someone used virsh.
		{Time: at(200), Kind: KindDefinition, What: "outside", Version: "v3"},
		// A snapshot onur took (its memory took 40 s to save) and one a schedule took.
		{Time: at(300), Kind: KindSnapshot, Detail: "before-upgrade", Ref: "before-upgrade"},
		action(340, "onur", "POST", "/snapshots", 201),
		{Time: at(400), Kind: KindSnapshot, Detail: "auto-20261116", Ref: "auto-20261116"},
		// A backup ayse started, and a failed try before it.
		action(495, "ayse", "POST", "/backup", 507),
		action(500, "ayse", "POST", "/backup", 202),
		{Time: at(501), Kind: KindBackup, Ref: "web/20261116-090821", SizeBytes: 1 << 30},
		// The pool filled up: paused by libvirt, an alert, resumed by onur.
		{Time: at(600), Kind: KindLifecycle, What: "paused", Detail: "ioerror"},
		{Time: at(605), Kind: KindAlert, What: "critical", Detail: "web is paused: its disk cannot be written", Ref: "a1"},
		action(700, "onur", "POST", "/actions/resume", 204),
		{Time: at(700), Kind: KindLifecycle, What: "resumed", Detail: "unpaused"},
		// The first thing ever known about it.
		{Time: at(0), Kind: KindDefinition, What: "first seen", Version: "v1"},
		{Time: at(1), Kind: KindRunning, What: "first seen", Version: "live-1"},
	}, 0)
	want := `action onur POST /actions/resume
lifecycle resumed unpaused
alert critical web is paused: its disk cannot be written a1
lifecycle paused ioerror
backup ayse web/20261116-090821
action ayse POST /backup
snapshot auto-20261116 auto-20261116
snapshot before-upgrade onur before-upgrade
definition outside v3
action onur POST /disks v2
definition first seen v1`
	if lines(got) != want {
		t.Errorf("timeline:\n%s\n\nwant:\n%s", lines(got), want)
	}
	if got[5].Status != 507 {
		t.Errorf("the failed try is not the one that stayed: %+v", got[5])
	}
}

// Each snapshot gets its own request, and a request is used once.
func TestFoldPairsEachRequestOnce(t *testing.T) {
	got := Fold([]Event{
		{Time: at(10), Kind: KindSnapshot, Ref: "a"},
		action(11, "onur", "POST", "/snapshots", 201),
		{Time: at(20), Kind: KindSnapshot, Ref: "b"},
		action(22, "ayse", "POST", "/snapshots", 201),
		{Time: at(30), Kind: KindSnapshot, Ref: "c"}, // nobody asked: a schedule
		action(40, "onur", "DELETE", "/snapshots/a", 204),
	}, 0)
	want := "action onur DELETE /snapshots/a\nsnapshot c\nsnapshot ayse b\nsnapshot onur a"
	if lines(got) != want {
		t.Errorf("timeline:\n%s\n\nwant:\n%s", lines(got), want)
	}
}

// Before libvirt's reports were kept, the configuration history still saw every start.
func TestFoldStartsFromBeforeReportsWereKept(t *testing.T) {
	got := Fold([]Event{
		{Time: at(10), Kind: KindRunning, What: "started", Version: "live-1"}, // no report: stands in
		{Time: at(500), Kind: KindLifecycle, What: "started", Detail: "booted"},
		{Time: at(503), Kind: KindRunning, What: "started", Version: "live-2"}, // reported: dropped
		{Time: at(900), Kind: KindRunning, What: "outside", Version: "live-3"}, // a hot-plug with virsh
	}, 0)
	want := "running outside live-3\nlifecycle started booted\nlifecycle started"
	if lines(got) != want {
		t.Errorf("timeline:\n%s\n\nwant:\n%s", lines(got), want)
	}
}

func TestFoldLimitsAndKeepsItsInput(t *testing.T) {
	var in []Event
	for i := 0; i < 50; i++ {
		in = append(in, action(i, "onur", "POST", "/actions/start", 204))
	}
	got := Fold(in, 10)
	if len(got) != 10 || !got[0].Time.Equal(at(49)) || !got[9].Time.Equal(at(40)) {
		t.Errorf("%d events, %v … %v", len(got), got[0].Time, got[len(got)-1].Time)
	}
	if !in[0].Time.Equal(at(0)) || len(Fold(nil, 5)) != 0 {
		t.Error("the input was reordered, or nothing became something")
	}
}

func TestStoreKeepsWhatHappened(t *testing.T) {
	const u = "2d860e5c-844a-4c8a-bd06-282d61d6ea52"
	s, err := Open(filepath.Join(t.TempDir(), "timeline"))
	if err != nil {
		t.Fatal(err)
	}
	now := t0
	s.now = func() time.Time { return now }
	rec := func(what, detail string) {
		t.Helper()
		if err := s.Record(u, what, detail); err != nil {
			t.Fatal(err)
		}
	}
	rec("started", "booted")
	now = now.Add(time.Hour)
	rec("crashed", "panicked")
	rec("crashed", "panicked") // libvirt said it twice
	rec("stopped", "crashed")
	rec("", "") // nothing to tell
	var got []string
	for _, o := range s.List(strings.ToUpper(u)) {
		got = append(got, o.What+"/"+o.Detail)
	}
	if strings.Join(got, " ") != "started/booted crashed/panicked stopped/crashed" {
		t.Errorf("kept %v", got)
	}

	// A crash loop is kept, a second apart at least, and never more than Keep.
	for i := 0; i < Keep+40; i++ {
		now = now.Add(time.Second)
		rec("started", "booted")
	}
	if list := s.List(u); len(list) != Keep || !list[len(list)-1].Time.Equal(now) {
		t.Errorf("%d kept", len(list))
	}

	// Another daemon on the same folder reads it; a torn file starts over instead of failing.
	again, _ := Open(s.dir)
	if len(again.List(u)) != Keep {
		t.Error("not read back")
	}
	if err := os.WriteFile(filepath.Join(s.dir, u+".json"), []byte(`[{"time":`), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := again.Record(u, "started", "booted"); err != nil || len(again.List(u)) != 1 {
		t.Errorf("after a torn file: %v, %d kept", err, len(again.List(u)))
	}

	for _, bad := range []string{"../../etc/passwd", "", "x", u + "/..", fmt.Sprintf("%s\x00", u)} {
		if err := s.Record(bad, "started", ""); err == nil || s.List(bad) != nil {
			t.Errorf("machine id %q was accepted", bad)
		}
	}
	if es, _ := os.ReadDir(s.dir); len(es) != 1 {
		t.Errorf("files: %v", es)
	}
}
