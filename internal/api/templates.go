package api

import (
	"context"
	"net/http"

	"github.com/onurbaydas/virtomir/internal/virt"
)

// Templater is the template part of the backend (implemented by *virt.Client).
type Templater interface {
	SetTemplate(uuid string, on bool) error
	Deploy(ctx context.Context, template string, spec virt.DeploySpec, report func(string)) (string, error)
}

func (s *Server) templateRoutes(mux *http.ServeMux) {
	mux.HandleFunc("PUT /api/v1/domains/{uuid}/template", s.setTemplate)
	mux.HandleFunc("POST /api/v1/domains/{uuid}/deploy", s.deploy)
}

func (s *Server) templater(w http.ResponseWriter) (Templater, bool) {
	t, ok := s.backend.(Templater)
	if !ok {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "templates are not available"})
	}
	return t, ok
}

func (s *Server) setTemplate(w http.ResponseWriter, r *http.Request) {
	t, ok := s.templater(w)
	if !ok {
		return
	}
	var b struct {
		Template bool `json:"template"`
	}
	if !s.decode(w, r, &b) {
		return
	}
	if err := t.SetTemplate(r.PathValue("uuid"), b.Template); err != nil {
		s.writeError(w, err)
		return
	}
	s.done(w, http.StatusNoContent, nil)
}

// deploy creates a machine from a template in the background (full copies take minutes).
func (s *Server) deploy(w http.ResponseWriter, r *http.Request) {
	t, ok := s.templater(w)
	if !ok {
		return
	}
	var b virt.DeploySpec
	if !s.decode(w, r, &b) {
		return
	}
	uuid := r.PathValue("uuid")
	job := s.jobs.Start("deploy", "New machine "+b.Name, func(ctx context.Context, report func(string)) (string, error) {
		res, err := t.Deploy(ctx, uuid, b, report)
		s.live.Refresh()
		return res, err
	})
	s.writeJSON(w, http.StatusAccepted, job)
}
