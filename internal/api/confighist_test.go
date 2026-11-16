package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/onurbaydas/virtomir/internal/confighist"
	"github.com/onurbaydas/virtomir/internal/virt"
)

type histSource struct{ xml string }

func (h *histSource) DomainUUIDs() ([]string, error)         { return []string{histUUID}, nil }
func (h *histSource) DomainXML(string, bool) (string, error) { return h.xml, nil }

const histUUID = "2d860e5c-844a-4c8a-bd06-282d61d6ea52"

// restoringBackend is a backend that restores like virt.Client does.
type restoringBackend struct {
	*fakeBackend
	missing []string
}

func (b restoringBackend) RestoreXML(uuid, x string) (virt.DefineResult, error) {
	b.call("restore " + uuid + " " + x)
	return virt.DefineResult{MissingDisks: b.missing}, b.err
}

// Going back to an earlier version restores (the present name kept, missing disks named)
// instead of defining the old XML as typed; a running configuration is never restored.
func TestRevertConfigVersionRestores(t *testing.T) {
	a := newTestNode(t, "alpha", nil)
	src := &histSource{xml: "<domain><name>old</name></domain>"}
	store, err := confighist.Open(t.TempDir(), src, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	a.api.confighist = store
	a.api.backend = restoringBackend{fakeBackend: a.backend, missing: []string{"/var/lib/libvirt/images/gone.qcow2"}}
	store.Sweep()
	vs, _ := store.List(histUUID)
	if len(vs) != 1 {
		t.Fatalf("versions: %+v", vs)
	}
	admin, viewer := a.signIn(t, "onur"), a.signIn(t, "view")
	path := "/api/v1/domains/" + histUUID + "/config-history/"

	if resp, _ := viewer.req("POST", path+vs[0].ID+"/revert", ""); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a viewer restored a version: %d", resp.StatusCode)
	}
	resp, body := admin.req("POST", path+vs[0].ID+"/revert", "")
	var res virt.DefineResult
	if resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &res) != nil || len(res.MissingDisks) != 1 {
		t.Fatalf("restore: %d %s", resp.StatusCode, body)
	}
	calls := strings.Join(a.backend.calls, "\n")
	if !strings.Contains(calls, "restore "+histUUID+" <domain><name>old</name></domain>") || strings.Contains(calls, "define ") {
		t.Errorf("backend calls:\n%s", calls)
	}
	for id, want := range map[string]int{"1": http.StatusNotFound, "live-1": http.StatusBadRequest, "..%2F..%2Fx": http.StatusNotFound} {
		if resp, body := admin.req("POST", path+id+"/revert", ""); resp.StatusCode != want {
			t.Errorf("revert %s: %d %s, want %d", id, resp.StatusCode, body, want)
		}
	}
}
