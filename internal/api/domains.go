package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/onurbaydas/virtomir/internal/auth"
	"github.com/onurbaydas/virtomir/internal/jobs"
	"github.com/onurbaydas/virtomir/internal/physnet"
	"github.com/onurbaydas/virtomir/internal/project"
	"github.com/onurbaydas/virtomir/internal/respool"
	"github.com/onurbaydas/virtomir/internal/virt"
)

func (s *Server) domainRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/domains", s.listDomains)
	mux.HandleFunc("GET /api/v1/containers/images", s.containerImages)
	mux.HandleFunc("GET /api/v1/host/confidential", s.confidentialKinds)
	mux.HandleFunc("POST /api/v1/domains", s.createDomain)
	mux.HandleFunc("GET /api/v1/domains/{uuid}", s.getDomain)
	mux.HandleFunc("DELETE /api/v1/domains/{uuid}", s.deleteDomain)
	mux.HandleFunc("GET /api/v1/domains/{uuid}/xml", s.getDomainXML)
	mux.HandleFunc("PUT /api/v1/domains/{uuid}/xml", s.putDomainXML)
	mux.HandleFunc("POST /api/v1/domains/{uuid}/actions/{action}", s.domainAction)
	mux.HandleFunc("PUT /api/v1/domains/{uuid}/autostart", s.setAutostart)
	mux.HandleFunc("POST /api/v1/domains/{uuid}/rename", s.rename)
	mux.HandleFunc("POST /api/v1/domains/{uuid}/clone", s.clone)

	mux.HandleFunc("PUT /api/v1/domains/{uuid}/media", s.changeMedia)
	mux.HandleFunc("POST /api/v1/domains/{uuid}/disks", s.attachDisk)
	mux.HandleFunc("POST /api/v1/domains/{uuid}/interfaces", s.attachInterface)
	mux.HandleFunc("POST /api/v1/domains/{uuid}/devices/detach", s.detachDevice)

	mux.HandleFunc("GET /api/v1/domains/{uuid}/snapshots", s.listSnapshots)
	mux.HandleFunc("POST /api/v1/domains/{uuid}/snapshots", s.createSnapshot)
	mux.HandleFunc("POST /api/v1/domains/{uuid}/snapshots/{name}/revert", s.revertSnapshot)
	mux.HandleFunc("DELETE /api/v1/domains/{uuid}/snapshots/{name}", s.deleteSnapshot)
}

func (s *Server) listDomains(w http.ResponseWriter, r *http.Request) {
	doms, err := s.backend.ListDomains()
	if err != nil {
		s.writeError(w, err)
		return
	}
	id := identity(r)
	if id.Role == auth.Member {
		filtered := []virt.Domain{}
		if s.projects != nil {
			myProjects := s.projects.ProjectsFor(id.Name, s.groupsOf(id.Name))
			allowed := make(map[string]bool, len(myProjects))
			for _, p := range myProjects {
				allowed[p.ID] = true
			}
			for _, d := range doms {
				if d.Project != "" && allowed[d.Project] {
					filtered = append(filtered, d)
				}
			}
		}
		doms = filtered
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"domains": doms})
}

