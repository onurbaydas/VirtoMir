package api

import (
	"net/http"

	"github.com/onurbaydas/virtomir/internal/virt"
)

func (s *Server) storageRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/storage/pools", s.listPools)
	mux.HandleFunc("POST /api/v1/storage/pools", s.createPool)
	mux.HandleFunc("DELETE /api/v1/storage/pools/{pool}", s.deletePool)
	mux.HandleFunc("POST /api/v1/storage/pools/{pool}/actions/{action}", s.poolAction)
	mux.HandleFunc("PUT /api/v1/storage/pools/{pool}/autostart", s.poolAutostart)
	mux.HandleFunc("GET /api/v1/storage/pools/{pool}/volumes", s.listVolumes)
	mux.HandleFunc("POST /api/v1/storage/pools/{pool}/volumes", s.createVolume)
	mux.HandleFunc("DELETE /api/v1/storage/pools/{pool}/volumes/{vol}", s.deleteVolume)
	mux.HandleFunc("POST /api/v1/storage/pools/{pool}/upload", s.upload)
}

func (s *Server) listPools(w http.ResponseWriter, _ *http.Request) {
	pools, err := s.backend.ListPools()
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"pools": pools})
}

func (s *Server) createPool(w http.ResponseWriter, r *http.Request) {
	var spec virt.PoolSpec
	if !s.decode(w, r, &spec) {
		return
	}
	if err := s.backend.CreatePool(spec); err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, map[string]string{"name": spec.Name})
}

func (s *Server) deletePool(w http.ResponseWriter, r *http.Request) {
	if err := s.backend.DeletePool(r.PathValue("pool")); err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) poolAction(w http.ResponseWriter, r *http.Request) {
	if err := s.backend.PoolAction(r.PathValue("pool"), r.PathValue("action")); err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) poolAutostart(w http.ResponseWriter, r *http.Request) {
	var t toggle
	if !s.decode(w, r, &t) {
		return
	}
	if err := s.backend.SetPoolAutostart(r.PathValue("pool"), t.Enabled); err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listVolumes(w http.ResponseWriter, r *http.Request) {
	vols, err := s.backend.ListVolumes(r.PathValue("pool"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"volumes": vols})
}

func (s *Server) createVolume(w http.ResponseWriter, r *http.Request) {
	var spec virt.VolumeSpec
	if !s.decode(w, r, &spec) {
		return
	}
	v, err := s.backend.CreateVolume(r.PathValue("pool"), spec)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, v)
}

func (s *Server) deleteVolume(w http.ResponseWriter, r *http.Request) {
	if err := s.backend.DeleteVolume(r.PathValue("pool"), r.PathValue("vol")); err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// upload streams the raw request body into a new volume: POST …/upload?name=debian.iso
// with Content-Length set (browsers do this for File bodies).
func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	if r.ContentLength <= 0 {
		s.writeJSON(w, http.StatusLengthRequired, map[string]string{"error": "Content-Length is required"})
		return
	}
	v, err := s.backend.UploadVolume(r.Context(), r.PathValue("pool"), r.URL.Query().Get("name"), r.ContentLength, r.Body)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, v)
}
