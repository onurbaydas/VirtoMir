package api

import (
	"net/http"
	"strings"

	"github.com/onurbaydas/virtomir/internal/respool"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// Resource pools (ADR-0032): machines grouped under hard vCPU, memory and disk limits.
func (s *Server) respoolRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/respools", s.listRespools)
	mux.HandleFunc("POST /api/v1/respools", s.createRespool)
	mux.HandleFunc("PUT /api/v1/respools/{id}", s.updateRespool)
	mux.HandleFunc("DELETE /api/v1/respools/{id}", s.deleteRespool)
	mux.HandleFunc("PUT /api/v1/domains/{uuid}/respool", s.assignRespool)
}

func (s *Server) noRespools(w http.ResponseWriter) bool {
	if s.respools == nil {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "resource pools are not available on this host"})
		return true
	}
	return false
}

// machineUses is every machine's vCPUs, memory and disk capacity per storage pool, as the
// limits count them. Disks are found through the storage pools' volume lists.
func (s *Server) machineUses() (map[string]respool.MachineUse, error) {
	doms, err := s.backend.ListDomains()
	if err != nil {
		return nil, err
	}
	out := make(map[string]respool.MachineUse, len(doms))
	byName := map[string]string{}
	for _, d := range doms {
		u := strings.ToLower(d.UUID)
		out[u] = respool.MachineUse{VCPUs: d.VCPUs, MemoryMiB: d.MaxMemoryKiB >> 10, StorageBytes: map[string]uint64{}}
		byName[d.Name] = u
	}
	pools, err := s.backend.ListPools()
	if err != nil {
		return out, nil // limits on vCPUs and memory still work
	}
	for _, p := range pools {
		if !p.Active {
			continue
		}
		vols, err := s.backend.ListVolumes(p.Name)
		if err != nil {
			continue
		}
		for _, v := range vols {
			for _, user := range v.UsedBy {
				if u, ok := byName[user]; ok { // "x (as backing image)" does not count
					out[u].StorageBytes[p.Name] += v.CapacityBytes
				}
			}
		}
	}
	return out, nil
}

// usagesWithout adds up the pools, leaving out machine skip (the one being changed).
func (s *Server) usagesWithout(uses map[string]respool.MachineUse, skip string) map[string]respool.Usage {
	skip = strings.ToLower(skip)
	return s.respools.Usages(func(u string) (respool.MachineUse, bool) {
		if u == skip {
			return respool.MachineUse{}, false
		}
		m, ok := uses[u]
		return m, ok
	})
}

// checkRespool answers 409 and returns false when adding d to the pool of machine uuid (or
// to poolID for a new machine) would break a limit. Machines outside pools are unlimited.
func (s *Server) checkRespool(w http.ResponseWriter, poolID, uuid string, d respool.Demand) bool {
	if s.respools == nil {
		return true
	}
	if poolID == "" && uuid != "" {
		poolID = s.respools.PoolOf(uuid)
	}
	if poolID == "" {
		return true
	}
	chain := s.respools.Chain(poolID)
	if len(chain) == 0 {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "no resource pool " + poolID})
		return false
	}
	uses, err := s.machineUses()
	if err != nil {
		s.writeError(w, err)
		return false
	}
	if err := respool.Check(chain, s.respools.Usages(func(u string) (respool.MachineUse, bool) { m, ok := uses[u]; return m, ok }), d); err != nil {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return false
	}
	return true
}

// joinRespool puts a newly created machine into the pool it was checked against.
func (s *Server) joinRespool(poolID, uuid string) {
	if s.respools == nil || poolID == "" || uuid == "" {
		return
	}
	if err := s.respools.Assign(poolID, uuid, nil); err != nil {
		s.log.Warn("resource pool", "machine", uuid, "err", err)
	}
}

// PoolView is a pool with what its machines use.
type PoolView struct {
	respool.ResourcePool
	Usage respool.Usage `json:"usage"`
}

func (s *Server) listRespools(w http.ResponseWriter, _ *http.Request) {
	if s.respools == nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"pools": []PoolView{}})
		return
	}
	uses, err := s.machineUses()
	if err != nil {
		s.writeError(w, err)
		return
	}
	// Machines deleted outside VirtoMir (virsh) leave their pool here.
	for _, p := range s.respools.List() {
		for _, m := range p.Machines {
			if _, ok := uses[m]; !ok {
				s.respools.Forget(m)
			}
		}
	}
	usage := s.usagesWithout(uses, "")
	pools := s.respools.List()
	out := make([]PoolView, len(pools))
	for i, p := range pools {
		out[i] = PoolView{ResourcePool: p, Usage: usage[p.ID]}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"pools": out})
}

func (s *Server) createRespool(w http.ResponseWriter, r *http.Request) {
	if s.noRespools(w) {
		return
	}
	var p respool.ResourcePool
	if !s.decode(w, r, &p) {
		return
	}
	created, err := s.respools.Create(p)
	if err != nil {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	s.writeJSON(w, http.StatusCreated, created)
}

func (s *Server) updateRespool(w http.ResponseWriter, r *http.Request) {
	if s.noRespools(w) {
		return
	}
	var p respool.ResourcePool
	if !s.decode(w, r, &p) {
		return
	}
	p.ID = r.PathValue("id")
	if err := s.respools.Update(p); err != nil {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteRespool(w http.ResponseWriter, r *http.Request) {
	if s.noRespools(w) {
		return
	}
	if err := s.respools.Delete(r.PathValue("id")); err != nil {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// assignRespool moves a machine into a pool ({"pool": "<id>"}) or out of all ({"pool": ""}),
// if it fits the destination's limits.
func (s *Server) assignRespool(w http.ResponseWriter, r *http.Request) {
	if s.noRespools(w) {
		return
	}
	var b struct {
		Pool string `json:"pool"`
	}
	if !s.decode(w, r, &b) {
		return
	}
	uuid := strings.ToLower(r.PathValue("uuid"))
	uses, err := s.machineUses()
	if err != nil {
		s.writeError(w, err)
		return
	}
	me, ok := uses[uuid]
	if !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such machine on this host"})
		return
	}
	err = s.respools.Assign(b.Pool, uuid, func(chain []respool.ResourcePool) error {
		usage := s.usagesWithout(uses, uuid)
		if err := respool.Check(chain, usage, respool.Demand{VCPUs: me.VCPUs, MemoryMiB: me.MemoryMiB}); err != nil {
			return err
		}
		for sp, bytes := range me.StorageBytes {
			if err := respool.Check(chain, usage, respool.Demand{StoragePool: sp, StorageBytes: bytes}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// diskDemand is the storage a new disk adds (zero for existing images and CD-ROMs).
func diskDemand(spec virt.DiskSpec) respool.Demand {
	if spec.Device == "cdrom" || spec.Path != "" || spec.Volume != "" || spec.SizeGiB == 0 {
		return respool.Demand{}
	}
	return respool.Demand{StoragePool: spec.Pool, StorageBytes: spec.SizeGiB << 30}
}
