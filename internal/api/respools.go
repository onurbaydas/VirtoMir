package api

import (
	"encoding/json"
	"net/http"

	"github.com/onurbaydas/virtomir/internal/respool"
)

func (s *Server) respoolRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/respools", s.listRespools)
	mux.HandleFunc("POST /api/v1/respools", s.createRespool)
	mux.HandleFunc("GET /api/v1/respools/{id}", s.getRespool)
	mux.HandleFunc("PUT /api/v1/respools/{id}", s.updateRespool)
	mux.HandleFunc("DELETE /api/v1/respools/{id}", s.deleteRespool)
	mux.HandleFunc("POST /api/v1/respools/{id}/assign", s.assignRespoolVM)
	mux.HandleFunc("GET /api/v1/respools/usage", s.getRespoolsUsage)
}

func (s *Server) listRespools(w http.ResponseWriter, _ *http.Request) {
	if s.respools == nil {
		s.writeJSON(w, http.StatusOK, []respool.ResourcePool{})
		return
	}
	s.writeJSON(w, http.StatusOK, s.respools.List())
}

func (s *Server) createRespool(w http.ResponseWriter, r *http.Request) {
	if s.respools == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "resource pools not configured"})
		return
	}
	var p respool.ResourcePool
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	created, err := s.respools.Create(p)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, created)
}

func (s *Server) getRespool(w http.ResponseWriter, r *http.Request) {
	if s.respools == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "resource pools not configured"})
		return
	}
	id := r.PathValue("id")
	p, ok := s.respools.Get(id)
	if !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "resource pool not found"})
		return
	}
	s.writeJSON(w, http.StatusOK, p)
}

func (s *Server) updateRespool(w http.ResponseWriter, r *http.Request) {
	if s.respools == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "resource pools not configured"})
		return
	}
	id := r.PathValue("id")
	var p respool.ResourcePool
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	p.ID = id
	if err := s.respools.Update(p); err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *Server) deleteRespool(w http.ResponseWriter, r *http.Request) {
	if s.respools == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "resource pools not configured"})
		return
	}
	id := r.PathValue("id")
	if err := s.respools.Delete(id); err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) assignRespoolVM(w http.ResponseWriter, r *http.Request) {
	if s.respools == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "resource pools not configured"})
		return
	}
	id := r.PathValue("id")
	var req struct {
		VMUUID   string `json:"vmUuid"`
		Unassign bool   `json:"unassign"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	var err error
	if req.Unassign {
		err = s.respools.UnassignVM(req.VMUUID)
	} else {
		err = s.respools.AssignVM(id, req.VMUUID)
	}
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) getRespoolsUsage(w http.ResponseWriter, _ *http.Request) {
	if s.respools == nil {
		s.writeJSON(w, http.StatusOK, []respool.PoolUsage{})
		return
	}
	domains, _ := s.backend.ListDomains()
	domainMap := make(map[string]int)
	memMap := make(map[string]uint64)
	for _, d := range domains {
		domainMap[d.UUID] = d.VCPUs
		memMap[d.UUID] = d.MemoryKiB / 1024
	}

	var usages []respool.PoolUsage
	for _, p := range s.respools.List() {
		u := respool.PoolUsage{
			PoolID:           p.ID,
			VMCount:          len(p.AssignedVMs),
			UsedStorageBytes: make(map[string]uint64),
		}
		for _, vmUUID := range p.AssignedVMs {
			if vcpus, ok := domainMap[vmUUID]; ok {
				u.UsedVCPUs += vcpus
			}
			if mem, ok := memMap[vmUUID]; ok {
				u.UsedMemoryMiB += mem
			}
		}
		usages = append(usages, u)
	}
	s.writeJSON(w, http.StatusOK, usages)
}
