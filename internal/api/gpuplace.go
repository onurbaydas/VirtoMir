package api

import (
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/onurbaydas/virtomir/internal/gpool"
	"github.com/onurbaydas/virtomir/internal/placement"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// GPU-aware placement and creation (ADR-0071). A machine asks for count GPUs of a pool;
// POST /api/v1/gpu/placement names the member that has them, and creating the machine on
// that member gives it the cards (or vGPU slices) and records them in its <metadata>.

// GPURequest is the GPUs a new machine asks for.
type GPURequest struct {
	Pool  string `json:"pool"`
	Count int    `json:"count"`
}

// gpuBackend hands GPUs to a machine (*virt.Client).
type gpuBackend interface {
	HostDevicer
	CreateMDev(spec virt.MDevSpec) (string, error)
	DeleteMDev(uuid string) error
	AddDevice(uuid string, spec virt.DeviceSpec) (virt.DeviceResult, error)
	SetGPUPool(uuid, pool string, count int) error
	DomainAction(uuid string, a virt.Action) error
	Delete(uuid string, removeStorage bool) (virt.DeleteResult, error)
}

// gpuMu keeps two machine creations on this host from taking the same free GPU.
var gpuMu sync.Mutex

func (s *Server) gpuPlacement(w http.ResponseWriter, r *http.Request) {
	if s.noGPools(w) {
		return
	}
	var b struct {
		PoolID string `json:"poolId"`
		Count  int    `json:"count"`
		UUID   string `json:"uuid,omitempty"`
	}
	if !s.decode(w, r, &b) {
		return
	}
	p, ok := s.gpools.Get(b.PoolID)
	if !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no GPU pool " + b.PoolID})
		return
	}
	if b.Count < 1 {
		b.Count = 1
	}
	cands := gpool.Candidates(p, s.clusterHardware(r.Context(), identity(r)), b.Count, func(h string) int { return s.gpools.Reserved(p.ID, h) })
	// A machine that exists already follows its placement rules.
	if b.UUID != "" && s.placement != nil && len(cands) > 0 {
		_, domains := s.collectClusterPlacements(r.Context(), identity(r))
		vms := make([]placement.VMRef, len(domains))
		for i, d := range domains {
			vms[i] = placement.VMRef{UUID: d.UUID, Name: d.Name, HostID: d.HostID, Running: d.State == "running"}
		}
		ids := make([]string, len(cands))
		for i, c := range cands {
			ids[i] = c.HostID
		}
		ranked, _ := placement.Rank(s.placement.Rules(), vms, b.UUID, ids)
		var kept []gpool.Candidate
		for _, id := range ranked {
			for _, c := range cands {
				if c.HostID == id {
					kept = append(kept, c)
				}
			}
		}
		cands = kept
	}
	if len(cands) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"best": cands[0], "candidates": cands})
}

// createWithGPUs creates the machine stopped, gives it its GPUs, records them and starts it
// if asked. Anything that fails removes the new machine again.
func (s *Server) createWithGPUs(w http.ResponseWriter, r *http.Request, spec virt.CreateSpec, req GPURequest) (virt.CreateResult, bool) {
	gb, ok := s.backend.(gpuBackend)
	if !ok || s.gpools == nil {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "GPUs from GPU pools are not available on this host"})
		return virt.CreateResult{}, false
	}
	p, ok := s.gpools.Get(req.Pool)
	if !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no GPU pool " + req.Pool})
		return virt.CreateResult{}, false
	}
	gpuMu.Lock()
	defer gpuMu.Unlock()
	hw := s.hostHardware(r.Context())
	hw.HostName = s.hostName
	if s.cluster != nil {
		hw.HostID = s.cluster.SelfID()
	}
	free := gpool.Availability(p, hw).Free - s.gpools.Reserved(p.ID, hw.HostID)
	if free < req.Count {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("this host has %d free GPUs of pool %s, the machine asks for %d; create it on the member GPU placement suggests", max(free, 0), p.ID, req.Count)})
		return virt.CreateResult{}, false
	}
	picks, err := gpool.Allocate(p, hw, req.Count)
	if err != nil {
		s.writeError(w, err)
		return virt.CreateResult{}, false
	}
	start := spec.Start
	spec.Start = false
	res, err := s.backend.CreateDomain(spec)
	if err != nil {
		s.writeError(w, err)
		return virt.CreateResult{}, false
	}
	_ = s.gpools.Reserve(res.UUID, p.ID, hw.HostID, req.Count)
	defer s.gpools.Release(res.UUID)
	var made []string // slices created here, removed again on failure
	fail := func(err error) (virt.CreateResult, bool) {
		for _, m := range made {
			_ = gb.DeleteMDev(m)
		}
		_, _ = gb.Delete(res.UUID, true)
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "the machine could not get its GPUs, so it was removed again: " + err.Error()})
		return virt.CreateResult{}, false
	}
	for _, a := range picks {
		switch {
		case a.PCI != "":
			if _, err := gb.AttachHostdev(res.UUID, virt.HostdevSpec{Type: "pci", Address: a.PCI}); err != nil {
				return fail(err)
			}
		default:
			id := a.MDevUUID
			if id == "" {
				if id, err = gb.CreateMDev(virt.MDevSpec{Parent: a.Parent, Type: a.Type}); err != nil {
					return fail(err)
				}
				made = append(made, id)
			}
			if _, err := gb.AddDevice(res.UUID, virt.DeviceSpec{Kind: "mdev", UUID: id}); err != nil {
				return fail(err)
			}
		}
	}
	if err := gb.SetGPUPool(res.UUID, p.ID, req.Count); err != nil {
		return fail(err)
	}
	if start {
		if err := gb.DomainAction(res.UUID, virt.ActionStart); err != nil {
			res.StartError = err.Error()
		}
	}
	s.log.Info("machine got GPUs", "uuid", res.UUID, "pool", p.ID, "count", req.Count)
	return res, true
}

// gpuMoveRefused: a machine with pool GPUs holds this host's cards; its definition names
// them, so it cannot simply run elsewhere.
func (s *Server) gpuMoveRefused(w http.ResponseWriter, uuid string) bool {
	doms, err := s.backend.ListDomains()
	if err != nil {
		return false
	}
	for _, d := range doms {
		if strings.EqualFold(d.UUID, uuid) && d.GPUPool != "" {
			s.writeJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("%s has %d GPUs of pool %s on this host; remove them before moving it, and give it GPUs again on the other host", d.Name, d.GPUCount, d.GPUPool)})
			return true
		}
	}
	return false
}
