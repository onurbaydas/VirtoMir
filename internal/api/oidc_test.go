package api

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
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
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, cb)
		return rec
	}

	rec := signIn()
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" || len(rec.Result().Cookies()) == 0 {
		t.Fatalf("callback: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if r := do(h, "GET", "/api/v1/auth/session", "", rec.Result().Cookies()[0]); !strings.Contains(r.Body.String(), `"user":"alice@sso"`) || !strings.Contains(r.Body.String(), `"role":"operator"`) {
		t.Errorf("session: %s", r.Body)
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
