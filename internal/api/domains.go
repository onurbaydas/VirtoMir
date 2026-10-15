package api

import (
	"context"
	"io"
	"net/http"

	"github.com/onurbaydas/virtomir/internal/virt"
)

func (s *Server) domainRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/domains", s.listDomains)
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

func (s *Server) listDomains(w http.ResponseWriter, _ *http.Request) {
	doms, err := s.backend.ListDomains()
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"domains": doms})
}

func (s *Server) createDomain(w http.ResponseWriter, r *http.Request) {
	var spec virt.CreateSpec
	if !s.decode(w, r, &spec) {
		return
	}
	if !s.checkQuota(w, r, 1, max(spec.VCPUs, 1), max(spec.MemoryMiB, 1024)) {
		return
	}
	res, err := s.backend.CreateDomain(spec)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.recordOwner(r, res.UUID)
	s.done(w, http.StatusCreated, res)
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
	res, err := s.backend.Delete(r.PathValue("uuid"), r.URL.Query().Get("storage") == "remove")
	if err != nil {
		s.writeError(w, err)
		return
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
	_, _ = io.WriteString(w, x)
}

// putDomainXML saves an edited definition (raw XML body).
func (s *Server) putDomainXML(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not read the XML: " + err.Error()})
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

// clone copies disks in the background and answers 202 with the job to poll.
func (s *Server) clone(w http.ResponseWriter, r *http.Request) {
	var b nameBody
	if !s.decode(w, r, &b) {
		return
	}
	uuid := r.PathValue("uuid")
	job := s.jobs.Start("clone", "Clone to "+b.Name, func(ctx context.Context, report func(string)) (string, error) {
		res, err := s.backend.Clone(ctx, uuid, b.Name, report)
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
	if err := s.backend.AttachInterface(r.PathValue("uuid"), spec); err != nil {
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
	if err := s.backend.RevertSnapshot(r.PathValue("uuid"), r.PathValue("name")); err != nil {
		s.writeError(w, err)
		return
	}
	s.done(w, http.StatusNoContent, nil)
}

func (s *Server) deleteSnapshot(w http.ResponseWriter, r *http.Request) {
	if err := s.backend.DeleteSnapshot(r.PathValue("uuid"), r.PathValue("name")); err != nil {
		s.writeError(w, err)
		return
	}
	s.done(w, http.StatusNoContent, nil)
}
