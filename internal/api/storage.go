package api

import (
	"net/http"

	"github.com/onurbaydas/virtomir/internal/virt"
	"github.com/onurbaydas/virtomir/internal/zfsrep"
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
	mux.HandleFunc("GET /api/v1/storage/sources", s.carveSources)
	mux.HandleFunc("POST /api/v1/storage/pools/carve", s.carvePool)

	mux.HandleFunc("GET /api/v1/storage/zfs/replications", s.listZFSReplications)
	mux.HandleFunc("POST /api/v1/storage/zfs/replications", s.createZFSReplication)
	mux.HandleFunc("PUT /api/v1/storage/zfs/replications/{id}", s.updateZFSReplication)
	mux.HandleFunc("DELETE /api/v1/storage/zfs/replications/{id}", s.deleteZFSReplication)
	mux.HandleFunc("POST /api/v1/storage/zfs/replications/{id}/sync", s.syncZFSReplication)
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

// zfsReplications answers 501 when this host has no ZFS replication.
func (s *Server) zfsReplications(w http.ResponseWriter) (*zfsrep.Manager, bool) {
	if s.zfsRep == nil {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "ZFS replication is not available on this host"})
		return nil, false
	}
	return s.zfsRep, true
}

func (s *Server) listZFSReplications(w http.ResponseWriter, _ *http.Request) {
	m, ok := s.zfsReplications(w)
	if !ok {
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"replications": m.List()})
}

func (s *Server) createZFSReplication(w http.ResponseWriter, r *http.Request) {
	m, ok := s.zfsReplications(w)
	if !ok {
		return
	}
	var spec zfsrep.Spec
	if !s.decode(w, r, &spec) {
		return
	}
	spec.ID = ""
	st, err := m.Put(spec)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, st)
}

func (s *Server) updateZFSReplication(w http.ResponseWriter, r *http.Request) {
	m, ok := s.zfsReplications(w)
	if !ok {
		return
	}
	var spec zfsrep.Spec
	if !s.decode(w, r, &spec) {
		return
	}
	spec.ID = r.PathValue("id")
	st, err := m.Put(spec)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, st)
}

func (s *Server) deleteZFSReplication(w http.ResponseWriter, r *http.Request) {
	m, ok := s.zfsReplications(w)
	if !ok {
		return
	}
	if err := m.Delete(r.PathValue("id")); err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// syncZFSReplication starts a pass now; the answer says "syncing" and the list shows how it went.
func (s *Server) syncZFSReplication(w http.ResponseWriter, r *http.Request) {
	m, ok := s.zfsReplications(w)
	if !ok {
		return
	}
	st, err := m.Start(r.PathValue("id"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusAccepted, st)
}
