package api

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/onurbaydas/virtomir/internal/carve"
	"github.com/onurbaydas/virtomir/internal/storconn"
)

// The pool name of a connect request becomes a directory, a password file and a mount unit
// on the host. A name that is a path is refused before anything runs or is written.
func TestConnectStorageRefusesPoolNamesThatArePaths(t *testing.T) {
	top := t.TempDir()
	oldBase, oldUnit, oldCred, oldCarve := storconn.Base, storconn.UnitDir, storconn.CredDir, carve.Base
	storconn.Base, storconn.UnitDir, storconn.CredDir, carve.Base = filepath.Join(top, "srv"), filepath.Join(top, "units"), filepath.Join(top, "cred"), filepath.Join(top, "srv")
	t.Cleanup(func() {
		storconn.Base, storconn.UnitDir, storconn.CredDir, carve.Base = oldBase, oldUnit, oldCred, oldCarve
	})
	victim := filepath.Join(top, "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	a := newTestNode(t, "alpha", nil)
	rec := &cephRecorder{answers: map[string]string{"systemd-escape": "x.mount\n"}}
	a.api.cephRunner = rec
	admin := a.signIn(t, "onur")
	for _, body := range []string{
		`{"kind":"smb","server":"10.0.0.7","name":"vms","username":"onur","password":"s3cret","pool":"../victim"}`,
		`{"kind":"smb","server":"10.0.0.7","name":"vms","pool":".."}`,
		`{"kind":"smb","server":"10.0.0.7","name":"vms","pool":"a\nOptions=exec"}`,
		`{"kind":"nfs","server":"10.0.0.7","name":"/srv/vm","pool":"../../etc"}`,
		`{"kind":"lun","name":"/dev/mapper/mpatha","pool":"../x"}`,
		`{"kind":"iscsi","server":"10.0.0.7","name":"iqn.2003-01.org.linux-iscsi.lab:sn.5b1e","pool":"a/b"}`,
	} {
		if resp, out := admin.req("POST", "/api/v1/storage/connect", body); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s", body, resp.StatusCode, out)
		}
	}
	if len(rec.calls) != 0 || len(a.backend.calls) != 0 {
		t.Errorf("ran %q, backend %q", rec.calls, a.backend.calls)
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Errorf("a file outside the credentials directory was written: %q", b)
	}
	if es, _ := os.ReadDir(top); len(es) != 1 {
		t.Errorf("something was made: %v", es)
	}
}
