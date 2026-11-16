package api

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/onurbaydas/virtomir/internal/auth"
)

// fakeIdP is a minimal OpenID provider: discovery, JWKS, and a token endpoint that checks
// PKCE and signs an ID token for the user and groups the test chose.
type fakeIdP struct {
	srv    *httptest.Server
	key    *rsa.PrivateKey
	mu     sync.Mutex
	codes  map[string]authReq
	user   string
	groups []string
}

type authReq struct{ challenge, nonce string }

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	f := &fakeIdP{key: key, codes: map[string]authReq{}, user: "alice", groups: []string{"vm-ops"}}
	mux := http.NewServeMux()
	f.srv = httptest.NewTLSServer(mux)
	t.Cleanup(f.srv.Close)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": f.srv.URL, "authorization_endpoint": f.srv.URL + "/authorize", "token_endpoint": f.srv.URL + "/token",
			"jwks_uri": f.srv.URL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		req, ok := f.codes[r.Form.Get("code")]
		delete(f.codes, r.Form.Get("code"))
		f.mu.Unlock()
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != req.challenge {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		claims, _ := json.Marshal(map[string]any{
			"iss": f.srv.URL, "aud": "virtomir", "sub": "1234", "exp": time.Now().Add(time.Minute).Unix(), "iat": time.Now().Unix(),
			"nonce": req.nonce, "preferred_username": f.user, "groups": f.groups,
		})
		signer, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: "k1"}}, nil)
		jws, _ := signer.Sign(claims)
		idt, _ := jws.CompactSerialize()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "id_token": idt, "expires_in": 60})
	})
	return f
}

