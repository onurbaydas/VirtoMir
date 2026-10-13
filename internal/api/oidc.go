package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc" // Apache-2.0
	"golang.org/x/oauth2"               // BSD-3-Clause

	"github.com/onurbaydas/virtomir/internal/activity"
	"github.com/onurbaydas/virtomir/internal/auth"
)

// External sign-in endpoints (ADR-0023). start/callback are public (the guard lets
// /api/v1/auth/* through); the configuration is admin-only.
func (s *Server) externalAuthRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/auth/methods", s.authMethods)
	mux.HandleFunc("GET /api/v1/auth/oidc/start", s.oidcStart)
	mux.HandleFunc("GET /api/v1/auth/oidc/callback", s.oidcCallback)
	mux.HandleFunc("GET /api/v1/signin-config", s.getSigninConfig)
	mux.HandleFunc("PUT /api/v1/signin-config", s.putSigninConfig)
}

type oidcPending struct {
	verifier, nonce string
	expires         time.Time
}

// oidcFlows remembers sign-ins in progress (state → PKCE verifier and nonce), 10 minutes.
type oidcFlows struct {
	mu sync.Mutex
	m  map[string]oidcPending
}

func (f *oidcFlows) put(state string, p oidcPending) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.m == nil {
		f.m = map[string]oidcPending{}
	}
	now := time.Now()
	for k, v := range f.m {
		if now.After(v.expires) {
			delete(f.m, k)
		}
	}
	f.m[state] = p
}

func (f *oidcFlows) take(state string) (oidcPending, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.m[state]
	delete(f.m, state)
	return p, ok && time.Now().Before(p.expires)
}

func (s *Server) external() *auth.External {
	if s.auth == nil {
		return nil
	}
	return s.auth.External()
}

func (s *Server) authMethods(w http.ResponseWriter, _ *http.Request) {
	out := map[string]any{"ldap": false, "oidc": false}
	if e := s.external(); e != nil {
		ldapOn, oidcOn, label := e.Methods()
		out["ldap"], out["oidc"], out["oidcLabel"] = ldapOn, oidcOn, label
	}
	s.writeJSON(w, http.StatusOK, out)
}

// oidcSetup discovers the provider and builds the OAuth2 configuration for this host.
func (s *Server) oidcSetup(ctx context.Context, r *http.Request) (*oidc.Provider, *oauth2.Config, auth.OIDCConfig, error) {
	e := s.external()
	if e == nil {
		return nil, nil, auth.OIDCConfig{}, fmt.Errorf("single sign-on is not set up")
	}
	cfg := e.Config(false).OIDC
	if !cfg.Enabled {
		return nil, nil, cfg, fmt.Errorf("single sign-on is not set up")
	}
	if s.oidcClient != nil {
		ctx = oidc.ClientContext(ctx, s.oidcClient)
	}
	p, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, nil, cfg, fmt.Errorf("the identity provider %s does not answer: %w", cfg.Issuer, err)
	}
	return p, &oauth2.Config{
		ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, Endpoint: p.Endpoint(),
		RedirectURL: redirectURL(r),
		Scopes:      []string{oidc.ScopeOpenID, "profile", "email"},
	}, cfg, nil
}

// redirectURL is where the provider sends the browser back: this host as the user reached it.
func redirectURL(r *http.Request) string {
	return "https://" + r.Host + "/api/v1/auth/oidc/callback"
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// signinError sends the browser back to the sign-in page with a message.
func signinError(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/?signin_error="+url.QueryEscape(msg), http.StatusFound)
}

func (s *Server) oidcStart(w http.ResponseWriter, r *http.Request) {
	_, conf, _, err := s.oidcSetup(r.Context(), r)
	if err != nil {
		signinError(w, r, err.Error())
		return
	}
	state, nonce, verifier := randHex(16), randHex(16), oauth2.GenerateVerifier()
	s.oidc.put(state, oidcPending{verifier: verifier, nonce: nonce, expires: time.Now().Add(10 * time.Minute)})
	http.Redirect(w, r, conf.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}

// oidcCallback finishes the sign-in: exchanges the code (PKCE), verifies the ID token
// (signature, issuer, audience, expiry, nonce) and maps the user's groups to a role.
func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		signinError(w, r, "the identity provider refused: "+e+" "+q.Get("error_description"))
		return
	}
	pending, ok := s.oidc.take(q.Get("state"))
	if !ok {
		signinError(w, r, "this sign-in link expired; try again")
		return
	}
	ctx := r.Context()
	if s.oidcClient != nil {
		ctx = oidc.ClientContext(ctx, s.oidcClient)
	}
	p, conf, cfg, err := s.oidcSetup(ctx, r)
	if err != nil {
		signinError(w, r, err.Error())
		return
	}
	tok, err := conf.Exchange(ctx, q.Get("code"), oauth2.VerifierOption(pending.verifier))
	if err != nil {
		signinError(w, r, "the identity provider did not confirm the sign-in")
		s.log.Warn("oidc exchange", "err", err)
		return
	}
	raw, _ := tok.Extra("id_token").(string)
	idt, err := p.Verifier(&oidc.Config{ClientID: cfg.ClientID}).Verify(ctx, raw)
	if err != nil || idt.Nonce != pending.nonce {
		signinError(w, r, "the sign-in could not be verified")
		s.log.Warn("oidc verify", "err", err)
		return
	}
	var claims map[string]any
	if err := idt.Claims(&claims); err != nil {
		signinError(w, r, "the sign-in could not be read")
		return
	}
	user, _ := claims[orDefault(cfg.UsernameClaim, "preferred_username")].(string)
	if user == "" {
		user, _ = claims["email"].(string)
	}
	if user == "" {
		signinError(w, r, "the identity provider sent no user name ("+cfg.UsernameClaim+")")
		return
	}
	var groups []string
	if list, ok := claims[orDefault(cfg.GroupsClaim, "groups")].([]any); ok {
		for _, g := range list {
			if s, ok := g.(string); ok {
				groups = append(groups, s)
			}
		}
	}
	id := s.external().SSOIdentity(user, groups)
	token, err := s.auth.NewSession(id)
	s.record(activity.Entry{User: id.Name, Remote: r.RemoteAddr, Method: "POST", Path: "/api/v1/auth/login",
		Status: map[bool]int{true: http.StatusOK, false: http.StatusForbidden}[err == nil], Error: errString(err)})
	if err != nil {
		signinError(w, r, user+" has no VirtoMir role: none of their groups is mapped to one")
		return
	}
	auth.SetCookie(w, r, token)
	http.Redirect(w, r, "/", http.StatusFound)
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (s *Server) getSigninConfig(w http.ResponseWriter, r *http.Request) {
	e := s.external()
	if e == nil {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "sign-in is disabled on this host (-auth none)"})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"config": e.Config(true), "redirectUrl": redirectURL(r)})
}

func (s *Server) putSigninConfig(w http.ResponseWriter, r *http.Request) {
	e := s.external()
	if e == nil {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "sign-in is disabled on this host (-auth none)"})
		return
	}
	var c auth.ExternalConfig
	if !s.decode(w, r, &c) {
		return
	}
	if err := e.SetConfig(c); err != nil {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"config": e.Config(true), "redirectUrl": redirectURL(r)})
}
