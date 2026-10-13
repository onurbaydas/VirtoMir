package api

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/onurbaydas/virtomir/internal/activity"
	"github.com/onurbaydas/virtomir/internal/auth"
	"github.com/onurbaydas/virtomir/internal/cluster"
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
	case strings.HasPrefix(path, "/api/v1/cluster/peer/"):
		return auth.NoAccess // host-to-host protocol; the handlers check the peer or the join secret
	case strings.HasPrefix(path, "/api/v1/tokens"):
		return auth.Viewer // everyone manages their own tokens (handlers check ownership)
	case path == "/api/v1/activity", path == "/api/v1/signin-config", path == "/api/v1/quotas":
		return auth.Admin // who may sign in and who may create what is the admins' business
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
	case len(parts) == 2 && parts[1] == "backup" && method == http.MethodPost:
		return true
	}
	return false
}

// guard authenticates (session cookie, API token or a cluster peer vouching for its user),
// authorizes by role, rejects cross-site writes, forwards requests for other cluster members
// and records every change in the activity log.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peerID, isPeer, peerOK := s.peerIdentity(r)
		if !peerOK {
			s.writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unknown cluster member"})
			return
		}
		unsafe := r.Method != http.MethodGet && r.Method != http.MethodHead
		bearer, hasBearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		// Tokens and peers are not sent by browsers automatically, so they cannot be used for CSRF.
		if unsafe && !hasBearer && !isPeer && !sameOrigin(r) {
			s.writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-site request refused"})
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r) // the UI itself
			return
		}

		// /api/v1/hosts/<id>/…: this host serves its own ID, others are forwarded below.
		var member cluster.Node
		path := r.URL.Path
		if hostID, inner, ok := splitHost(path); ok {
			switch {
			case s.cluster == nil || hostID == s.cluster.SelfID():
				r.URL.Path, r.URL.RawPath, path = inner, "", inner
			case isPeer:
				s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "members do not forward for each other"})
				return
			default:
				if member, ok = s.cluster.Member(hostID); !ok {
					s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such host in this cluster"})
					return
				}
				path = inner
			}
		}

		need := requiredRole(r.Method, path)
		id := auth.Identity{Name: "dev", Role: auth.Admin}
		switch {
		case isPeer:
			id = peerID
			if id.Role == auth.NoAccess && !strings.HasPrefix(path, "/api/v1/cluster/peer/") {
				s.writeJSON(w, http.StatusForbidden, map[string]string{"error": "members may only call the cluster protocol on their own behalf"})
				return
			}
		case s.auth != nil:
			var ok bool
			id, ok = s.authenticate(r, bearer, hasBearer)
			if !ok && need > auth.NoAccess {
				s.writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "sign in required"})
				return
			}
		}
		if id.Role < need && !s.selfService(id, r.Method, path) {
			s.writeJSON(w, http.StatusForbidden, map[string]string{
				"error": "your role (" + id.Role.String() + ") may not do this; it needs " + need.String(),
			})
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), identityKey, id))

		if member.ID != "" {
			if !unsafe {
				s.forward(w, r, member, path, id)
				return
			}
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			s.forward(rec, r, member, path, id)
			s.record(activity.Entry{
				User: id.Name, Token: id.Token, Remote: r.RemoteAddr, Method: r.Method, Path: r.URL.Path,
				Status: rec.status, Error: rec.errorMessage(),
			})
			return
		}

		if !unsafe || strings.HasPrefix(path, "/api/v1/auth/") {
			next.ServeHTTP(w, r)
			return
		}
		// Anything changed outside VirtoMir since the last look is recorded as such first,
		// so this user's version shows only this user's change.
		s.captureConfig(path, "")
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if rec.status < 400 {
			s.captureConfig(path, id.Name)
		}
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
	entries := s.activity.List(limit, f)
	if q.Get("format") == "csv" {
		writeActivityCSV(w, entries)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

// writeActivityCSV is the audit export: one row per change, oldest last like the list.
func writeActivityCSV(w http.ResponseWriter, entries []activity.Entry) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=virtomir-activity.csv")
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"time", "user", "token", "remote", "method", "path", "status", "error"})
	for _, e := range entries {
		_ = cw.Write([]string{e.Time.UTC().Format(time.RFC3339), e.User, e.Token, e.Remote, e.Method, e.Path, strconv.Itoa(e.Status), e.Error})
	}
	cw.Flush()
}
