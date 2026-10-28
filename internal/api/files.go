package api

import (
	"context"
	"fmt"
	"net/http"
	"path"
	"time"

	"github.com/onurbaydas/virtomir/internal/dsbrowse"
)

// The datastore browser: the files and folders of a file-based pool (dsbrowse).

func (s *Server) fileRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/storage/pools/{pool}/files", s.listFiles)
	mux.HandleFunc("GET /api/v1/storage/pools/{pool}/files/download", s.downloadFile)
	mux.HandleFunc("POST /api/v1/storage/pools/{pool}/files/upload", s.uploadFile)
	mux.HandleFunc("POST /api/v1/storage/pools/{pool}/files/folder", s.makeFolder)
	mux.HandleFunc("POST /api/v1/storage/pools/{pool}/files/rename", s.renameFile)
	mux.HandleFunc("POST /api/v1/storage/pools/{pool}/files/copy", s.copyFile)
	mux.HandleFunc("DELETE /api/v1/storage/pools/{pool}/files", s.deleteFile)
}

// diskUserBackend knows which machines use which disk images (virt.Client).
type diskUserBackend interface {
	DiskUsers() (map[string][]string, error)
}

// diskUsers fails closed: a file is not renamed, moved or deleted when the machines using
// it cannot be read.
func (s *Server) diskUsers() (map[string][]string, error) {
	b, ok := s.backend.(diskUserBackend)
	if !ok {
		return map[string][]string{}, nil
	}
	users, err := b.DiskUsers()
	if err != nil {
		return nil, fmt.Errorf("could not read which machines use which disks: %w", err)
	}
	return users, nil
}

// fileRoot is the directory of a started file-based pool.
func (s *Server) fileRoot(name string) (string, error) {
	pools, err := s.backend.ListPools()
	if err != nil {
		return "", err
	}
	for _, p := range pools {
		if p.Name != name {
			continue
		}
		switch {
		case p.Type != "dir" && p.Type != "fs" && p.Type != "netfs":
			return "", fmt.Errorf("%w: %s holds block volumes, not files", dsbrowse.ErrInvalid, name)
		case !p.Active || p.Path == "":
			return "", fmt.Errorf("%w: start the pool %s first", dsbrowse.ErrInvalid, name)
		}
		return p.Path, nil
	}
	return "", fmt.Errorf("%w: pool %s", dsbrowse.ErrNotFound, name)
}

// refreshPool lets libvirt see files added or removed behind its back.
func (s *Server) refreshPool(name string) {
	if err := s.backend.PoolAction(name, "refresh"); err != nil {
		s.log.Debug("refresh pool", "pool", name, "err", err)
	}
}

func (s *Server) listFiles(w http.ResponseWriter, r *http.Request) {
	root, err := s.fileRoot(r.PathValue("pool"))
	if err == nil {
		var users map[string][]string
		if users, err = s.diskUsers(); err == nil {
			var es []dsbrowse.Entry
			if es, err = dsbrowse.List(root, r.URL.Query().Get("path"), users); err == nil {
				s.writeJSON(w, http.StatusOK, map[string]any{"entries": es})
				return
			}
		}
	}
	s.writeError(w, err)
}

func (s *Server) downloadFile(w http.ResponseWriter, r *http.Request) {
	root, err := s.fileRoot(r.PathValue("pool"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	f, err := dsbrowse.Open(root, r.URL.Query().Get("path"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		s.writeError(w, err)
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", path.Base(st.Name())))
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, st.Name(), st.ModTime(), f) // ranges: a broken download resumes
}

func (s *Server) uploadFile(w http.ResponseWriter, r *http.Request) {
	pool := r.PathValue("pool")
	root, err := s.fileRoot(pool)
	if err != nil {
		s.writeError(w, err)
		return
	}
	n, err := dsbrowse.Upload(root, r.URL.Query().Get("path"), r.Body)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.refreshPool(pool)
	s.writeJSON(w, http.StatusCreated, map[string]any{"sizeBytes": n})
}

func (s *Server) makeFolder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	root, err := s.fileRoot(r.PathValue("pool"))
	if err == nil {
		err = dsbrowse.Mkdir(root, body.Path)
	}
	if err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) renameFile(w http.ResponseWriter, r *http.Request) {
	var body struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	pool := r.PathValue("pool")
	root, err := s.fileRoot(pool)
	var users map[string][]string
	if err == nil {
		users, err = s.diskUsers()
	}
	if err == nil {
		err = dsbrowse.Rename(root, body.From, body.To, users)
	}
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.refreshPool(pool)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteFile(w http.ResponseWriter, r *http.Request) {
	pool := r.PathValue("pool")
	root, err := s.fileRoot(pool)
	var users map[string][]string
	if err == nil {
		users, err = s.diskUsers()
	}
	if err == nil {
		err = dsbrowse.Remove(root, r.URL.Query().Get("path"), users)
	}
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.refreshPool(pool)
	w.WriteHeader(http.StatusNoContent)
}

// copyFile copies or moves a file to a folder of any file-based pool of this host (a job:
// disk images are large).
func (s *Server) copyFile(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path   string `json:"path"`
		ToPool string `json:"toPool"`
		ToPath string `json:"toPath"`
		Move   bool   `json:"move"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	pool := r.PathValue("pool")
	src, err := s.fileRoot(pool)
	var dst string
	if err == nil {
		dst, err = s.fileRoot(body.ToPool)
	}
	var users map[string][]string
	if err == nil {
		users, err = s.diskUsers()
	}
	if err != nil {
		s.writeError(w, err)
		return
	}
	verb := "Copy"
	if body.Move {
		verb = "Move"
	}
	job := s.jobs.Start("copy-file", fmt.Sprintf("%s %s to %s", verb, path.Base(body.Path), body.ToPool), func(ctx context.Context, report func(string)) (string, error) {
		start := time.Now()
		err := dsbrowse.Copy(ctx, src, body.Path, dst, body.ToPath, body.Move, users, report)
		s.refreshPool(pool)
		if body.ToPool != pool {
			s.refreshPool(body.ToPool)
		}
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("done in %s", time.Since(start).Round(time.Second)), nil
	})
	s.writeJSON(w, http.StatusAccepted, job)
}