// authorize plays the browser + provider: it reads the start redirect and issues a code.
func (f *fakeIdP) authorize(t *testing.T, location string) (state, code string) {
	t.Helper()
	u, err := url.Parse(location)
	if err != nil || !strings.HasPrefix(location, f.srv.URL+"/authorize") {
		t.Fatalf("start redirect = %q", location)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("nonce") == "" || q.Get("client_id") != "virtomir" {
		t.Fatalf("authorize request lacks PKCE/nonce/client: %s", u.RawQuery)
	}
	code = randHex(8)
	f.mu.Lock()
	f.codes[code] = authReq{challenge: q.Get("code_challenge"), nonce: q.Get("nonce")}
	f.mu.Unlock()
	return q.Get("state"), code
}

func TestOIDCSignIn(t *testing.T) {
	idp := newFakeIdP(t)
	ext, err := auth.OpenExternal(filepath.Join(t.TempDir(), "signin.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ext.SetConfig(auth.ExternalConfig{OIDC: auth.OIDCConfig{Enabled: true, Issuer: idp.srv.URL, ClientID: "virtomir", ClientSecret: "s",
		UsernameClaim: "preferred_username", GroupsClaim: "groups", Roles: auth.GroupRoles{Operator: []string{"vm-ops"}}}}); err != nil {
		t.Fatal(err)
	}
	m := auth.New(stubAuthn{}, func(string) auth.Role { return auth.NoAccess })
	m.SetExternal(ext)
	s := NewServer(Options{Backend: &fakeBackend{}, Live: &fakeLive{}, Auth: m, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	s.oidcClient = idp.srv.Client()
	h := s.Handler()

	signIn := func() *httptest.ResponseRecorder {
		start := httptest.NewRequest("GET", "https://virtomir.test/api/v1/auth/oidc/start", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, start)
		state, code := idp.authorize(t, rec.Header().Get("Location"))
		cb := httptest.NewRequest("GET", "https://virtomir.test/api/v1/auth/oidc/callback?state="+state+"&code="+code, nil)
		for _, c := range rec.Result().Cookies() { // the browser that started the sign-in
			cb.AddCookie(c)
		}
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, cb)
		return rec
	}
	sessionOf := func(rec *httptest.ResponseRecorder) *http.Cookie {
		for _, c := range rec.Result().Cookies() {
			if c.Name == auth.CookieName && c.Value != "" {
				return c
			}
		}
		return nil
	}

	rec := signIn()
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" || sessionOf(rec) == nil {
		t.Fatalf("callback: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if r := do(h, "GET", "/api/v1/auth/session", "", sessionOf(rec)); !strings.Contains(r.Body.String(), `"user":"alice@sso"`) || !strings.Contains(r.Body.String(), `"role":"operator"`) {
		t.Errorf("session: %s", r.Body)
	}

	// A sign-in is finished by the browser that started it. An attacker who signs in at the
	// provider themselves and hands the way back (state and code) to someone else gets
	// nowhere: that browser has no cookie for it, and is not signed in as the attacker.
	{
		start := httptest.NewRequest("GET", "https://virtomir.test/api/v1/auth/oidc/start", nil)
		started := httptest.NewRecorder()
		h.ServeHTTP(started, start)
		var flow *http.Cookie
		for _, c := range started.Result().Cookies() {
			if c.Name == oidcCookie {
				flow = c
			}
		}
		if flow == nil || !flow.HttpOnly || !flow.Secure || flow.SameSite != http.SameSiteLaxMode {
			t.Fatalf("the sign-in cookie: %+v", flow)
		}
		state, code := idp.authorize(t, started.Header().Get("Location"))
		link := "https://virtomir.test/api/v1/auth/oidc/callback?state=" + state + "&code=" + code
		for name, cookie := range map[string]*http.Cookie{"no cookie": nil, "another sign-in's cookie": {Name: oidcCookie, Value: "0123456789abcdef0123456789abcdef"}} {
			victim := httptest.NewRequest("GET", link, nil)
			if cookie != nil {
				victim.AddCookie(cookie)
			}
			got := httptest.NewRecorder()
			h.ServeHTTP(got, victim)
			if sessionOf(got) != nil || !strings.Contains(got.Header().Get("Location"), "signin_error=") {
				t.Errorf("%s: finished someone else's sign-in: %s", name, got.Header().Get("Location"))
			}
		}
		// It was not used up by those tries: the browser that started it still finishes it.
		own := httptest.NewRequest("GET", link, nil)
		own.AddCookie(flow)
		got := httptest.NewRecorder()
		h.ServeHTTP(got, own)
		if sessionOf(got) == nil {
			t.Errorf("the browser that started the sign-in could not finish it: %s", got.Header().Get("Location"))
		}
	}

	// A user whose groups map to no role is turned away with a reason.
	idp.user, idp.groups = "mallory", []string{"guests"}
	rec = signIn()
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "signin_error=") || !strings.Contains(loc, "no+VirtoMir+role") {
		t.Errorf("no role: %s", loc)
	}

	// Unknown or replayed state is refused.
	cb := httptest.NewRequest("GET", "https://virtomir.test/api/v1/auth/oidc/callback?state=nope&code=x", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, cb)
	if !strings.Contains(rec.Header().Get("Location"), "signin_error=") {
		t.Errorf("bad state: %s", rec.Header().Get("Location"))
	}

	// The sign-in page learns that SSO is offered.
	if r := do(h, "GET", "/api/v1/auth/methods", "", nil); !strings.Contains(r.Body.String(), `"oidc":true`) {
		t.Errorf("methods: %s", r.Body)
	}
	_ = context.Background()
}

// Sign-ins in progress are kept for ten minutes, and there is an end to how many.
func TestOIDCFlowsAreBounded(t *testing.T) {
	var f oidcFlows
	soon := time.Now().Add(10 * time.Minute)
	for i := 0; i < oidcMaxFlows; i++ {
		if !f.put(fmt.Sprintf("s%d", i), oidcPending{expires: soon}) {
			t.Fatalf("refused sign-in %d", i)
		}
	}
	if f.put("one-too-many", oidcPending{expires: soon}) || len(f.m) != oidcMaxFlows {
		t.Errorf("%d sign-ins in progress", len(f.m))
	}
	if _, ok := f.take("s7"); !ok {
		t.Error("a sign-in in progress was lost")
	}
	if !f.put("after-one-finished", oidcPending{expires: soon}) {
		t.Error("no room after one finished")
	}
	for k, p := range f.m { // they all run out
		p.expires = time.Now().Add(-time.Second)
		f.m[k] = p
	}
	if !f.put("fresh", oidcPending{expires: soon}) || len(f.m) != 1 {
		t.Errorf("%d kept after they ran out", len(f.m))
	}
}