func (s *Server) createDomain(w http.ResponseWriter, r *http.Request) {
	var spec struct {
		virt.CreateSpec
		// Project is the multi-tenancy project the machine belongs to (ADR-0060); empty = global.
		Project string `json:"project,omitempty"`
		// GPU asks for GPUs of a GPU pool (ADR-0071).
		GPU *GPURequest `json:"gpu,omitempty"`
		// StoragePolicy picks the pool of a new disk (ADR-0073); without it and without a
		// pool, the default policy does.
		StoragePolicy string `json:"storagePolicy,omitempty"`
		// IP gives the adapter a fixed address of an IP pool, set through cloud-init and
		// registered in DNS (ADR-0075).
		IP *IPRequest `json:"ip,omitempty"`
	}
	if !s.decode(w, r, &spec) {
		return
	}
	if spec.DiskPath == "" && spec.Pool == "" {
		pool, err := s.policyPool(spec.StoragePolicy)
		if err != nil {
			s.writeError(w, err)
			return
		}
		spec.Pool = pool
	}
	var newDisk uint64
	if spec.DiskPath == "" {
		newDisk = spec.DiskGiB // a new disk or a copy of a cloud image, grown to this size
	}
	if !s.checkQuota(w, r, 1, max(spec.VCPUs, 1), max(spec.MemoryMiB, 1024), newDisk) {
		return
	}
	// A project machine needs an operator+ role in the project, its pool and network on the
	// project's allowed lists, and room in the project's limits (ADR-0060).
	id := identity(r)
	pool := ""
	if spec.DiskPath == "" {
		pool = spec.Pool // only a newly created volume reaches into a pool
		if spec.DiskGiB > 0 && pool == "" {
			pool = "default" // CreateDomain puts new disks here when no pool is given
		}
	}
	var networks []string
	if spec.Network != nil {
		networks = []string{spec.Network.Network}
	}
	demand := project.Usage{
		Machines: 1, VCPUs: max(spec.VCPUs, 1), MemoryMiB: int64(max(spec.MemoryMiB, 1024)), DiskGiB: int64(newDisk),
	}
	if spec.GPU != nil {
		if spec.GPU.Count < 1 || spec.Kind == "container" {
			s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "a machine asks for one GPU or more (containers get none)"})
			return
		}
		demand.GPUs = map[string]int{spec.GPU.Pool: spec.GPU.Count}
	}
	if !s.checkProject(w, id.Name, id.Role, spec.Project, demand, pool, networks) {
		return
	}
	// ?respool=<id>: the machine goes into that resource pool if it fits its limits.
	rp := r.URL.Query().Get("respool")
	d := respool.Demand{VCPUs: max(spec.VCPUs, 1), MemoryMiB: max(spec.MemoryMiB, 1024)}
	if spec.DiskPath == "" && spec.DiskGiB > 0 {
		d.StoragePool, d.StorageBytes = spec.Pool, spec.DiskGiB<<30
	}
	if rp != "" && !s.checkRespool(w, rp, "", d) {
		return
	}
	if spec.Kind == "container" {
		s.createContainer(w, r, spec.CreateSpec, rp, spec.Project)
		return
	}
	// An adapter on an SDN subnet (ADR-0072) is plugged in after the machine exists: its
	// OVN port is named after the machine. The machine starts once it is plugged.
	if c, ok := s.backend.(interface {
		CheckCreate(virt.CreateSpec) error
	}); ok {
		if err := c.CheckCreate(spec.CreateSpec); err != nil { // name taken, disk in use, ...
			s.writeError(w, err)
			return
		}
	}
	var ovnNIC *virt.NICSpec
	startAfter := false
	if spec.Network != nil && spec.Network.OVNSubnet != "" {
		if spec.GPU != nil {
			s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "a GPU machine cannot start on an SDN subnet yet; add the adapter once it is created"})
			return
		}
		if !s.ovnNICAllowed(w, r, *spec.Network, spec.Project) {
			return
		}
		nic := *spec.Network
		ovnNIC, spec.Network = &nic, nil
		startAfter, spec.Start = spec.Start, false
	}
	var addr physnet.Allocation
	if spec.IP != nil {
		var ok bool
		if addr, ok = s.fixedAddress(w, &spec.CreateSpec, *spec.IP); !ok {
			return
		}
	}
	// The slow part (copying a cloud image, GPUs, the SDN port) runs as a job; the checks
	// above and CheckCreate already answered every mistake the caller can make.
	var res virt.CreateResult
	var createErr error
	job := s.jobs.Start("create", "Create machine "+spec.Name, func(ctx context.Context, _ func(string)) (string, error) {
		res, createErr = s.finishCreate(r.WithContext(ctx), spec.CreateSpec, spec.GPU, spec.IP, addr, ovnNIC, startAfter, rp, spec.Project)
		if createErr != nil {
			return "", createErr
		}
		s.live.Refresh()
		if res.StartError != "" {
			return res.UUID, fmt.Errorf("created, but it did not start: %s", res.StartError)
		}
		return res.UUID, nil
	})
	if r.URL.Query().Get("wait") != "true" {
		s.writeJSON(w, http.StatusAccepted, job)
		return
	}
	// ?wait=true: the answer integrations got before machines were created as jobs.
	for {
		if j, ok := s.jobs.Get(job.ID); !ok || j.State != jobs.Running {
			break
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
	if createErr != nil {
		s.writeError(w, createErr)
		return
	}
	s.writeJSON(w, http.StatusCreated, res)
}

// httpError is a failure that already has its status and message (the GPU placement
// answers its own); writeError passes it through.
type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return e.msg }

