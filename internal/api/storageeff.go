package api

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/onurbaydas/virtomir/internal/ceph"
	"github.com/onurbaydas/virtomir/internal/storageeff"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// Storage efficiency (D15, ADR-0074): what compression and deduplication save per pool
// (ZFS here; Ceph RBD on the member that manages the cluster), compacting stopped
// machines' disk images, storage tiers as pool labels that disks move between, and NVMe/TCP
// subsystems whose namespaces become pools. Tiers are cluster-wide; the rest is per host.

func (s *Server) storageEffRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/storage/pools/{pool}/efficiency", s.poolEfficiency)
	mux.HandleFunc("PUT /api/v1/storage/pools/{pool}/zfs", s.setPoolZFS)
	mux.HandleFunc("PUT /api/v1/ceph/pools/{name}/compression", s.cephCompression)
	mux.HandleFunc("POST /api/v1/domains/{uuid}/disks/{target}/sparsify", s.sparsifyDisk)
	mux.HandleFunc("POST /api/v1/domains/{uuid}/disks/{target}/tier", s.moveDiskToTier)
	mux.HandleFunc("GET /api/v1/storage/tiers", s.listTiers)
	mux.HandleFunc("PUT /api/v1/storage/tiers/{pool}", s.setTier)
	mux.HandleFunc("GET /api/v1/storage/nvme", s.listNVMe)
	mux.HandleFunc("POST /api/v1/storage/nvme/discover", s.discoverNVMe)
	mux.HandleFunc("POST /api/v1/storage/nvme/connect", s.connectNVMe)
	mux.HandleFunc("POST /api/v1/storage/nvme/disconnect", s.disconnectNVMe)
}

func (s *Server) noStorageEff(w http.ResponseWriter) bool {
	if s.storageEff == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "storage efficiency is not available on this host"})
		return true
	}
	return false
}

// PoolEfficiency is what a pool saves: ZFS compression/dedup, or Ceph compression when
// this host manages the cluster; Kind "none" for pools without either.
type PoolEfficiency struct {
	Pool string                    `json:"pool"`
	Kind string                    `json:"kind"` // zfs, ceph or none
	ZFS  *storageeff.ZFSEfficiency `json:"zfs,omitempty"`
	Ceph *ceph.PoolDetail          `json:"ceph,omitempty"`
	Note string                    `json:"note,omitempty"`
}

func (s *Server) poolFor(w http.ResponseWriter, name string) (virt.Pool, bool) {
	pools, err := s.backend.ListPools()
	if err != nil {
		s.writeError(w, err)
		return virt.Pool{}, false
	}
	i := slices.IndexFunc(pools, func(p virt.Pool) bool { return p.Name == name })
	if i < 0 {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no storage pool " + name})
		return virt.Pool{}, false
	}
	return pools[i], true
}

func (s *Server) poolEfficiency(w http.ResponseWriter, r *http.Request) {
	if s.noStorageEff(w) {
		return
	}
	p, ok := s.poolFor(w, r.PathValue("pool"))
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	out := PoolEfficiency{Pool: p.Name, Kind: "none"}
	switch p.Type {
	case "zfs":
		e, err := storageeff.InspectZFS(ctx, s.effRunner, p.Source)
		if err != nil {
			s.writeError(w, err)
			return
		}
		out.Kind, out.ZFS = "zfs", &e
	case "rbd":
		out.Kind = "ceph"
		if _, managed := s.cephConfig(); !managed {
			out.Note = "Compression of Ceph pools is shown and set on the member that manages the cluster."
			break
		}
		pools, err := ceph.ListRBDPools(ctx, s.cephRunner)
		if err != nil {
			s.writeError(w, err)
			return
		}
		if i := slices.IndexFunc(pools, func(c ceph.PoolDetail) bool { return c.Name == p.Source }); i >= 0 {
			out.Ceph = &pools[i]
		}
	}
	s.writeJSON(w, http.StatusOK, out)
}

func (s *Server) cephConfig() (ceph.ClusterConfig, bool) {
	if s.ceph == nil {
		return ceph.ClusterConfig{}, false
	}
	return s.ceph.Config()
}

func (s *Server) setPoolZFS(w http.ResponseWriter, r *http.Request) {
	if s.noStorageEff(w) {
		return
	}
	var body storageeff.ZFSSettings
	if !s.decode(w, r, &body) {
		return
	}
	p, ok := s.poolFor(w, r.PathValue("pool"))
	if !ok {
		return
	}
	if p.Type != "zfs" {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": p.Name + " is not a ZFS pool"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := storageeff.SetZFS(ctx, s.effRunner, p.Source, body); err != nil {
		s.writeError(w, err)
		return
	}
	e, err := storageeff.InspectZFS(ctx, s.effRunner, p.Source)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, e)
}

