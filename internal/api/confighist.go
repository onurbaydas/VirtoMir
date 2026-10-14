package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/onurbaydas/virtomir/internal/confighist"
)

// Configuration history of a machine (ADR-0021): every saved definition, compare, revert.
func (s *Server) configHistoryRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/domains/{uuid}/config-history", s.listConfigVersions)
	mux.HandleFunc("GET /api/v1/domains/{uuid}/config-history/{id}", s.getConfigVersion)
	mux.HandleFunc("POST /api/v1/domains/{uuid}/config-history/{id}/revert", s.revertConfigVersion)
}

func (s *Server) noConfigHistory(w http.ResponseWriter) bool {
	if s.confighist == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "configuration history is not kept on this host"})
		return true
	}
	return false
}

func (s *Server) listConfigVersions(w http.ResponseWriter, r *http.Request) {
	if s.noConfigHistory(w) {
		return
	}
	vs, err := s.confighist.List(r.PathValue("uuid"))
	if err != nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"versions": vs})
}

func (s *Server) getConfigVersion(w http.ResponseWriter, r *http.Request) {
	if s.noConfigHistory(w) {
		return
	}
	doc, err := s.confighist.Get(r.PathValue("uuid"), r.PathValue("id"))
	if err != nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"xml": doc})
}

// revertConfigVersion makes an old version the machine's definition again (validated by
// libvirt like any XML edit; a running machine picks it up on its next start).
func (s *Server) revertConfigVersion(w http.ResponseWriter, r *http.Request) {
	if s.noConfigHistory(w) {
		return
	}
	uuid := r.PathValue("uuid")
	doc, err := s.confighist.Get(uuid, r.PathValue("id"))
	if errors.Is(err, confighist.ErrNoVersion) {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	res, err := s.backend.DefineXML(uuid, doc)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.done(w, http.StatusOK, res)
}

// captureConfig saves a machine's definition after a successful change through the API,
// with the user who made it. Called by guard for every changing /domains/{uuid}/… request.
func (s *Server) captureConfig(path, user string) {
	if s.confighist == nil {
		return
	}
	rest, ok := strings.CutPrefix(path, "/api/v1/domains/")
	if !ok {
		return
	}
	uuid, _, _ := strings.Cut(rest, "/")
	if len(uuid) != 36 {
		return
	}
	_ = s.confighist.Capture(uuid, user) // a deleted machine simply has nothing to capture
}
