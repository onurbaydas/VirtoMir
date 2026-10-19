package api

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/onurbaydas/virtomir/internal/auth"
	"github.com/onurbaydas/virtomir/internal/gpool"
	"github.com/onurbaydas/virtomir/internal/hostinfo"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// GPU pools (D14, ADR-0071): the cluster's GPUs grouped by how machines get them.

func (s *Server) gpoolRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/cluster/gpus", s.clusterGPUs)
	mux.HandleFunc("GET /api/v1/gpu/pools", s.listGPUPools)
	mux.HandleFunc("POST /api/v1/gpu/pools", s.createGPUPool)
	mux.HandleFunc("GET /api/v1/gpu/pools/{id}", s.getGPUPool)
	mux.HandleFunc("PUT /api/v1/gpu/pools/{id}", s.updateGPUPool)
	mux.HandleFunc("DELETE /api/v1/gpu/pools/{id}", s.deleteGPUPool)
	mux.HandleFunc("GET /api/v1/gpu/pools/{id}/availability", s.gpuPoolAvailability)
	mux.HandleFunc("POST /api/v1/gpu/placement", s.gpuPlacement)
}

func (s *Server) noGPools(w http.ResponseWriter) bool {
	if s.gpools == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "gpu pools are not available on this host"})
		return true
	}
	return false
}

// GPUPoolView is a pool with what the cluster has of it.
type GPUPoolView struct {
	gpool.Pool
	Hosts []gpool.GPUResource `json:"hosts"`
	Total int                 `json:"total"`
	Used  int                 `json:"used"`
	Free  int                 `json:"free"`
}

func (s *Server) poolViews(ctx context.Context, id auth.Identity, pools []gpool.Pool) []GPUPoolView {
	avail := gpool.ClusterAvailability(pools, s.clusterHardware(ctx, id))
	out := make([]GPUPoolView, 0, len(pools))
	for _, p := range pools {
		v := GPUPoolView{Pool: p, Hosts: avail[p.ID]}
		for i := range v.Hosts {
			v.Hosts[i].Reserved = s.gpools.Reserved(p.ID, v.Hosts[i].HostID)
			v.Total += v.Hosts[i].Total
			v.Used += v.Hosts[i].Used
			v.Free += v.Hosts[i].Free
		}
		out = append(out, v)
	}
	return out
}

func (s *Server) listGPUPools(w http.ResponseWriter, r *http.Request) {
	if s.noGPools(w) {
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"pools": s.poolViews(r.Context(), identity(r), s.gpools.List())})
}

func (s *Server) getGPUPool(w http.ResponseWriter, r *http.Request) {
	if s.noGPools(w) {
		return
	}
	p, ok := s.gpools.Get(r.PathValue("id"))
	if !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no GPU pool " + r.PathValue("id")})
		return
	}
	s.writeJSON(w, http.StatusOK, s.poolViews(r.Context(), identity(r), []gpool.Pool{p})[0])
}

func (s *Server) gpuPoolAvailability(w http.ResponseWriter, r *http.Request) {
	if s.noGPools(w) {
		return
	}
	p, ok := s.gpools.Get(r.PathValue("id"))
	if !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no GPU pool " + r.PathValue("id")})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"hosts": s.poolViews(r.Context(), identity(r), []gpool.Pool{p})[0].Hosts})
}

func (s *Server) createGPUPool(w http.ResponseWriter, r *http.Request) {
	if s.noGPools(w) {
		return
	}
	var p gpool.Pool
	if !s.decode(w, r, &p) {
		return
	}
	created, err := s.gpools.Create(p)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.replicateToMembers(r, http.MethodPost, "/api/v1/gpu/pools", created)
	s.writeJSON(w, http.StatusCreated, created)
}

func (s *Server) updateGPUPool(w http.ResponseWriter, r *http.Request) {
	if s.noGPools(w) {
		return
	}
	var p gpool.Pool
	if !s.decode(w, r, &p) {
		return
	}
	p.ID = r.PathValue("id")
	saved, err := s.gpools.Update(p)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.replicateToMembers(r, http.MethodPut, "/api/v1/gpu/pools/"+p.ID, saved)
	s.writeJSON(w, http.StatusOK, saved)
}

func (s *Server) deleteGPUPool(w http.ResponseWriter, r *http.Request) {
	if s.noGPools(w) {
		return
	}
	id := r.PathValue("id")
	if r.URL.Query().Get("local") == "" { // the member that was asked checks; copies follow it
		if why := s.gpoolInUse(r.Context(), identity(r), id); why != "" {
			s.writeJSON(w, http.StatusConflict, map[string]string{"error": why})
			return
		}
	}
	if err := s.gpools.Delete(id); err != nil {
		s.writeError(w, err)
		return
	}
	s.replicateToMembers(r, http.MethodDelete, "/api/v1/gpu/pools/"+id, nil)
	w.WriteHeader(http.StatusNoContent)
}

