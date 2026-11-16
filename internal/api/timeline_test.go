package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/onurbaydas/virtomir/internal/confighist"
	"github.com/onurbaydas/virtomir/internal/timeline"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// timelineBackend is a backend with snapshots.
type timelineBackend struct {
	*fakeBackend
	snaps   []virt.Snapshot
	snapErr error
}

func (b timelineBackend) ListSnapshots(string) ([]virt.Snapshot, error) { return b.snaps, b.snapErr }

// One answer tells what happened to a machine: what was done through VirtoMir (with the
// version of the definition it made), what libvirt reported, its snapshots.
func TestMachineTimeline(t *testing.T) {
	a := newTestNode(t, "alpha", nil)
	src := &histSource{xml: "<domain><name>web</name></domain>"}
	hist, err := confighist.Open(t.TempDir(), src, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	tl, err := timeline.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.api.confighist, a.api.timeline = hist, tl
	backend := timelineBackend{fakeBackend: a.backend, snaps: []virt.Snapshot{{Name: "before-upgrade", Created: time.Now().Add(-time.Hour).Unix()}}}
	a.api.backend = backend
	hist.Sweep() // the first version VirtoMir saw
	admin, viewer := a.signIn(t, "onur"), a.signIn(t, "view")
	base := "/api/v1/domains/" + histUUID

	// onur changes the machine through the API, then it crashes.
	src.xml = "<domain><name>web</name><memory>2</memory></domain>"
	if resp, body := admin.req("POST", base+"/actions/start", ""); resp.StatusCode >= 300 {
		t.Fatalf("change: %d %s", resp.StatusCode, body)
	}
	if err := tl.Record(histUUID, "stopped", "crashed"); err != nil {
		t.Fatal(err)
	}

	resp, body := viewer.req("GET", base+"/timeline", "")
	var got struct {
		Events  []timeline.Event
		Missing []string
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &got) != nil {
		t.Fatalf("timeline: %d %s", resp.StatusCode, body)
	}
	var kinds []string
	for _, e := range got.Events {
		kinds = append(kinds, e.Kind+":"+e.What+e.Detail)
	}
	if strings.Join(kinds, " ") != "lifecycle:stoppedcrashed action: definition:first seen snapshot:before-upgrade" || len(got.Missing) != 0 {
		t.Fatalf("events %v, missing %v\n%s", kinds, got.Missing, body)
	}
	act := got.Events[1]
	versions, _ := hist.List(histUUID)
	if act.User != "onur" || act.Method != "POST" || !strings.HasSuffix(act.Path, "/actions/start") || act.Version != versions[0].ID {
		t.Errorf("the action: %+v (newest version %s)", act, versions[0].ID)
	}
	if strings.Contains(body, "remote") || strings.Contains(body, "127.0.0.1") {
		t.Errorf("the timeline tells where people signed in from: %s", body)
	}

	// A source that cannot be read is named, and the rest is still told.
	backend.snapErr = errors.New("libvirt is away")
	a.api.backend = backend
	_, body = viewer.req("GET", base+"/timeline?limit=2", "")
	got.Events, got.Missing = nil, nil
	if json.Unmarshal([]byte(body), &got) != nil || len(got.Events) != 2 || len(got.Missing) != 1 || got.Missing[0] != "snapshots" {
		t.Errorf("without snapshots: %s", body)
	}
	if resp, _ := viewer.req("GET", "/api/v1/domains/not-a-machine/timeline", ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("not a machine id: %d", resp.StatusCode)
	}
}
