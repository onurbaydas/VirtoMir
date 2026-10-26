package api

import (
	"net/http"
	"strings"
	"testing"
)

func TestSettingsVersionCheck(t *testing.T) {
	b := &fakeBackend{xml: "<domain><name>a</name></domain>"}
	h := newTestServer(b, &fakeLive{}).Handler()

	rec := do(h, "GET", "/api/v1/domains/u1/xml?inactive=true", "", nil)
	etag := rec.Header().Get("ETag")
	if etag == "" || strings.Trim(etag, `"`) != xmlVersion(b.xml) {
		t.Fatalf("ETag = %q", etag)
	}

	// Same version: saved. No version: saved (scripts).
	if rec := do(h, "PUT", "/api/v1/domains/u1/settings", `{"vcpus":2,"maxVcpus":2}`, nil, "If-Match", etag); rec.Code != http.StatusOK {
		t.Fatalf("current version: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "PUT", "/api/v1/domains/u1/settings", `{"vcpus":2,"maxVcpus":2}`, nil); rec.Code != http.StatusOK {
		t.Fatalf("no version: %d %s", rec.Code, rec.Body)
	}

	// Changed elsewhere in between: refused, nothing saved.
	b.xml = "<domain><name>a</name><vcpu>4</vcpu></domain>"
	b.calls = nil
	rec = do(h, "PUT", "/api/v1/domains/u1/settings", `{"vcpus":2,"maxVcpus":2}`, nil, "If-Match", etag)
	if rec.Code != http.StatusConflict || len(b.calls) != 0 || !strings.Contains(rec.Body.String(), "changed elsewhere") {
		t.Fatalf("stale version: %d %s calls %v", rec.Code, rec.Body, b.calls)
	}
	if rec := do(h, "PUT", "/api/v1/domains/u1/xml", b.xml, nil, "If-Match", etag); rec.Code != http.StatusConflict {
		t.Fatalf("stale XML save: %d %s", rec.Code, rec.Body)
	}
}