// gpoolInUse says why pool id cannot go: machines hold its GPUs, or project quotas name it.
func (s *Server) gpoolInUse(ctx context.Context, id auth.Identity, poolID string) string {
	if s.projects != nil {
		for _, p := range s.projects.List() {
			if p.Limits.GPUs[poolID] > 0 {
				return "project " + p.Name + " has a GPU quota for this pool; remove it first"
			}
		}
	}
	domains, _ := s.clusterDomains(ctx, id)
	for _, d := range domains {
		if d.GPUPool == poolID {
			return "machine " + d.Name + " has GPUs of this pool; remove them first"
		}
	}
	return ""
}

// ClusterGPU is a GPU with the member it sits in and the pools it belongs to.
type ClusterGPU struct {
	hostinfo.GPU
	HostID   string   `json:"hostId"`
	HostName string   `json:"hostName"`
	Pools    []string `json:"pools"`
}

// hostHardware is what this host reports for the GPU pools.
func (s *Server) hostHardware(ctx context.Context) gpool.HostHardware {
	h := gpool.HostHardware{GPUs: []hostinfo.GPU{}}
	if s.hostinfo != nil {
		h.GPUs = s.hostinfo.GPUs(ctx)
	}
	if hd, ok := s.backend.(HostDevicer); ok {
		if hw, err := hd.HostHardware(); err == nil {
			h.PCI, h.MDevs = hw.PCI, hw.MDevs
		}
	}
	return withUsers(h)
}

// withUsers marks the cards machines hold, from libvirt's view of the PCI devices.
func withUsers(h gpool.HostHardware) gpool.HostHardware {
	for i := range h.GPUs {
		for _, p := range h.PCI {
			if strings.EqualFold(p.Address, h.GPUs[i].Address) && len(p.UsedBy) > 0 {
				h.GPUs[i].UsedBy = slices.Clone(p.UsedBy)
			}
		}
	}
	return h
}

// clusterHardware asks every member (this one in-process) for its GPUs and devices; a
// member that does not answer is left out.
func (s *Server) clusterHardware(ctx context.Context, id auth.Identity) []gpool.HostHardware {
	if s.cluster == nil || !s.cluster.Clustered() {
		h := s.hostHardware(ctx)
		h.HostName = s.hostName
		if s.cluster != nil {
			h.HostID = s.cluster.SelfID()
		}
		return []gpool.HostHardware{h}
	}
	nodes, _ := s.cluster.Members()
	out := make([]gpool.HostHardware, len(nodes))
	ok := make([]bool, len(nodes))
	var wg sync.WaitGroup
	for i, n := range nodes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if n.ID == s.cluster.SelfID() {
				out[i], ok[i] = s.hostHardware(ctx), true
			} else {
				var g struct{ GPUs []hostinfo.GPU }
				var hw virt.HostHardware
				if err := s.callHost(ctx, id, n.ID, http.MethodGet, "/api/v1/host/gpus", nil, &g); err != nil {
					return
				}
				_ = s.callHost(ctx, id, n.ID, http.MethodGet, "/api/v1/host/devices", nil, &hw) // optional
				out[i], ok[i] = withUsers(gpool.HostHardware{GPUs: g.GPUs, PCI: hw.PCI, MDevs: hw.MDevs}), true
			}
			out[i].HostID, out[i].HostName = n.ID, n.Name
		}()
	}
	wg.Wait()
	var res []gpool.HostHardware
	for i := range out {
		if ok[i] {
			res = append(res, out[i])
		}
	}
	return res
}

func (s *Server) clusterGPUs(w http.ResponseWriter, r *http.Request) {
	var pools []gpool.Pool
	if s.gpools != nil {
		pools = s.gpools.List()
	}
	out := []ClusterGPU{}
	for _, h := range s.clusterHardware(r.Context(), identity(r)) {
		for _, g := range h.GPUs {
			cg := ClusterGPU{GPU: g, HostID: h.HostID, HostName: h.HostName, Pools: []string{}}
			for _, p := range pools {
				if p.Matches(g, pciOf(h.PCI, g.Address)) && offers(p, g) {
					cg.Pools = append(cg.Pools, p.ID)
				}
			}
			out = append(out, cg)
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"gpus": out})
}

func pciOf(list []virt.HostPCI, addr string) *virt.HostPCI {
	for i := range list {
		if strings.EqualFold(list[i].Address, addr) {
			return &list[i]
		}
	}
	return nil
}

// offers reports whether card g can serve pool p at all (it has the slice type or profile).
func offers(p gpool.Pool, g hostinfo.GPU) bool {
	switch p.Kind {
	case gpool.KindMDev:
		return slices.ContainsFunc(g.MDevTypes, func(t hostinfo.MDevTypeInfo) bool { return t.ID == p.Profile })
	case gpool.KindMIG:
		return slices.ContainsFunc(g.MIGProfiles, func(m hostinfo.MIGProfile) bool { return m.ID == p.Profile })
	}
	return true
}
