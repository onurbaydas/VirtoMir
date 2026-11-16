package api

import (
	"crypto/sha256"
	"encoding/base64"
	"io/fs"
	"net/http"
	"regexp"
	"strings"
)

// Security headers for every answer, and the content security policy of the UI.
//
// The panel can start, stop and delete machines with one click, and shows names that come
// from elsewhere (guests, imported machines, other tools). Svelte escapes what it renders,
// and the session cookie is SameSite=Strict; these headers are the second line behind
// both: a page that cannot be framed, and a browser that runs no script but the UI's own.

// apiPolicy is for everything that is not the UI's document: JSON, downloads, streams. If a
// browser ever shows such an answer as a page, nothing in it runs or loads.
const apiPolicy = "default-src 'none'; frame-ancestors 'none'"

// reportPolicy is for the HTML reports VirtoMir writes itself: their own styles, no script.
const reportPolicy = "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'"

// secureHeaders sets what every answer carries. Handlers may narrow it (reportPolicy); the
// UI's document gets uiPolicy from spaHandler.
func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff") // an upload or a disk image is never run as script
		h.Set("X-Frame-Options", "DENY")           // no clickjacking; frame-ancestors for newer browsers
		h.Set("Referrer-Policy", "same-origin")    // machine IDs in the address stay here
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Content-Security-Policy", apiPolicy)
		next.ServeHTTP(w, r)
	})
}

var inlineScriptRE = regexp.MustCompile(`(?s)<script>(.*?)</script>`)

// uiPolicy is the content security policy of the UI's document: scripts only from this
// server, plus the inline ones index.html itself has (the theme before first paint), named
// by their hashes so nothing injected runs. Styles may be inline (Svelte sets style
// attributes); images and fonts may be data: or blob: (the console's screenshots, CSS).
func uiPolicy(ui fs.FS) string {
	scripts := "'self'"
	if index, err := fs.ReadFile(ui, "index.html"); err == nil {
		for _, m := range inlineScriptRE.FindAllSubmatch(index, -1) {
			sum := sha256.Sum256(m[1])
			scripts += " 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
		}
	}
	return strings.Join([]string{
		"default-src 'self'",
		"script-src " + scripts,
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data: blob:",
		"font-src 'self' data:",
		"connect-src 'self' %WS%", // the live stream and the consoles
		"worker-src 'self' blob:",
		"object-src 'none'",
		"base-uri 'self'",
		"form-action 'self'",
		"frame-ancestors 'none'",
	}, "; ")
}

// forHost fills in the web socket addresses of this very server: 'self' covers them in
// today's browsers, older ones want them spelled out.
func forHost(policy, host string) string {
	ws := ""
	if host != "" && !strings.ContainsAny(host, " ;,'\"") {
		ws = "ws://" + host + " wss://" + host
	}
	return strings.Replace(policy, " %WS%", strings.TrimRight(" "+ws, " "), 1)
}
