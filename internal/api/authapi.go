package api

import (
	"errors"
	"net/http"

	"github.com/onurbaydas/virtomir/internal/activity"
	"github.com/onurbaydas/virtomir/internal/auth"
)

func (s *Server) authRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/auth/session", s.getSession)
	mux.HandleFunc("POST /api/v1/auth/login", s.login)
	mux.HandleFunc("POST /api/v1/auth/logout", s.logout)
}

type sessionInfo struct {
	// Mode "pam": sign-in required; "none": development mode without sign-in.
	Mode string    `json:"mode"`
	User string    `json:"user,omitempty"`
	Role auth.Role `json:"role,omitempty"`
	// NeedCode: the password was right; send the one-time code with Pending to /auth/2fa.
	NeedCode bool   `json:"needCode,omitempty"`
	Pending  string `json:"pending,omitempty"`
}

func (s *Server) mode() string {
	if s.auth == nil {
		return "none"
	}
	return "pam"
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		s.writeJSON(w, http.StatusOK, sessionInfo{Mode: "none", Role: auth.Admin})
		return
	}
	u, ok := s.auth.User(auth.SessionToken(r))
	if !ok {
		s.writeJSON(w, http.StatusUnauthorized, sessionInfo{Mode: s.mode()})
		return
	}
	s.writeJSON(w, http.StatusOK, sessionInfo{Mode: s.mode(), User: u.Name, Role: u.Role})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		s.writeJSON(w, http.StatusOK, sessionInfo{Mode: "none", Role: auth.Admin})
		return
	}
	var body struct {
		User     string `json:"user"`
		Password string `json:"password"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	token, err := s.auth.Login(r.Context(), r.RemoteAddr, body.User, body.Password)
	var need *auth.CodeRequired
	if errors.As(err, &need) {
		s.writeJSON(w, http.StatusOK, sessionInfo{Mode: s.mode(), NeedCode: true, Pending: need.Pending})
		return
	}
	if err != nil {
		s.log.Warn("sign-in failed", "user", body.User, "remote", r.RemoteAddr, "err", err)
		s.recordSignIn(body.User, r.RemoteAddr, http.StatusUnauthorized, err.Error())
		s.writeError(w, err)
		return
	}
	s.recordSignIn(body.User, r.RemoteAddr, http.StatusOK, "")
	auth.SetCookie(w, r, token)
	u, _ := s.auth.User(token)
	s.writeJSON(w, http.StatusOK, sessionInfo{Mode: s.mode(), User: u.Name, Role: u.Role})
}

func (s *Server) recordSignIn(user, remote string, status int, errMsg string) {
	s.record(activity.Entry{User: user, Remote: remote, Method: "POST", Path: "/api/v1/auth/login", Status: status, Error: errMsg})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if s.auth != nil {
		s.auth.Logout(auth.SessionToken(r))
	}
	auth.ClearCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}
