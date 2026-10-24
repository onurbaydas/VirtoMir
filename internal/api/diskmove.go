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
	s.startDiskMove(w, r, m, r.PathValue("uuid"), r.PathValue("target"), body.Pool)
}

// startDiskMove starts the job that moves a disk into pool.
func (s *Server) startDiskMove(w http.ResponseWriter, r *http.Request, m DiskMover, uuid, target, pool string) {
	// A project machine's disks stay in the project's pools (ADR-0060).
	id := identity(r)
	if !s.checkProject(w, id.Name, id.Role, s.projectOf(uuid), project.Usage{}, pool, nil) {
		return
	}
	job := s.jobs.Start("disk-move", "Move "+target+" to "+pool, func(ctx context.Context, report func(string)) (string, error) {
		err := m.MoveDisk(ctx, uuid, target, pool, report)
		s.live.Refresh()
		return pool, err
	})
	s.writeJSON(w, http.StatusAccepted, job)
}
