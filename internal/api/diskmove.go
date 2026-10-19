package api

import (
	"context"
	"net/http"

	"github.com/onurbaydas/virtomir/internal/project"
)

// DiskMover moves a machine's disk into another storage pool on the same host.
type DiskMover interface {
	MoveDisk(ctx context.Context, uuid, target, pool string, report func(string)) error
}

// moveDisk starts a background job; running machines keep running (block copy).
func (s *Server) moveDisk(w http.ResponseWriter, r *http.Request) {
	m, ok := s.backend.(DiskMover)
	if !ok {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "moving disks is not available"})
		return
	}
	var body struct {
		Pool string `json:"pool"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if body.Pool == "" {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name the storage pool to move the disk to"})
		return
	}
	uuid, target := r.PathValue("uuid"), r.PathValue("target")
	// A project machine's disks stay in the project's pools (ADR-0060).
	id := identity(r)
	if !s.checkProject(w, id.Name, id.Role, s.projectOf(uuid), project.Usage{}, body.Pool, nil) {
		return
	}
	job := s.jobs.Start("disk-move", "Move "+target+" to "+body.Pool, func(ctx context.Context, report func(string)) (string, error) {
		err := m.MoveDisk(ctx, uuid, target, body.Pool, report)
		s.live.Refresh()
		return "", err
	})
	s.writeJSON(w, http.StatusAccepted, job)
}
