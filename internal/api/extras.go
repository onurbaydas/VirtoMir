package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/onurbaydas/virtomir/internal/auth"
	"github.com/onurbaydas/virtomir/internal/history"
	"github.com/onurbaydas/virtomir/internal/project"
	"github.com/onurbaydas/virtomir/internal/respool"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// HistoryQuerier answers history queries (implemented by *history.Store).
type HistoryQuerier interface {
	Query(names []string, rng time.Duration, now time.Time) (history.Result, error)
}

func (s *Server) extraRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/domains/{uuid}/settings", s.getSettings)
	mux.HandleFunc("POST /api/v1/domains/{uuid}/settings/preview", s.previewSettings)
	mux.HandleFunc("PUT /api/v1/domains/{uuid}/settings", s.saveSettings)
	mux.HandleFunc("POST /api/v1/domains/{uuid}/disks/{target}/resize", s.resizeDisk)
	mux.HandleFunc("PUT /api/v1/domains/{uuid}/disks/{target}/qos", s.setDiskQoS)
	mux.HandleFunc("POST /api/v1/domains/{uuid}/disks/{target}/move", s.moveDisk)
	mux.HandleFunc("POST /api/v1/storage/pools/{pool}/volumes/{vol}/resize", s.resizeVolume)
	mux.HandleFunc("POST /api/v1/networks/{name}/dhcp-hosts", s.addDHCPHost)
	mux.HandleFunc("DELETE /api/v1/networks/{name}/dhcp-hosts/{mac}", s.deleteDHCPHost)
	mux.HandleFunc("GET /api/v1/history", s.getHistory)
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	v, err := s.backend.GetSettings(r.PathValue("uuid"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, v)
}

// previewSettings returns the definition before and after the form's change.
func (s *Server) previewSettings(w http.ResponseWriter, r *http.Request) {
	var set virt.Settings
	if !s.decode(w, r, &set) {
		return
	}
	p, err := s.backend.PreviewSettings(r.PathValue("uuid"), set)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, p)
}

func (s *Server) saveSettings(w http.ResponseWriter, r *http.Request) {
	var set virt.Settings
	if !s.decode(w, r, &set) {
		return
	}
	var vcpus int
	var mem uint64
	if doms, err := s.backend.ListDomains(); err == nil {
		for _, d := range doms {
			if strings.EqualFold(d.UUID, r.PathValue("uuid")) {
				vcpus, mem = d.VCPUs, d.MaxMemoryKiB>>10
			}
		}
	}
	addCPU, addMem := max(int(set.MaxVCPUs)-vcpus, 0), uint64(max(int64(set.MemoryMiB)-int64(mem), 0))
	// An owner growing their machine stays within their quota; everyone within its pool.
	if identity(r).Role < auth.Admin && (addCPU > 0 || addMem > 0) && !s.checkQuota(w, r, 0, addCPU, addMem, 0) {
		return
	}
	if (addCPU > 0 || addMem > 0) && !s.checkRespool(w, "", r.PathValue("uuid"), respool.Demand{VCPUs: addCPU, MemoryMiB: addMem}) {
		return
	}
	// A project machine may only grow within its project's limits (ADR-0060).
	if addCPU > 0 || addMem > 0 {
		id := identity(r)
		if !s.checkProject(w, id.Name, id.Role, s.projectOf(r.PathValue("uuid")), project.Usage{VCPUs: addCPU, MemoryMiB: int64(addMem)}, "", nil) {
			return
		}
	}
	res, err := s.backend.SaveSettings(r.PathValue("uuid"), set)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.done(w, http.StatusOK, res)
}

