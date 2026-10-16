package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/onurbaydas/virtomir/internal/cluster"
	"github.com/onurbaydas/virtomir/internal/ha"
)

// High availability (ADR-0031). The configuration is cluster-wide: a change on any member
// reaches the others through the heartbeats. /api/v1/cluster/peer/ha/* is the member-to-member
// part: heartbeats, machine definitions, configuration and restart requests.
func (s *Server) haRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/ha/status", s.haStatus)
	mux.HandleFunc("PUT /api/v1/ha/settings", s.haSettings)
	mux.HandleFunc("PUT /api/v1/ha/vms/{uuid}", s.haSetPolicy)
	mux.HandleFunc("DELETE /api/v1/ha/vms/{uuid}", s.haDeletePolicy)
	mux.HandleFunc("POST /api/v1/ha/nodes/{id}/fence", s.haFence)
	mux.HandleFunc("POST /api/v1/ha/nodes/{id}/confirm-off", s.haConfirmOff)
	mux.HandleFunc("POST /api/v1/ha/nodes/{id}/simulate", s.haSimulate)

	mux.HandleFunc("POST /api/v1/cluster/peer/ha/heartbeat", s.peerHAHeartbeat)
	mux.HandleFunc("PUT /api/v1/cluster/peer/ha/replicas", s.peerHAReplicas)
	mux.HandleFunc("GET /api/v1/cluster/peer/ha/config", s.peerHAConfig)
	mux.HandleFunc("POST /api/v1/cluster/peer/ha/recover", s.peerHARecover)
}

// SetHA connects the HA manager (it needs the server as its transport, so it comes later).
func (s *Server) SetHA(m *ha.Manager) { s.haMgr = m }

// HATransport is how the HA manager reaches the other members: over the pinned peer links.
func (s *Server) HATransport() ha.Transport { return haTransport{s} }

type haTransport struct{ s *Server }

func (t haTransport) SelfID() string {
	if t.s.cluster == nil {
		return "local"
	}
	return t.s.cluster.SelfID()
}

// Members is every cluster member; a standalone host is a cluster of one.
func (t haTransport) Members() []ha.Node {
	if t.s.cluster == nil || !t.s.cluster.Clustered() {
		return []ha.Node{{ID: t.SelfID(), Name: t.s.hostName}}
	}
	ms, _ := t.s.cluster.Members()
	out := make([]ha.Node, len(ms))
	for i, m := range ms {
		out[i] = ha.Node{ID: m.ID, Name: m.Name}
	}
	return out
}

func (t haTransport) node(n ha.Node) (*cluster.Peer, error) {
	m, ok := t.s.cluster.Member(n.ID)
	if !ok {
		return nil, &cluster.StatusError{Code: http.StatusNotFound, Message: "no such member " + n.Name}
	}
	return t.s.peer(m), nil
}

func (t haTransport) Heartbeat(ctx context.Context, n ha.Node, mine ha.Report) (ha.Report, error) {
	var theirs ha.Report
	p, err := t.node(n)
	if err == nil {
		err = p.Call(ctx, http.MethodPost, "/api/v1/cluster/peer/ha/heartbeat", mine, &theirs)
	}
	return theirs, err
}

func (t haTransport) PushReplicas(ctx context.Context, n ha.Node, reps []ha.Replica) error {
	p, err := t.node(n)
	if err != nil {
		return err
	}
	return p.Call(ctx, http.MethodPut, "/api/v1/cluster/peer/ha/replicas", reps, nil)
}

func (t haTransport) FetchConfig(ctx context.Context, n ha.Node) (ha.Config, error) {
	var c ha.Config
	p, err := t.node(n)
	if err == nil {
		err = p.Call(ctx, http.MethodGet, "/api/v1/cluster/peer/ha/config", nil, &c)
	}
	return c, err
}

func (t haTransport) Recover(ctx context.Context, n ha.Node, r ha.Replica) error {
	p, err := t.node(n)
	if err != nil {
		return err
	}
	return p.Call(ctx, http.MethodPost, "/api/v1/cluster/peer/ha/recover", r, nil)
}

// ---- People -------------------------------------------------------------------------------

func (s *Server) noHA(w http.ResponseWriter) bool {
	if s.ha == nil || s.haMgr == nil {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "high availability is not available on this host"})
		return true
	}
	return false
}