func (s *Server) cephCompression(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.cephManaged(w); !ok {
		return
	}
	var body struct {
		Mode      string `json:"mode"`
		Algorithm string `json:"algorithm"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	pools, err := ceph.ListRBDPools(ctx, s.cephRunner)
	if err != nil {
		s.writeError(w, err)
		return
	}
	i := slices.IndexFunc(pools, func(p ceph.PoolDetail) bool { return p.Name == r.PathValue("name") })
	if i < 0 {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no RBD pool " + r.PathValue("name")})
		return
	}
	if err := ceph.SetCompression(ctx, s.cephRunner, pools[i].Name, pools[i].DataPool, body.Mode, body.Algorithm); err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DiskSparsifier compacts a stopped machine's disk image.
type DiskSparsifier interface {
	SparsifyDisk(ctx context.Context, uuid, target string, report func(string)) (uint64, error)
}

func (s *Server) sparsifyDisk(w http.ResponseWriter, r *http.Request) {
	sp, ok := s.backend.(DiskSparsifier)
	if !ok {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "compacting disks is not available"})
		return
	}
	uuid, target := r.PathValue("uuid"), r.PathValue("target")
	// Refuse a running machine now, not in a job that fails a moment later.
	if d, err := s.backend.DomainDetail(uuid); err != nil {
		s.writeError(w, err)
		return
	} else if d.State != virt.StateShutoff {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": d.Name + " is " + string(d.State) + ": shut it down first. A disk is only compacted while nothing writes to it."})
		return
	}
	job := s.jobs.Start("disk-sparsify", "Compact "+target, func(ctx context.Context, report func(string)) (string, error) {
		freed, err := sp.SparsifyDisk(ctx, uuid, target, report)
		s.live.Refresh()
		return fmt.Sprint(freed), err
	})
	s.writeJSON(w, http.StatusAccepted, job)
}

// moveDiskToTier moves a disk into the pool of a tier with the most free space here.
func (s *Server) moveDiskToTier(w http.ResponseWriter, r *http.Request) {
	if s.noStorageEff(w) {
		return
	}
	m, ok := s.backend.(DiskMover)
	if !ok {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "moving disks is not available"})
		return
	}
	var body struct {
		Tier string `json:"tier"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	uuid, target := r.PathValue("uuid"), r.PathValue("target")
	d, err := s.backend.DomainDetail(uuid)
	if err != nil {
		s.writeError(w, err)
		return
	}
	var need uint64
	for _, dev := range d.Devices {
		if dev.Target == target {
			need = dev.CapacityBytes
		}
	}
	pools, err := s.backend.ListPools()
	if err != nil {
		s.writeError(w, err)
		return
	}
	pool, why := pickTierPool(pools, s.storageEff.Tiers(), body.Tier, need)
	if pool == "" {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": why})
		return
	}
	s.startDiskMove(w, r, m, uuid, target, pool)
}

// pickTierPool is the active file pool of tier with the most free space that has room for
// need bytes; why says what is missing when there is none.
func pickTierPool(pools []virt.Pool, tiers map[string]string, tier string, need uint64) (string, string) {
	if !slices.Contains(storageeff.Tiers, tier) {
		return "", "the tier is nvme, ssd or hdd"
	}
	best := -1
	for i, p := range pools {
		if tiers[p.Name] != tier || !p.Active || !slices.Contains([]string{"dir", "fs", "netfs"}, p.Type) || p.AvailableBytes < need {
			continue
		}
		if best < 0 || p.AvailableBytes > pools[best].AvailableBytes {
			best = i
		}
	}
	if best < 0 {
		return "", "no active file pool of tier " + tier + " on this host has room for the disk (label pools with a tier on the Storage page)"
	}
	return pools[best].Name, ""
}

func (s *Server) listTiers(w http.ResponseWriter, _ *http.Request) {
	if s.noStorageEff(w) {
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"tiers": s.storageEff.Tiers()})
}

func (s *Server) setTier(w http.ResponseWriter, r *http.Request) {
	if s.noStorageEff(w) {
		return
	}
	var body struct {
		Tier string `json:"tier"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	pool := r.PathValue("pool")
	if err := s.storageEff.SetTier(pool, body.Tier); err != nil {
		s.writeError(w, err)
		return
	}
	s.replicateToMembers(r, http.MethodPut, "/api/v1/storage/tiers/"+pool, body)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listNVMe(w http.ResponseWriter, r *http.Request) {
	if s.noStorageEff(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	subs, err := storageeff.List(ctx, s.effRunner, s.storageEff)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"subsystems": subs})
}

func (s *Server) discoverNVMe(w http.ResponseWriter, r *http.Request) {
	if s.noStorageEff(w) {
		return
	}
	var body struct {
		Portal string `json:"portal"`
		Port   int    `json:"port,omitempty"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	found, err := storageeff.Discover(ctx, s.effRunner, body.Portal, body.Port)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"targets": found})
}

func (s *Server) connectNVMe(w http.ResponseWriter, r *http.Request) {
	if s.noStorageEff(w) {
		return
	}
	var t storageeff.Target
	if !s.decode(w, r, &t) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if err := storageeff.Connect(ctx, s.effRunner, s.storageEff, t); err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) disconnectNVMe(w http.ResponseWriter, r *http.Request) {
	if s.noStorageEff(w) {
		return
	}
	var body struct {
		NQN string `json:"nqn"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if err := storageeff.Disconnect(ctx, s.effRunner, s.storageEff, body.NQN); err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
