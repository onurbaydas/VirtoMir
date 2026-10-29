package api

import (
	"net/http"

	"github.com/onurbaydas/virtomir/internal/virt"
)

func (s *Server) networkRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/networks", s.listNetworks)
	mux.HandleFunc("POST /api/v1/networks", s.createNetwork)
	mux.HandleFunc("DELETE /api/v1/networks/{name}", s.deleteNetwork)
	mux.HandleFunc("POST /api/v1/networks/{name}/actions/{action}", s.networkAction)
	mux.HandleFunc("PUT /api/v1/networks/{name}/autostart", s.networkAutostart)
	mux.HandleFunc("GET /api/v1/networks/{name}/leases", s.networkLeases)
}

func (s *Server) listNetworks(w http.ResponseWriter, _ *http.Request) {
	nets, err := s.backend.ListNetworks()
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"networks": nets})
}

func (s *Server) createNetwork(w http.ResponseWriter, r *http.Request) {
	var spec virt.NetworkSpec
	if !s.decode(w, r, &spec) {
		return
	}
	if err := s.backend.CreateNetwork(spec); err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, map[string]string{"name": spec.Name})
}

func (s *Server) deleteNetwork(w http.ResponseWriter, r *http.Request) {
	if err := s.backend.DeleteNetwork(r.PathValue("name")); err != nil {
		s.writeError(w, err)
		return
	}
	s.wifiBridges().Remove(r.Context(), r.PathValue("name")) // its Wi-Fi bridge, if it had one
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) networkAction(w http.ResponseWriter, r *http.Request) {
	if err := s.backend.NetworkAction(r.PathValue("name"), r.PathValue("action")); err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) networkAutostart(w http.ResponseWriter, r *http.Request) {
	var t toggle
	if !s.decode(w, r, &t) {
		return
	}
	if err := s.backend.SetNetworkAutostart(r.PathValue("name"), t.Enabled); err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) networkLeases(w http.ResponseWriter, r *http.Request) {
	leases, err := s.backend.NetworkLeases(r.PathValue("name"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"leases": leases})
}