func (s *Server) haStatus(w http.ResponseWriter, _ *http.Request) {
	if s.noHA(w) {
		return
	}
	s.writeJSON(w, http.StatusOK, s.haMgr.Status())
}

func (s *Server) haSettings(w http.ResponseWriter, r *http.Request) {
	if s.noHA(w) {
		return
	}
	var st ha.Settings
	if !s.decode(w, r, &st) {
		return
	}
	if _, err := s.ha.UpdateSettings(st); err != nil {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	s.writeJSON(w, http.StatusOK, s.haMgr.Status())
}

func (s *Server) haSetPolicy(w http.ResponseWriter, r *http.Request) {
	if s.noHA(w) {
		return
	}
	var p ha.VMPolicy
	if !s.decode(w, r, &p) {
		return
	}
	p.UUID = r.PathValue("uuid")
	if p.Name == "" {
		if d, err := s.backend.DomainDetail(p.UUID); err == nil {
			p.Name = d.Name
		}
	}
	cfg, err := s.ha.SetPolicy(p)
	if err != nil {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	s.writeJSON(w, http.StatusOK, cfg.VMPolicies[strings.ToLower(p.UUID)])
}

func (s *Server) haDeletePolicy(w http.ResponseWriter, r *http.Request) {
	if s.noHA(w) {
		return
	}
	if _, err := s.ha.DeletePolicy(r.PathValue("uuid")); err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// haFence runs a member's fence agent by hand: {"action": "status" | "off" | "reboot"}.
func (s *Server) haFence(w http.ResponseWriter, r *http.Request) {
	if s.noHA(w) {
		return
	}
	var b struct {
		Action ha.FenceAction `json:"action"`
	}
	if !s.decode(w, r, &b) {
		return
	}
	switch b.Action {
	case ha.FenceStatus, ha.FenceOff, ha.FenceReboot:
	default:
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "action must be status, off or reboot"})
		return
	}
	res, err := s.haMgr.Fence(r.Context(), r.PathValue("id"), b.Action, identity(r).Name)
	if err != nil {
		s.writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "result": res})
		return
	}
	s.writeJSON(w, http.StatusOK, res)
}

func (s *Server) haConfirmOff(w http.ResponseWriter, r *http.Request) {
	if s.noHA(w) {
		return
	}
	if err := s.haMgr.ConfirmOff(r.PathValue("id"), identity(r).Name); err != nil {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) haSimulate(w http.ResponseWriter, r *http.Request) {
	if s.noHA(w) {
		return
	}
	sim, err := s.haMgr.Simulate(r.PathValue("id"))
	if err != nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	s.writeJSON(w, http.StatusOK, sim)
}

// ---- Members ------------------------------------------------------------------------------

func (s *Server) peerHAHeartbeat(w http.ResponseWriter, r *http.Request) {
	if s.notPeer(w, r) || s.noHA(w) {
		return
	}
	var rep ha.Report
	if !s.decode(w, r, &rep) {
		return
	}
	// The sender is who the peer credential says, whatever the body claims.
	if n, ok := s.cluster.VerifyPeer(strings.TrimPrefix(r.Header.Get("Authorization"), cluster.AuthScheme)); ok {
		rep.NodeID = n.ID
	}
	s.haMgr.Seen(rep)
	s.writeJSON(w, http.StatusOK, s.haMgr.LocalReport())
}

func (s *Server) peerHAReplicas(w http.ResponseWriter, r *http.Request) {
	if s.notPeer(w, r) || s.noHA(w) {
		return
	}
	var reps []ha.Replica
	dec := http.MaxBytesReader(w, r.Body, 64<<20) // definitions of many machines
	if err := json.NewDecoder(dec).Decode(&reps); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.haMgr.Accept(reps)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) peerHAConfig(w http.ResponseWriter, r *http.Request) {
	if s.notPeer(w, r) || s.noHA(w) {
		return
	}
	s.writeJSON(w, http.StatusOK, s.ha.Config())
}

func (s *Server) peerHARecover(w http.ResponseWriter, r *http.Request) {
	if s.notPeer(w, r) || s.noHA(w) {
		return
	}
	var rep ha.Replica
	if !s.decode(w, r, &rep) {
		return
	}
	if err := s.haMgr.Recover(rep); err != nil {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	s.live.Refresh()
	w.WriteHeader(http.StatusNoContent)
}
