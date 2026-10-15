package api

import (
	"net/http"
	"testing"

	"github.com/onurbaydas/virtomir/internal/auth"
)

func TestExtraDeviceRoles(t *testing.T) {
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/v1/domains/u1/devices"},
		{"PUT", "/api/v1/domains/u1/qemu-args"},
		{"POST", "/api/v1/host/mdevs"},
		{"DELETE", "/api/v1/host/mdevs/4b20d080-1b54-4048-85b3-a6a62d165c01"},
	} {
		if got := requiredRole(c.method, c.path); got != auth.Admin {
			t.Errorf("%s %s = %s, want admin", c.method, c.path, got)
		}
	}

	h, _, _ := rbacServer(t)
	admin, op := signIn(t, h, "onur"), signIn(t, h, "op")
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/v1/domains/u1/devices", `{"kind":"watchdog"}`},
		{"PUT", "/api/v1/domains/u1/qemu-args", `{"args":["-S"]}`},
		{"POST", "/api/v1/host/mdevs", `{"parent":"0000:3b:00.0","type":"nvidia-63"}`},
	} {
		if rec := do(h, c.method, c.path, c.body, op); rec.Code != http.StatusForbidden {
			t.Errorf("operator %s %s: %d", c.method, c.path, rec.Code)
		}
		// The routes exist; the fake backend has no extra devices.
		if rec := do(h, c.method, c.path, c.body, admin); rec.Code != http.StatusNotImplemented {
			t.Errorf("admin %s %s: %d %s", c.method, c.path, rec.Code, rec.Body)
		}
	}
}
