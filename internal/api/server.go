// Package api exposes VirtoMir's HTTP/JSON API (versioned under /api/v1).
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/onurbaydas/virtomir/internal/virt"
)

// Backend is what the API needs from the virtualization layer (implemented by *virt.Client).
type Backend interface {
	HostInfo() (virt.HostInfo, error)
	ListDomains() ([]virt.Domain, error)
}

type Server struct {
	backend Backend
	version string
	log     *slog.Logger
}

func NewServer(backend Backend, version string, log *slog.Logger) *Server {
	return &Server{backend: backend, version: version, log: log}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /api/v1/version", s.getVersion)
	mux.HandleFunc("GET /api/v1/host", s.getHost)
	mux.HandleFunc("GET /api/v1/domains", s.listDomains)
	return mux
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getVersion(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]string{"version": s.version})
}

func (s *Server) getHost(w http.ResponseWriter, _ *http.Request) {
	h, err := s.backend.HostInfo()
	if err != nil {
		s.writeError(w, http.StatusBadGateway, err)
		return
	}
	s.writeJSON(w, http.StatusOK, h)
}

func (s *Server) listDomains(w http.ResponseWriter, _ *http.Request) {
	doms, err := s.backend.ListDomains()
	if err != nil {
		s.writeError(w, http.StatusBadGateway, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"domains": doms})
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.log.Warn("write response", "err", err)
	}
}

// writeError reports a backend failure. 502: VirtoMir is up, but libvirt is not answering.
func (s *Server) writeError(w http.ResponseWriter, status int, err error) {
	s.log.Error("request failed", "status", status, "err", err)
	s.writeJSON(w, status, map[string]string{"error": err.Error()})
}
