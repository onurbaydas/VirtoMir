package api

import (
	"context"
	"net/http"
	"path/filepath"
)

// Updates (ADR-0036): which release is newest on GitHub, and installing it on this host.
// In a cluster each host updates itself (the UI talks to one host at a time).
func (s *Server) updateRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/update", s.updateStatus)
	mux.HandleFunc("POST /api/v1/update/install", s.updateInstall)
}

func (s *Server) updateStatus(w http.ResponseWriter, r *http.Request) {
	if s.updater == nil {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "updates are not available on this host"})
		return
	}
	s.writeJSON(w, http.StatusOK, s.updater.Check(r.Context(), r.URL.Query().Get("refresh") != ""))
}

func (s *Server) updateInstall(w http.ResponseWriter, _ *http.Request) {
	if s.updater == nil {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "updates are not available on this host"})
		return
	}
	dir := filepath.Join(s.stateDir, "updates")
	job := s.jobs.Start("update", "Update VirtoMir", func(ctx context.Context, report func(string)) (string, error) {
		return s.updater.Install(ctx, dir, report)
	})
	s.writeJSON(w, http.StatusAccepted, job)
}
