package api

import (
	"encoding/json"
	"net/http"

	"github.com/onurbaydas/virtomir/internal/ha"
)

func (s *Server) haRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/ha/status", s.getHAStatus)
	mux.HandleFunc("GET /api/v1/ha/config", s.getHAConfig)
	mux.HandleFunc("PUT /api/v1/ha/config", s.updateHAConfig)
	mux.HandleFunc("POST /api/v1/ha/fence/{id}", s.fenceHANode)
	mux.HandleFunc("GET /api/v1/ha/vms", s.getHAVMPolicies)
	mux.HandleFunc("PUT /api/v1/ha/vms/{uuid}", s.setHAVMPolicy)
	mux.HandleFunc("DELETE /api/v1/ha/vms/{uuid}", s.deleteHAVMPolicy)
	mux.HandleFunc("POST /api/v1/ha/simulate-failover", s.simulateHAFailover)
}

func (s *Server) getHAStatus(w http.ResponseWriter, _ *http.Request) {
	if s.ha == nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "message": "HA store not configured"})
		return
	}
	totalNodes := 1
	if s.cluster != nil {
		members, _ := s.cluster.Members()
		if len(members) > 0 {
			totalNodes = len(members)
		}
	}
	s.writeJSON(w, http.StatusOK, s.ha.GetStatus(totalNodes))
}

func (s *Server) getHAConfig(w http.ResponseWriter, _ *http.Request) {
	if s.ha == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "HA store not configured"})
		return
	}
	status := s.ha.GetStatus(1)
	s.writeJSON(w, http.StatusOK, status.Config)
}

func (s *Server) updateHAConfig(w http.ResponseWriter, r *http.Request) {
	if s.ha == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "HA store not configured"})
		return
	}
	var cfg ha.Config
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := s.ha.UpdateConfig(cfg); err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *Server) fenceHANode(w http.ResponseWriter, r *http.Request) {
	if s.ha == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "HA store not configured"})
		return
	}
	nodeID := r.PathValue("id")
	var req struct {
		Action string `json:"action"` // "poweroff", "reboot"
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	res, err := s.ha.FenceNode(r.Context(), nodeID, req.Action)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, res)
}

func (s *Server) getHAVMPolicies(w http.ResponseWriter, _ *http.Request) {
	if s.ha == nil {
		s.writeJSON(w, http.StatusOK, []ha.VMHAPolicy{})
		return
	}
	status := s.ha.GetStatus(1)
	s.writeJSON(w, http.StatusOK, status.Policies)
}

func (s *Server) setHAVMPolicy(w http.ResponseWriter, r *http.Request) {
	if s.ha == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "HA store not configured"})
		return
	}
	uuid := r.PathValue("uuid")
	var p ha.VMHAPolicy
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	p.UUID = uuid
	if err := s.ha.SetVMPolicy(p); err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, p)
}

func (s *Server) deleteHAVMPolicy(w http.ResponseWriter, r *http.Request) {
	if s.ha == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "HA store not configured"})
		return
	}
	uuid := r.PathValue("uuid")
	if err := s.ha.DeleteVMPolicy(uuid); err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) simulateHAFailover(w http.ResponseWriter, r *http.Request) {
	if s.ha == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "HA store not configured"})
		return
	}
	var req struct {
		DeadNodeID string `json:"deadNodeId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	var allNodes []string
	if s.cluster != nil {
		members, _ := s.cluster.Members()
		for _, m := range members {
			allNodes = append(allNodes, m.ID)
		}
	}
	if len(allNodes) == 0 {
		allNodes = []string{req.DeadNodeID, "node-backup-01"}
	}

	sim, err := s.ha.SimulateFailover(req.DeadNodeID, allNodes)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, sim)
}
