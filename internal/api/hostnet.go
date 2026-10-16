package api

import (
	"errors"
	"net/http"

	"github.com/onurbaydas/virtomir/internal/hostnet"
)

// Bridges, bonds and VLAN interfaces on the host (ADR-0042). Every change must be
// confirmed within the window from the browser that made it; otherwise it is rolled back.
func (s *Server) hostNetRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/host/network", s.hostNetStatus)
	mux.HandleFunc("POST /api/v1/host/network/interfaces", s.hostNetCreate)
	mux.HandleFunc("DELETE /api/v1/host/network/interfaces/{name}", s.hostNetDelete)
	mux.HandleFunc("POST /api/v1/host/network/pending/{id}/{action}", s.hostNetPending)
}

func (s *Server) hostNetMgr(w http.ResponseWriter) (*hostnet.Manager, bool) {
	if s.hostnet == nil {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "host network changes are not available on this host"})
		return nil, false
	}
	return s.hostnet, true
}

func (s *Server) hostNetError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, hostnet.ErrInvalid):
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
	case errors.Is(err, hostnet.ErrRefused):
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	default:
		s.writeError(w, err)
	}
}

func (s *Server) hostNetStatus(w http.ResponseWriter, _ *http.Request) {
	m, ok := s.hostNetMgr(w)
	if !ok {
		return
	}
	s.writeJSON(w, http.StatusOK, m.Status())
}

func (s *Server) hostNetCreate(w http.ResponseWriter, r *http.Request) {
	m, ok := s.hostNetMgr(w)
	if !ok {
		return
	}
	var spec hostnet.Spec
	if !s.decode(w, r, &spec) {
		return
	}
	p, err := m.Create(r.Context(), spec)
	if err != nil {
		s.hostNetError(w, err)
		return
	}
	s.writeJSON(w, http.StatusAccepted, p)
}

func (s *Server) hostNetDelete(w http.ResponseWriter, r *http.Request) {
	m, ok := s.hostNetMgr(w)
	if !ok {
		return
	}
	p, err := m.Delete(r.Context(), r.PathValue("name"))
	if err != nil {
		s.hostNetError(w, err)
		return
	}
	s.writeJSON(w, http.StatusAccepted, p)
}

func (s *Server) hostNetPending(w http.ResponseWriter, r *http.Request) {
	m, ok := s.hostNetMgr(w)
	if !ok {
		return
	}
	var err error
	switch r.PathValue("action") {
	case "confirm":
		err = m.Confirm(r.PathValue("id"))
	case "rollback":
		err = m.Rollback(r.PathValue("id"))
	default:
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown action"})
		return
	}
	if err != nil {
		s.hostNetError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, m.Status())
}
