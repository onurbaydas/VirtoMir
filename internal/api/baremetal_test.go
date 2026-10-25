package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onurbaydas/virtomir/internal/baremetal"
)

func TestBaremetalAPI(t *testing.T) {
	n := newTestNode(t, "alpha", nil)
	st, _ := baremetal.Open("")
	n.api.baremetal, n.api.stateDir = st, t.TempDir()
	t.Cleanup(func() {
		if n.api.netboot.srv != nil {
			_ = n.api.netboot.srv.Close()
		}
	})
	admin, view := n.signIn(t, "onur"), n.signIn(t, "view")

	host := `{"name":"hv3","mac":"52:54:00:aa:bb:01","password":"s3cret-pass","bmc":{"driver":"ipmi","endpoint":"10.0.0.103","username":"admin","password":"calvin"}}`
	if resp, _ := view.req("POST", "/api/v1/baremetal/hosts", host); resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer adds: %d", resp.StatusCode)
	}
	resp, b := admin.req("POST", "/api/v1/baremetal/hosts", host)
	if resp.StatusCode != http.StatusCreated || strings.Contains(b, "calvin") || strings.Contains(b, "$6$") {
		t.Fatalf("add: %d %s", resp.StatusCode, b)
	}
	var h baremetal.Host
	_ = json.Unmarshal([]byte(b), &h)
	stored, _ := st.Host(h.ID)
	if !strings.HasPrefix(stored.PasswordHash, "$6$") || stored.BMC.Password != "calvin" {
		t.Errorf("stored %+v", stored)
	}
	// An edit without passwords keeps them.
	if resp, _ := admin.req("PUT", "/api/v1/baremetal/hosts/"+h.ID, `{"name":"hv3","mac":"52:54:00:aa:bb:01","disk":"/dev/sda","bmc":{"driver":"ipmi","endpoint":"10.0.0.103","username":"admin"}}`); resp.StatusCode != http.StatusOK {
		t.Errorf("edit: %d", resp.StatusCode)
	}
	if stored, _ = st.Host(h.ID); stored.BMC.Password != "calvin" || stored.PasswordHash == "" || stored.Disk != "/dev/sda" {
		t.Errorf("after edit %+v", stored)
	}

	if resp, b := admin.req("POST", "/api/v1/baremetal/hosts/"+h.ID+"/install", ""); resp.StatusCode != http.StatusConflict || !strings.Contains(b, "network boot") {
		t.Errorf("install while off: %d %s", resp.StatusCode, b)
	}
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	if resp, b := admin.req("PUT", "/api/v1/baremetal/settings", fmt.Sprintf(`{"enabled":true,"address":"127.0.0.1","port":%d}`, port)); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("settings: %d %s", resp.StatusCode, b)
	}
	if resp, b := admin.req("POST", "/api/v1/baremetal/hosts/"+h.ID+"/install", ""); resp.StatusCode != http.StatusConflict || !strings.Contains(b, "installer") {
		t.Errorf("install without installer: %d %s", resp.StatusCode, b)
	}
	dir := filepath.Join(n.api.stateDir, "netboot", "debian13")
	_ = os.MkdirAll(dir, 0o755)
	for _, f := range []string{"linux", "initrd.gz"} {
		_ = os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644)
	}
	// The BMC answers through a fake ipmitool.
	n.api.fencer.Run = func(_ context.Context, _ []string, name string, args ...string) ([]byte, error) {
		return []byte("Chassis Power is on\n"), nil
	}
	resp, b = admin.req("POST", "/api/v1/baremetal/hosts/"+h.ID+"/install", "")
	if resp.StatusCode != http.StatusAccepted || !strings.Contains(b, `"state":"installing"`) || strings.Contains(b, "token") {
		t.Fatalf("install: %d %s", resp.StatusCode, b)
	}
	_, b = view.req("GET", "/api/v1/baremetal", "")
	if !strings.Contains(b, `"installerReady":true`) || !strings.Contains(b, `"listening":true`) || strings.Contains(b, "calvin") {
		t.Errorf("status %s", b)
	}
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/boot/ipxe?mac=52:54:00:aa:bb:01", port), nil)
	req.Header.Set("User-Agent", "iPXE/1.21.1")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	script, _ := io.ReadAll(res.Body)
	res.Body.Close()
	stored, _ = st.Host(h.ID)
	if !strings.Contains(string(script), "/boot/"+stored.Token+"/preseed.cfg") {
		t.Errorf("boot script %s", script)
	}
	if resp, _ := admin.req("POST", "/api/v1/baremetal/hosts/"+h.ID+"/cancel", ""); resp.StatusCode != http.StatusOK {
		t.Errorf("cancel: %d", resp.StatusCode)
	}
	if stored, _ = st.Host(h.ID); stored.State != baremetal.StateRegistered || stored.Token != "" {
		t.Errorf("after cancel %+v", stored)
	}
	if resp, _ := admin.req("POST", "/api/v1/baremetal/hosts/"+h.ID+"/power", `{"action":"melt"}`); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("bad power action: %d", resp.StatusCode)
	}
	if resp, b := admin.req("POST", "/api/v1/baremetal/hosts/"+h.ID+"/power", `{"action":"status"}`); resp.StatusCode != http.StatusOK || !strings.Contains(b, `"power":"on"`) {
		t.Errorf("power status: %d %s", resp.StatusCode, b)
	}
	if resp, _ := admin.req("DELETE", "/api/v1/baremetal/hosts/"+h.ID, ""); resp.StatusCode != http.StatusNoContent {
		t.Errorf("delete: %d", resp.StatusCode)
	}
}