// finishCreate makes the machine and everything that hangs on it. It runs in a job, so
// it cannot answer the HTTP request itself.
func (s *Server) finishCreate(r *http.Request, spec virt.CreateSpec, gpu *GPURequest, ip *IPRequest, addr physnet.Allocation, ovnNIC *virt.NICSpec, startAfter bool, rp, projectID string) (virt.CreateResult, error) {
	var res virt.CreateResult
	if gpu != nil {
		rec := httptest.NewRecorder()
		var ok bool
		if res, ok = s.createWithGPUs(rec, r, spec, *gpu); !ok {
			s.dropAddress(ip, addr)
			var body struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &body)
			return res, &httpError{rec.Code, body.Error}
		}
	} else {
		var err error
		if res, err = s.backend.CreateDomain(spec); err != nil {
			s.dropAddress(ip, addr)
			return res, err
		}
	}
	if ip != nil {
		if msg := s.claimAddress(r.Context(), ip.Pool, addr, res.UUID); msg != "" && res.StartError == "" {
			res.StartError = msg
		}
	}
	s.recordOwner(r, res.UUID)
	s.recordProject(res.UUID, projectID)
	s.joinRespool(rp, res.UUID)
	if ovnNIC != nil {
		if err := s.plugOVN(r.Context(), res.UUID, *ovnNIC); err != nil {
			res.StartError = "created without its network adapter: " + err.Error()
		} else {
			s.pushSDN(r.Context())
			if startAfter {
				if err := s.backend.DomainAction(res.UUID, virt.ActionStart); err != nil {
					res.StartError = err.Error()
				}
			}
		}
	}
	return res, nil
}

// Containers (ADR-0055) are created in the background: the image may have to be downloaded.
type containerBackend interface {
	CreateContainer(ctx context.Context, spec virt.CreateSpec, report func(string)) (virt.CreateResult, error)
	ContainerSupport() error
}

func (s *Server) containers(w http.ResponseWriter) (containerBackend, bool) {
	cb, ok := s.backend.(containerBackend)
	if !ok {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "system containers are not available"})
	}
	return cb, ok
}

// createContainer answers 202 with the job; its result is the new container's UUID.
func (s *Server) createContainer(w http.ResponseWriter, r *http.Request, spec virt.CreateSpec, respool string, projectID string) {
	cb, ok := s.containers(w)
	if !ok {
		return
	}
	if err := cb.ContainerSupport(); err != nil {
		s.writeError(w, err)
		return
	}
	job := s.jobs.Start("create", "Create container "+spec.Name, func(ctx context.Context, report func(string)) (string, error) {
		res, err := cb.CreateContainer(ctx, spec, report)
		if err != nil {
			return "", err
		}
		s.recordOwner(r, res.UUID)
		s.recordProject(res.UUID, projectID)
		s.joinRespool(respool, res.UUID)
		s.live.Refresh()
		if res.StartError != "" {
			return res.UUID, fmt.Errorf("created, but it did not start: %s", res.StartError)
		}
		return res.UUID, nil
	})
	s.writeJSON(w, http.StatusAccepted, job)
}

// containerImages lists the images containers start from, and whether this host can run them.
func (s *Server) containerImages(w http.ResponseWriter, _ *http.Request) {
	cb, ok := s.containers(w)
	if !ok {
		return
	}
	out := map[string]any{"images": virt.ContainerImages, "available": true}
	if err := cb.ContainerSupport(); err != nil {
		out["available"], out["reason"] = false, err.Error()
	}
	s.writeJSON(w, http.StatusOK, out)
}

// confidentialKinds is the memory encryption (sev, sev-snp, tdx) new machines can have here.
func (s *Server) confidentialKinds(w http.ResponseWriter, _ *http.Request) {
	kinds := []string{}
	if b, ok := s.backend.(interface{ ConfidentialKinds() ([]string, error) }); ok {
		k, err := b.ConfidentialKinds()
		if err != nil {
			s.writeError(w, err)
			return
		}
		kinds = append(kinds, k...)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"kinds": kinds})
}

