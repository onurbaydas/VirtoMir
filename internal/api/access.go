package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/onurbaydas/virtomir/internal/activity"
	"github.com/onurbaydas/virtomir/internal/auth"
)

type ctxKey int

const identityKey ctxKey = 0

// identity is who made the request (always set behind guard; Admin "dev" without sign-in).
func identity(r *http.Request) auth.Identity {
	id, _ := r.Context().Value(identityKey).(auth.Identity)
	return id
}

// requiredRole is the least role a request needs. Reading is for viewers; running machines
// (power, console, media, snapshots) for operators; everything that creates, deletes or
// reconfigures is for admins.
func requiredRole(method, path string) auth.Role {
	switch {
	case path == "/api/v1/version" || strings.HasPrefix(path, "/api/v1/auth/"):
		return auth.NoAccess // public: the session probe and sign-in itself
	case strings.HasPrefix(path, "/api/v1/tokens"):
		return auth.Viewer // everyone manages their own tokens (handlers check ownership)
	case path == "/api/v1/activity":
		return auth.Admin
	case strings.Contains(path, "/console/"):
		return auth.Operator // a console is control over the machine
	case method == http.MethodGet || method == http.MethodHead:
		return auth.Viewer
	case operatorWrite(method, path):
		return auth.Operator
	}
	return auth.Admin
}

func operatorWrite(method, path string) bool {
	rest, ok := strings.CutPrefix(path, "/api/v1/domains/")
	if !ok {
		return strings.HasPrefix(path, "/api/v1/alerts/") && strings.HasSuffix(path, "/ack")
	}
	parts := strings.Split(rest, "/") // uuid, …
	switch {
	case len(parts) == 3 && parts[1] == "actions" && method == http.MethodPost:
		return true
	case len(parts) == 2 && parts[1] == "media" && method == http.MethodPut:
		return true
	case len(parts) == 2 && parts[1] == "snapshots" && method == http.MethodPost:
		return true
	case len(parts) == 4 && parts[1] == "snapshots" && parts[3] == "revert" && method == http.MethodPost:
		return true
	}
	return false
}

// guard authenticates (session cookie or API token), authorizes by role, rejects cross-site
// writes and records every change in the activity log.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		unsafe := r.Method != http.MethodGet && r.Method != http.MethodHead
		bearer, hasBearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		// Tokens are not sent by browsers automatically, so they cannot be used for CSRF.
		if unsafe && !hasBearer && !sameOrigin(r) {
			s.writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-site request refused"})
			return
		}
		if !strings.HasPrefix(path, "/api/") {
			next.ServeHTTP(w, r) // the UI itself
			return
		}

		need := requiredRole(r.Method, path)
		id := auth.Identity{Name: "dev", Role: auth.Admin}
		if s.auth != nil {
			var ok bool
			id, ok = s.authenticate(r, bearer, hasBearer)
			if !ok && need > auth.NoAccess {
				s.writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "sign in required"})
				return
			}
		}
		if id.Role < need {
			s.writeJSON(w, http.StatusForbidden, map[string]string{
				"error": "your role (" + id.Role.String() + ") may not do this; it needs " + need.String(),
			})
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), identityKey, id))

		if !unsafe || strings.HasPrefix(path, "/api/v1/auth/") {
			next.ServeHTTP(w, r)
			return
		}
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.record(activity.Entry{
			User: id.Name, Token: id.Token, Remote: r.RemoteAddr, Method: r.Method, Path: path,
			Status: rec.status, Error: rec.errorMessage(),
		})
	})
}

// authenticate resolves an API token or the session cookie. A token's role is capped by
// its owner's current role, so removing someone from a group also disarms their tokens.
func (s *Server) authenticate(r *http.Request, bearer string, hasBearer bool) (auth.Identity, bool) {
	if hasBearer {
		if s.tokens == nil {
			return auth.Identity{}, false
		}
		tok, ok := s.tokens.Verify(bearer)
		if !ok {
			return auth.Identity{}, false
		}
		role := min(tok.Role, s.auth.RoleOf(tok.Owner))
		if role == auth.NoAccess {
			return auth.Identity{}, false
		}
		return auth.Identity{Name: tok.Owner, Role: role, Token: tok.Name}, true
	}
	return s.auth.User(auth.SessionToken(r))
}

func (s *Server) record(e activity.Entry) {
	s.log.Info("api change", "user", e.User, "method", e.Method, "path", e.Path, "status", e.Status)
	if s.activity != nil {
		if err := s.activity.Add(e); err != nil {
			s.log.Warn("activity log", "err", err)
		}
	}
}

// statusRecorder keeps the status and (for failures) the JSON error message.
type statusRecorder struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status >= 400 && r.body.Len() < 2048 {
		r.body.Write(b)
	}
	return r.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach Flush and Hijack (streams, WebSockets).
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *statusRecorder) errorMessage() string {
	if r.status < 400 {
		return ""
	}
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(r.body.Bytes(), &e) == nil {
		return e.Error
	}
	return http.StatusText(r.status)
}

// ---- Tokens and activity endpoints --------------------------------------------------------

func (s *Server) accessRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/tokens", s.listTokens)
	mux.HandleFunc("POST /api/v1/tokens", s.createToken)
	mux.HandleFunc("DELETE /api/v1/tokens/{id}", s.revokeToken)
	mux.HandleFunc("GET /api/v1/activity", s.listActivity)
}

// listTokens: your own tokens; admins add ?all=true to see everyone's.
func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	if s.tokens == nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"tokens": []auth.Token{}})
		return
	}
	id := identity(r)
	owner := id.Name
	if id.Role == auth.Admin && r.URL.Query().Get("all") == "true" {
		owner = ""
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"tokens": s.tokens.List(owner)})
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	if s.tokens == nil || s.auth == nil {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "API tokens need sign-in to be enabled"})
		return
	}
	var b struct {
		Name    string    `json:"name"`
		Role    auth.Role `json:"role"`
		TTLDays int       `json:"ttlDays"` // 0: no expiry
	}
	if !s.decode(w, r, &b) {
		return
	}
	id := identity(r)
	if id.Token != "" {
		s.writeJSON(w, http.StatusForbidden, map[string]string{"error": "tokens cannot create tokens; sign in to the web UI"})
		return
	}
	secret, tok, err := s.tokens.Create(id, b.Name, b.Role, time.Duration(b.TTLDays)*24*time.Hour)
	if err != nil {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	s.writeJSON(w, http.StatusCreated, map[string]any{"token": tok, "secret": secret})
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request) {
	if s.tokens == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such token"})
		return
	}
	if err := s.tokens.Revoke(identity(r), r.PathValue("id")); err != nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listActivity: GET /api/v1/activity?limit=200&user=…&target=<uuid>&failed=true
func (s *Server) listActivity(w http.ResponseWriter, r *http.Request) {
	if s.activity == nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"entries": []activity.Entry{}})
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > activity.Keep {
		limit = 200
	}
	f := activity.Filter{User: q.Get("user"), Target: q.Get("target"), Failed: q.Get("failed") == "true"}
	s.writeJSON(w, http.StatusOK, map[string]any{"entries": s.activity.List(limit, f)})
}
