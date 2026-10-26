package api

import (
	"context"
	"net/http"

	"github.com/onurbaydas/virtomir/internal/schedule"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// Backupper is the backup part of the backend (implemented by *virt.Client).
type Backupper interface {
	ListBackups(dir virt.BackupDir, uuid string) ([]virt.Backup, error)
	BackupDomain(ctx context.Context, dir virt.BackupDir, uuid string, opts virt.BackupOptions, report func(string)) (virt.Backup, error)
	DeleteBackup(dir virt.BackupDir, id string) error
	RestoreBackup(ctx context.Context, dir virt.BackupDir, id, name, pool string, force bool, report func(string)) (string, error)
	VerifyBackup(ctx context.Context, dir virt.BackupDir, id string, report func(string)) (virt.Backup, error)
}

// Flattener merges an incremental backup's chain for an off-site copy (*virt.Client).
type Flattener interface {
	FlatCopy(ctx context.Context, dir virt.BackupDir, id string, report func(string)) (string, func(), error)
}

func (s *Server) backupRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/backups", s.listBackups)
	mux.HandleFunc("DELETE /api/v1/backups", s.deleteBackup)
	mux.HandleFunc("POST /api/v1/backups/restore", s.restoreBackup)
	mux.HandleFunc("POST /api/v1/backups/verify", s.verifyBackup)
	mux.HandleFunc("GET /api/v1/backups/settings", s.getBackupSettings)
	mux.HandleFunc("PUT /api/v1/backups/settings", s.putBackupSettings)
	mux.HandleFunc("POST /api/v1/domains/{uuid}/backup", s.backupNow)

	mux.HandleFunc("GET /api/v1/schedules", s.listSchedules)
	mux.HandleFunc("PUT /api/v1/schedules", s.putSchedule)
	mux.HandleFunc("DELETE /api/v1/schedules/{id}", s.deleteSchedule)
	mux.HandleFunc("POST /api/v1/schedules/{id}/run", s.runSchedule)
}

func (s *Server) backups(w http.ResponseWriter) (Backupper, virt.BackupDir, bool) {
	b, ok := s.backend.(Backupper)
	if !ok || s.schedules == nil {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "backups are not available"})
		return nil, "", false
	}
	return b, virt.BackupDir(s.schedules.Settings().BackupDir), true
}

// listBackups: ?domain=<uuid> for one machine.
func (s *Server) listBackups(w http.ResponseWriter, r *http.Request) {
	b, dir, ok := s.backups(w)
	if !ok {
		return
	}
	list, err := b.ListBackups(dir, r.URL.Query().Get("domain"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	out := map[string]any{"backups": list, "dir": dir}
	if free, total, ok := virt.BackupSpace(dir); ok {
		out["freeBytes"], out["totalBytes"] = free, total
	}
	out["onSystemDisk"] = virt.OnSystemDisk(dir)
	if old := s.schedules.Settings().OldBackupDirs; len(old) > 0 {
		out["oldDirs"] = old
	}
	s.writeJSON(w, http.StatusOK, out)
}

// verifyBackup checks a backup's files again (qemu-img check, recorded checksums) as a job.
func (s *Server) verifyBackup(w http.ResponseWriter, r *http.Request) {
	b, dir, ok := s.backups(w)
	if !ok {
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	job := s.jobs.Start("verify", "Verify backup", func(ctx context.Context, report func(string)) (string, error) {
		res, err := b.VerifyBackup(ctx, dir, body.ID, report)
		if err != nil {
			return "", err
		}
		return res.ID + " is intact", nil
	})
	s.writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) backupNow(w http.ResponseWriter, r *http.Request) {
	b, dir, ok := s.backups(w)
	if !ok {
		return
	}
	// Optional body {"incremental": true}: only what changed since the previous backup.
	var body struct {
		Incremental bool `json:"incremental"`
	}
	if r.ContentLength != 0 && !s.decode(w, r, &body) {
		return
	}
	opts := virt.BackupOptions{}
	if body.Incremental {
		opts.FullEvery = virt.MaxManualChain
	}
	uuid := r.PathValue("uuid")
	job := s.jobs.Start("backup", "Backup", func(ctx context.Context, report func(string)) (string, error) {
		res, err := b.BackupDomain(ctx, dir, uuid, opts, report)
		return res.ID, err
	})
	s.writeJSON(w, http.StatusAccepted, job)
}

// deleteBackup: ?id=<machine folder>/<timestamp>
func (s *Server) deleteBackup(w http.ResponseWriter, r *http.Request) {
	b, dir, ok := s.backups(w)
	if !ok {
		return
	}
	if err := b.DeleteBackup(dir, r.URL.Query().Get("id")); err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) restoreBackup(w http.ResponseWriter, r *http.Request) {
	b, dir, ok := s.backups(w)
	if !ok {
		return
	}
	var body struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Pool string `json:"pool"`
		// Force restores a backup whose files failed their check or changed since.
		Force bool `json:"force"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	job := s.jobs.Start("restore", "Restore as "+body.Name, func(ctx context.Context, report func(string)) (string, error) {
		uuid, err := b.RestoreBackup(ctx, dir, body.ID, body.Name, body.Pool, body.Force, report)
		s.live.Refresh()
		return uuid, err
	})
	s.writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) getBackupSettings(w http.ResponseWriter, _ *http.Request) {
	if s.schedules == nil {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "backups are not available"})
		return
	}
	s.writeJSON(w, http.StatusOK, s.schedules.Settings())
}

func (s *Server) putBackupSettings(w http.ResponseWriter, r *http.Request) {
	if s.schedules == nil {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "backups are not available"})
		return
	}
	var st schedule.Settings
	if !s.decode(w, r, &st) {
		return
	}
	if err := s.schedules.SetSettings(st); err != nil {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	s.writeJSON(w, http.StatusOK, s.schedules.Settings())
}

func (s *Server) listSchedules(w http.ResponseWriter, r *http.Request) {
	if s.schedules == nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"schedules": []schedule.Schedule{}})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"schedules": s.schedules.List(r.URL.Query().Get("domain"))})
}

func (s *Server) putSchedule(w http.ResponseWriter, r *http.Request) {
	if s.schedules == nil {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "schedules are not available"})
		return
	}
	var sc schedule.Schedule
	if !s.decode(w, r, &sc) {
		return
	}
	out, err := s.schedules.Put(sc)
	if err != nil {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	s.writeJSON(w, http.StatusOK, out)
}

func (s *Server) deleteSchedule(w http.ResponseWriter, r *http.Request) {
	if s.schedules == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such schedule"})
		return
	}
	if err := s.schedules.Delete(r.PathValue("id")); err != nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) runSchedule(w http.ResponseWriter, r *http.Request) {
	if s.schedules == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such schedule"})
		return
	}
	if err := s.schedules.RunNow(r.PathValue("id")); err != nil {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