func (s *Server) getDomain(w http.ResponseWriter, r *http.Request) {
	d, err := s.backend.DomainDetail(r.PathValue("uuid"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, d)
}

// deleteDomain: ?storage=remove also deletes the machine's own disks.
func (s *Server) deleteDomain(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	res, err := s.backend.Delete(uuid, r.URL.Query().Get("storage") == "remove")
	if err != nil {
		s.writeError(w, err)
		return
	}
	if f, ok := s.backend.(Firewaller); ok {
		if err := f.RemoveFirewall(uuid); err != nil {
			s.log.Warn("firewall of deleted machine", "machine", uuid, "err", err)
		}
	}
	s.unplugOVN(r.Context(), uuid, "")
	s.releaseAddresses(r.Context(), uuid)
	// Its automatic snapshots and backups go with it (its backups stay until deleted).
	if s.schedules != nil {
		if n := s.schedules.DeleteForDomain(uuid); n > 0 {
			s.log.Info("schedules of deleted machine removed", "machine", uuid, "count", n)
		}
	}
	// A deleted machine leaves its resource pool, and HA must not bring it back elsewhere.
	if s.respools != nil {
		s.respools.Forget(uuid)
	}
	if s.ha != nil {
		if _, ok := s.ha.Policy(uuid); ok {
			if _, err := s.ha.DeletePolicy(uuid); err != nil {
				s.log.Warn("ha policy of deleted machine", "machine", uuid, "err", err)
			}
		}
	}
	s.done(w, http.StatusOK, res)
}

// getDomainXML returns the raw domain XML; ?inactive=true selects the next-boot config.
func (s *Server) getDomainXML(w http.ResponseWriter, r *http.Request) {
	inactive := r.URL.Query().Get("inactive") == "true"
	x, err := s.backend.DomainXML(r.PathValue("uuid"), inactive)
	if err != nil {
		s.writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if inactive {
		w.Header().Set("ETag", `"`+xmlVersion(x)+`"`) // sent back in If-Match when saving
	}
	_, _ = io.WriteString(w, x)
}

// putDomainXML saves an edited definition (raw XML body).
func (s *Server) putDomainXML(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not read the XML: " + err.Error()})
		return
	}
	if !s.checkVersion(w, r, r.PathValue("uuid")) {
		return
	}
	res, err := s.backend.DefineXML(r.PathValue("uuid"), string(body))
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.done(w, http.StatusOK, res)
}

func (s *Server) domainAction(w http.ResponseWriter, r *http.Request) {
	action, ok := virt.ParseAction(r.PathValue("action"))
	if !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown action " + r.PathValue("action")})
		return
	}
	if err := s.backend.DomainAction(r.PathValue("uuid"), action); err != nil {
		s.writeError(w, err)
		return
	}
	s.done(w, http.StatusAccepted, nil) // e.g. a shutdown is only a request to the guest
}

type toggle struct {
	Enabled bool `json:"enabled"`
}

func (s *Server) setAutostart(w http.ResponseWriter, r *http.Request) {
	var t toggle
	if !s.decode(w, r, &t) {
		return
	}
	if err := s.backend.SetAutostart(r.PathValue("uuid"), t.Enabled); err != nil {
		s.writeError(w, err)
		return
	}
	s.done(w, http.StatusNoContent, nil)
}

type nameBody struct {
	Name string `json:"name"`
}

func (s *Server) rename(w http.ResponseWriter, r *http.Request) {
	var b nameBody
	if !s.decode(w, r, &b) {
		return
	}
	if err := s.backend.Rename(r.PathValue("uuid"), b.Name); err != nil {
		s.writeError(w, err)
		return
	}
	s.done(w, http.StatusNoContent, nil)
}

