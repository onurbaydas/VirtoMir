package api

import (
	"net/http"

	"github.com/onurbaydas/virtomir/internal/auth"
)

func (s *Server) authRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/auth/session", s.getSession)
	mux.HandleFunc("POST /api/v1/auth/login", s.login)
	mux.HandleFunc("POST /api/v1/auth/logout", s.logout)
}

type sessionInfo struct {
	// Mode "pam": sign-in required; "none": development mode without sign-in.
	Mode string `json:"mode"`
	User string `json:"user,omitempty"`
}

func (s *Server) mode() string {
	if s.auth == nil {
		return "none"
	}
	return "pam"
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		s.writeJSON(w, http.StatusOK, sessionInfo{Mode: "none"})
		return
	}
	u, ok := s.auth.User(auth.Token(r))
	if !ok {
		s.writeJSON(w, http.StatusUnauthorized, sessionInfo{Mode: s.mode()})
		return
	}
	s.writeJSON(w, http.StatusOK, sessionInfo{Mode: s.mode(), User: u})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		s.writeJSON(w, http.StatusOK, sessionInfo{Mode: "none"})
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
	if err != nil {
		s.log.Warn("sign-in failed", "user", body.User, "remote", r.RemoteAddr, "err", err)
		s.writeError(w, err)
		return
	}
	s.log.Info("signed in", "user", body.User, "remote", r.RemoteAddr)
	auth.SetCookie(w, r, token)
	s.writeJSON(w, http.StatusOK, sessionInfo{Mode: s.mode(), User: body.User})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if s.auth != nil {
		s.auth.Logout(auth.Token(r))
	}
	auth.ClearCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}
