package api

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

const testIndex = `<!doctype html><html><head>
<script>
  document.documentElement.dataset.theme = 'dark'
</script>
</head><body><div id="app"></div><script type="module" src="/assets/index.js"></script></body></html>`

// Every answer carries the headers that keep the panel from being framed or sniffed, and
// what is not the UI's document can run and load nothing.
func TestSecureHeadersOnEveryAnswer(t *testing.T) {
	h := secureHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/report" {
			w.Header().Set("Content-Security-Policy", reportPolicy) // a handler may narrow it
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, path := range []string{"/api/v1/version", "/healthz", "/report"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		for k, want := range map[string]string{
			"X-Content-Type-Options":       "nosniff",
			"X-Frame-Options":              "DENY",
			"Referrer-Policy":              "same-origin",
			"Cross-Origin-Resource-Policy": "same-origin",
		} {
			if got := rec.Header().Get(k); got != want {
				t.Errorf("%s: %s = %q, want %q", path, k, got, want)
			}
		}
		csp := rec.Header().Get("Content-Security-Policy")
		if !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "frame-ancestors 'none'") {
			t.Errorf("%s: policy %q", path, csp)
		}
		if (path == "/report") != strings.Contains(csp, "style-src 'unsafe-inline'") {
			t.Errorf("%s: policy %q", path, csp)
		}
	}
}

// The UI's document may run its own scripts and the inline ones index.html has, by their
// hashes, and nothing else; client-side routes get the same policy.
func TestUIPolicyNamesItsInlineScripts(t *testing.T) {
	ui := fstest.MapFS{
		"index.html":      {Data: []byte(testIndex)},
		"assets/index.js": {Data: []byte("console.log(1)")},
	}
	h := secureHeaders(spaHandler(ui))
	sum := sha256.Sum256([]byte("\n  document.documentElement.dataset.theme = 'dark'\n"))
	hash := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	for _, path := range []string{"/", "/machines/2d860e5c", "/storage"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		req.Host = "virtomir.example:8420"
		h.ServeHTTP(rec, req)
		csp := rec.Header().Get("Content-Security-Policy")
		for _, want := range []string{
			"default-src 'self'", "script-src 'self' " + hash + ";", "object-src 'none'", "frame-ancestors 'none'", "base-uri 'self'",
			"connect-src 'self' ws://virtomir.example:8420 wss://virtomir.example:8420;",
		} {
			if !strings.Contains(csp, want) {
				t.Errorf("%s: policy lacks %q:\n%s", path, want, csp)
			}
		}
		for _, never := range []string{"'unsafe-eval'", "script-src 'self' 'unsafe-inline'", "%WS%", "*"} {
			if strings.Contains(csp, never) {
				t.Errorf("%s: policy has %q:\n%s", path, never, csp)
			}
		}
	}
	// A script of the UI is not a document: it keeps the strict policy.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/assets/index.js", nil))
	if csp := rec.Header().Get("Content-Security-Policy"); csp != apiPolicy || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("asset: %q", csp)
	}
}

// A Host header cannot write into the policy.
func TestForHostIgnoresAHostThatIsNotOne(t *testing.T) {
	p := "connect-src 'self' %WS%; object-src 'none'"
	if got := forHost(p, "h:1"); got != "connect-src 'self' ws://h:1 wss://h:1; object-src 'none'" {
		t.Errorf("%q", got)
	}
	for _, host := range []string{"", "evil; script-src *", "a b", "x'y", "a,b"} {
		if got := forHost(p, host); got != "connect-src 'self'; object-src 'none'" {
			t.Errorf("host %q: %q", host, got)
		}
	}
}