// clone copies disks in the background and answers 202 with the job to poll. The copy
// stays in its source's project (or the one given) and must fit that project's limits.
func (s *Server) clone(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name    string `json:"name"`
		Project string `json:"project,omitempty"`
	}
	if !s.decode(w, r, &b) {
		return
	}
	uuid := r.PathValue("uuid")
	projectID := b.Project
	if projectID == "" {
		projectID = s.projectOf(uuid) // a copy stays in the project its source belongs to
	}
	id := identity(r)
	vcpus, mem := 0, uint64(0)
	if doms, err := s.backend.ListDomains(); err == nil {
		for _, d := range doms {
			if strings.EqualFold(d.UUID, uuid) {
				vcpus, mem = d.VCPUs, d.MaxMemoryKiB>>10
			}
		}
	}
	if !s.checkProject(w, id.Name, id.Role, projectID, project.Usage{
		Machines: 1, VCPUs: vcpus, MemoryMiB: int64(mem), DiskGiB: int64(s.diskGiB([]string{uuid})),
	}, "", nil) {
		return
	}
	job := s.jobs.Start("clone", "Clone to "+b.Name, func(ctx context.Context, report func(string)) (string, error) {
		res, err := s.backend.Clone(ctx, uuid, b.Name, report)
		if err == nil {
			s.recordProject(res, projectID)
		}
		s.live.Refresh()
		return res, err
	})
	s.writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) changeMedia(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Target string `json:"target"`
		Source string `json:"source"` // "" ejects
	}
	if !s.decode(w, r, &b) {
		return
	}
	if err := s.backend.ChangeMedia(r.PathValue("uuid"), b.Target, b.Source); err != nil {
		s.writeError(w, err)
		return
	}
	s.done(w, http.StatusNoContent, nil)
}

func (s *Server) attachDisk(w http.ResponseWriter, r *http.Request) {
	var spec virt.DiskSpec
	if !s.decode(w, r, &spec) {
		return
	}
	if !s.checkRespool(w, "", r.PathValue("uuid"), diskDemand(spec)) {
		return
	}
	target, err := s.backend.AttachDisk(r.PathValue("uuid"), spec)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.done(w, http.StatusCreated, map[string]string{"target": target})
}

func (s *Server) attachInterface(w http.ResponseWriter, r *http.Request) {
	var spec virt.NICSpec
	if !s.decode(w, r, &spec) {
		return
	}
	uuid := r.PathValue("uuid")
	if spec.OVNSubnet != "" {
		if !s.ovnNICAllowed(w, r, spec, s.projectOf(uuid)) {
			return
		}
		if err := s.plugOVN(r.Context(), uuid, spec); err != nil {
			s.writeError(w, err)
			return
		}
		s.pushSDN(r.Context())
		s.done(w, http.StatusCreated, nil)
		return
	}
	if err := s.backend.AttachInterface(uuid, spec); err != nil {
		s.writeError(w, err)
		return
	}
	s.done(w, http.StatusCreated, nil)
}

func (s *Server) detachDevice(w http.ResponseWriter, r *http.Request) {
	var ref virt.DeviceRef
	if !s.decode(w, r, &ref) {
		return
	}
	res, err := s.backend.DetachDevice(r.PathValue("uuid"), ref)
	if err != nil {
		s.writeError(w, err)
		return
	}
	if ref.Kind == "interface" && ref.MAC != "" {
		s.unplugOVN(r.Context(), r.PathValue("uuid"), ref.MAC)
	}
	s.done(w, http.StatusOK, res)
}

func (s *Server) listSnapshots(w http.ResponseWriter, r *http.Request) {
	snaps, err := s.backend.ListSnapshots(r.PathValue("uuid"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"snapshots": snaps})
}

func (s *Server) createSnapshot(w http.ResponseWriter, r *http.Request) {
	var spec virt.SnapshotSpec
	if !s.decode(w, r, &spec) {
		return
	}
	name, err := s.backend.CreateSnapshot(r.PathValue("uuid"), spec)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.done(w, http.StatusCreated, map[string]string{"name": name})
}

func (s *Server) revertSnapshot(w http.ResponseWriter, r *http.Request) {
	// ?safety=false skips the snapshot of the current state that is taken first.
	res, err := s.backend.RevertSnapshot(r.PathValue("uuid"), r.PathValue("name"), r.URL.Query().Get("safety") != "false")
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.done(w, http.StatusOK, res)
}

func (s *Server) deleteSnapshot(w http.ResponseWriter, r *http.Request) {
	if err := s.backend.DeleteSnapshot(r.PathValue("uuid"), r.PathValue("name")); err != nil {
		s.writeError(w, err)
		return
	}
	s.done(w, http.StatusNoContent, nil)
}
