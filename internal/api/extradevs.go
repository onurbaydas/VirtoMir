package api

import (
	"net/http"

	"github.com/onurbaydas/virtomir/internal/virt"
)

// ExtraDevicer adds watchdogs, TPMs, shared folders, vGPU slices and the like, edits the
// QEMU command line and manages the host's mediated devices (implemented by *virt.Client).
// All writes are admin-only (requiredRole's default) and stay out of operator self-service:
// shared folders, mdevs and QEMU arguments reach past the host's safety checks.
type ExtraDevicer interface {
	AddDevice(uuid string, spec virt.DeviceSpec) (virt.DeviceResult, error)
	GetQEMUArgs(uuid string) (virt.QEMUArgs, error)
	SetQEMUArgs(uuid string, q virt.QEMUArgs) (virt.DefineResult, error)
	CreateMDev(spec virt.MDevSpec) (string, error)
	DeleteMDev(uuid string) error
}

func (s *Server) extraDeviceRoutes(mux *routeMux) {
	mux.HandleFunc("POST /api/v1/domains/{uuid}/devices", s.addDevice)
	mux.HandleFunc("GET /api/v1/domains/{uuid}/qemu-args", s.getQEMUArgs)
	mux.HandleFunc("PUT /api/v1/domains/{uuid}/qemu-args", s.setQEMUArgs)
	mux.HandleFunc("POST /api/v1/host/mdevs", s.createMediatedDevice)
	mux.HandleFunc("DELETE /api/v1/host/mdevs/{uuid}", s.deleteMediatedDevice)
}

func (s *Server) extraDevicer(w http.ResponseWriter) (ExtraDevicer, bool) {
	e, ok := s.backend.(ExtraDevicer)
	if !ok {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "extra devices are not available"})
	}
	return e, ok
}

func (s *Server) addDevice(w http.ResponseWriter, r *http.Request) {
	e, ok := s.extraDevicer(w)
	if !ok {
		return
	}
	var spec virt.DeviceSpec
	if !s.decode(w, r, &spec) {
		return
	}
	res, err := e.AddDevice(r.PathValue("uuid"), spec)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.done(w, http.StatusOK, res)
}

func (s *Server) getQEMUArgs(w http.ResponseWriter, r *http.Request) {
	e, ok := s.extraDevicer(w)
	if !ok {
		return
	}
	q, err := e.GetQEMUArgs(r.PathValue("uuid"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, q)
}

func (s *Server) setQEMUArgs(w http.ResponseWriter, r *http.Request) {
	e, ok := s.extraDevicer(w)
	if !ok {
		return
	}
	var q virt.QEMUArgs
	if !s.decode(w, r, &q) {
		return
	}
	res, err := e.SetQEMUArgs(r.PathValue("uuid"), q)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.done(w, http.StatusOK, res)
}

func (s *Server) createMediatedDevice(w http.ResponseWriter, r *http.Request) {
	e, ok := s.extraDevicer(w)
	if !ok {
		return
	}
	var spec virt.MDevSpec
	if !s.decode(w, r, &spec) {
		return
	}
	uuid, err := e.CreateMDev(spec)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.done(w, http.StatusCreated, map[string]string{"uuid": uuid})
}

func (s *Server) deleteMediatedDevice(w http.ResponseWriter, r *http.Request) {
	e, ok := s.extraDevicer(w)
	if !ok {
		return
	}
	if err := e.DeleteMDev(r.PathValue("uuid")); err != nil {
		s.writeError(w, err)
		return
	}
	s.done(w, http.StatusNoContent, nil)
}
