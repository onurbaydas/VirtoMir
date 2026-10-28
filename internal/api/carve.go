package api

import (
	"context"
	"net/http"
	"time"

	"github.com/onurbaydas/virtomir/internal/carve"
	"github.com/onurbaydas/virtomir/internal/ceph"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// sources lists the free space a pool can be carved from: empty disks (as Ceph sees them:
// no partition, file system, label, mount or pool using them), volume groups and zpools.
func (s *Server) sources(ctx context.Context) ([]carve.Source, error) {
	disks, err := ceph.ListDisks(ctx, s.cephRunner)
	if err != nil {
		return nil, err
	}
	ceph.MarkUsed(disks, s.diskUser())
	var empty []carve.Disk
	for _, d := range disks {
		if d.Empty {
			empty = append(empty, carve.Disk{Path: d.Path, Type: d.DeviceType, SizeBytes: d.SizeBytes})
		}
	}
	return carve.Sources(ctx, s.cephRunner, empty), nil
}

func (s *Server) carveSources(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	src, err := s.sources(ctx)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"sources": src})
}

// carvePool makes a pool of the asked size from a source (a job: making the file system
// takes a while on a large volume).
func (s *Server) carvePool(w http.ResponseWriter, r *http.Request) {
	var req carve.Request
	if !s.decode(w, r, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	src, err := s.sources(ctx)
	cancel()
	if err == nil {
		err = carve.Check(req, src)
	}
	if err != nil {
		s.writeError(w, err)
		return
	}
	job := s.jobs.Start("carve-pool", "Create pool "+req.Name, func(ctx context.Context, report func(string)) (string, error) {
		if req.Kind == carve.KindDisk {
			if err := ceph.EnsureDiskEmpty(ctx, s.cephRunner, req.Source); err != nil {
				return "", err
			}
		}
		dir, err := carve.Carve(ctx, s.cephRunner, req, report)
		if err != nil {
			return "", err
		}
		report("Creating storage pool " + req.Name)
		return req.Name, s.backend.CreatePool(virt.PoolSpec{Name: req.Name, Path: dir})
	})
	s.writeJSON(w, http.StatusAccepted, job)
}
