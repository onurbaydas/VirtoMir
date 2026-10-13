package api

import (
	"net/http"

	"github.com/onurbaydas/virtomir/internal/virt"
)

// HostDevicer lists host hardware and passes devices to machines (implemented by *virt.Client).
type HostDevicer interface {
	HostHardware() (virt.HostHardware, error)
	AttachHostdev(uuid string, spec virt.HostdevSpec) (string, error)
}

func (s *Server) hostdevRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/host/devices", s.hostDevices)
	mux.HandleFunc("POST /api/v1/domains/{uuid}/hostdevs", s.attachHostdev)
}

func (s *Server) hostDevicer(w http.ResponseWriter) (HostDevicer, bool) {
	h, ok := s.backend.(HostDevicer)
	if !ok {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "host devices are not available"})
	}
	return h, ok
}

func (s *Server) hostDevices(w http.ResponseWriter, _ *http.Request) {
	h, ok := s.hostDevicer(w)
	if !ok {
		return
	}
	hw, err := h.HostHardware()
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, hw)
}

func (s *Server) attachHostdev(w http.ResponseWriter, r *http.Request) {
	h, ok := s.hostDevicer(w)
	if !ok {
		return
	}
	var spec virt.HostdevSpec
	if !s.decode(w, r, &spec) {
		return
	}
	liveErr, err := h.AttachHostdev(r.PathValue("uuid"), spec)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.done(w, http.StatusOK, map[string]string{"liveError": liveErr})
}
