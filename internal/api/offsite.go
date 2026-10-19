package api

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/onurbaydas/virtomir/internal/offsite"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// Off-site copies of backups with restic (ADR-0043). Reading the list is for viewers like
// the local backups; the configuration (credentials) and every change are for admins.
func (s *Server) offsiteRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/offsite", s.getOffsite)
	mux.HandleFunc("PUT /api/v1/offsite", s.putOffsite)
	mux.HandleFunc("POST /api/v1/offsite/check", s.checkOffsite)
	mux.HandleFunc("GET /api/v1/offsite/snapshots", s.offsiteSnapshots)
	mux.HandleFunc("POST /api/v1/offsite/upload", s.offsiteUpload)
	mux.HandleFunc("POST /api/v1/offsite/restore", s.offsiteRestore)
}

func (s *Server) offsiteStore(w http.ResponseWriter) (*offsite.Store, bool) {
	if s.offsite == nil {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "off-site copies are not available on this host"})
		return nil, false
	}
	return s.offsite, true
}

func (s *Server) offsiteError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, offsite.ErrInvalid):
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
	case errors.Is(err, offsite.ErrRefused):
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	default:
		s.writeError(w, err)
	}
}

func (s *Server) getOffsite(w http.ResponseWriter, r *http.Request) {
	o, ok := s.offsiteStore(w)
	if !ok {
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"config": o.Config(), "status": o.Status(r.Context())})
}

func (s *Server) putOffsite(w http.ResponseWriter, r *http.Request) {
	o, ok := s.offsiteStore(w)
	if !ok {
		return
	}
	var c offsite.Config
	if !s.decode(w, r, &c) {
		return
	}
	if err := o.SetConfig(c); err != nil {
		s.offsiteError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"config": o.Config(), "status": o.Status(r.Context())})
}

func (s *Server) checkOffsite(w http.ResponseWriter, r *http.Request) {
	o, ok := s.offsiteStore(w)
	if !ok {
		return
	}
	var body struct {
		Init bool `json:"init"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	msg, err := o.Check(r.Context(), body.Init)
	if err != nil {
		s.offsiteError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"message": msg})
}

func (s *Server) offsiteSnapshots(w http.ResponseWriter, r *http.Request) {
	o, ok := s.offsiteStore(w)
	if !ok {
		return
	}
	list, err := o.Snapshots(r.Context())
	if err != nil {
		s.offsiteError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"snapshots": list})
}

func (s *Server) offsiteUpload(w http.ResponseWriter, r *http.Request) {
	o, ok := s.offsiteStore(w)
	if !ok {
		return
	}
	b, dir, ok := s.backups(w)
	if !ok {
		return
	}
	var body struct {
		Backup string `json:"backup"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	list, err := b.ListBackups(dir, "")
	if err != nil {
		s.writeError(w, err)
		return
	}
	i := slices.IndexFunc(list, func(x virt.Backup) bool { return x.ID == body.Backup })
	if i < 0 {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such backup"})
		return
	}
	bk := list[i]
	job := s.jobs.Start("offsite", "Copy off-site: "+bk.Domain, func(ctx context.Context, report func(string)) (string, error) {
		// An incremental backup is merged first, so the off-site copy stands alone.
		root, cleanup := string(dir), func() {}
		if f, ok := b.(Flattener); ok {
			var err error
			if root, cleanup, err = f.FlatCopy(ctx, dir, bk.ID, report); err != nil {
				return "", err
			}
		}
		defer cleanup()
		return o.Upload(ctx, root, bk.ID, bk.DomainUUID, bk.Domain, report)
	})
	s.writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) offsiteRestore(w http.ResponseWriter, r *http.Request) {
	o, ok := s.offsiteStore(w)
	if !ok {
		return
	}
	_, dir, ok := s.backups(w)
	if !ok {
		return
	}
	var body struct {
		Snapshot string `json:"snapshot"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	job := s.jobs.Start("offsite", "Bring back an off-site copy", func(ctx context.Context, report func(string)) (string, error) {
		return o.Restore(ctx, string(dir), body.Snapshot, report)
	})
	s.writeJSON(w, http.StatusAccepted, job)
}