// diskGrowth is what growing a machine's disk to sizeGiB adds to its storage pool (ok=false
// when the disk is not a volume of a pool, so no pool limit applies).
func (s *Server) diskGrowth(uuid, target string, sizeGiB uint64) (respool.Demand, bool) {
	if s.respools == nil || s.respools.PoolOf(uuid) == "" {
		return respool.Demand{}, false
	}
	det, err := s.backend.DomainDetail(uuid)
	if err != nil {
		return respool.Demand{}, false
	}
	var path string
	var cur uint64
	for _, dev := range det.Devices {
		if dev.Target == target {
			cur = dev.CapacityBytes
			for _, p := range dev.Props {
				if p.Label == "Source" {
					path = p.Value
				}
			}
		}
	}
	if path == "" || sizeGiB<<30 <= cur {
		return respool.Demand{}, false
	}
	pools, err := s.backend.ListPools()
	if err != nil {
		return respool.Demand{}, false
	}
	for _, p := range pools {
		vols, err := s.backend.ListVolumes(p.Name)
		if err != nil {
			continue
		}
		for _, v := range vols {
			if v.Path == path {
				return respool.Demand{StoragePool: p.Name, StorageBytes: sizeGiB<<30 - cur}, true
			}
		}
	}
	return respool.Demand{}, false
}

type sizeBody struct {
	SizeGiB uint64 `json:"sizeGiB"`
}

func (s *Server) resizeDisk(w http.ResponseWriter, r *http.Request) {
	var b sizeBody
	if !s.decode(w, r, &b) {
		return
	}
	uuid := r.PathValue("uuid")
	if d, ok := s.diskGrowth(uuid, r.PathValue("target"), b.SizeGiB); ok {
		if !s.checkRespool(w, "", uuid, d) {
			return
		}
		// Growing a project machine's disk must fit its project's limits (ADR-0060).
		if g := (d.StorageBytes + 1<<30 - 1) >> 30; g > 0 {
			id := identity(r)
			if !s.checkProject(w, id.Name, id.Role, s.projectOf(uuid), project.Usage{DiskGiB: int64(g)}, "", nil) {
				return
			}
		}
	}
	if err := s.backend.ResizeDisk(uuid, r.PathValue("target"), b.SizeGiB); err != nil {
		s.writeError(w, err)
		return
	}
	s.done(w, http.StatusNoContent, nil)
}

func (s *Server) setDiskQoS(w http.ResponseWriter, r *http.Request) {
	var qos virt.DiskIoTune
	if !s.decode(w, r, &qos) {
		return
	}
	if err := s.backend.SetDiskQoS(r.PathValue("uuid"), r.PathValue("target"), qos); err != nil {
		s.writeError(w, err)
		return
	}
	s.done(w, http.StatusNoContent, nil)
}

func (s *Server) resizeVolume(w http.ResponseWriter, r *http.Request) {
	var b sizeBody
	if !s.decode(w, r, &b) {
		return
	}
	if err := s.backend.ResizeVolume(r.PathValue("pool"), r.PathValue("vol"), b.SizeGiB); err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) addDHCPHost(w http.ResponseWriter, r *http.Request) {
	var h virt.DHCPHost
	if !s.decode(w, r, &h) {
		return
	}
	if err := s.backend.AddDHCPHost(r.PathValue("name"), h); err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) deleteDHCPHost(w http.ResponseWriter, r *http.Request) {
	if err := s.backend.DeleteDHCPHost(r.PathValue("name"), r.PathValue("mac")); err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// historyRanges are the ranges the UI offers; anything else is refused.
var historyRanges = map[string]time.Duration{
	"1h": time.Hour, "6h": 6 * time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour,
}

// getHistory: GET /api/v1/history?series=host.cpu,vm.<uuid>.cpu&range=24h
func (s *Server) getHistory(w http.ResponseWriter, r *http.Request) {
	if s.history == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "history is not enabled"})
		return
	}
	rng, ok := historyRanges[r.URL.Query().Get("range")]
	if !ok {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "range must be one of 1h, 6h, 24h, 7d, 30d"})
		return
	}
	var names []string
	for _, n := range strings.Split(r.URL.Query().Get("series"), ",") {
		if n = strings.TrimSpace(n); n != "" {
			names = append(names, n)
		}
	}
	if len(names) == 0 || len(names) > 64 {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ask for 1 to 64 series"})
		return
	}
	res, err := s.history.Query(names, rng, time.Now())
	if err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.writeJSON(w, http.StatusOK, res)
}
