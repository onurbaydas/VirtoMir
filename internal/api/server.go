// Package api exposes VirtoMir's HTTP/JSON API (versioned under /api/v1) and serves the web UI.
package api

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/onurbaydas/virtomir/internal/virt"
)

// Backend is what the API needs from the virtualization layer (implemented by *virt.Client).
type Backend interface {
	HostInfo() (virt.HostInfo, error)
	ListDomains() ([]virt.Domain, error)
	DomainDetail(uuid string) (virt.DomainDetail, error)
	DomainXML(uuid string, inactive bool) (string, error)
	DomainAction(uuid string, action virt.Action) error
}

// Live is the snapshot stream (implemented by *live.Hub).
type Live interface {
	Subscribe() (backlog [][]byte, updates <-chan []byte, unsubscribe func())
	Refresh()
}

type Server struct {
	backend Backend
	live    Live
	ui      fs.FS // nil: no UI bundled
	version string
	log     *slog.Logger
}

func NewServer(backend Backend, live Live, ui fs.FS, version string, log *slog.Logger) *Server {
	return &Server{backend: backend, live: live, ui: ui, version: version, log: log}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /api/v1/version", s.getVersion)
	mux.HandleFunc("GET /api/v1/host", s.getHost)
	mux.HandleFunc("GET /api/v1/domains", s.listDomains)
	mux.HandleFunc("GET /api/v1/domains/{uuid}", s.getDomain)
	mux.HandleFunc("GET /api/v1/domains/{uuid}/xml", s.getDomainXML)
	mux.HandleFunc("POST /api/v1/domains/{uuid}/actions/{action}", s.domainAction)
	mux.HandleFunc("GET /api/v1/stream", s.stream)
	// GET-only fallbacks, so a wrong method on a real endpoint still gets 405.
	mux.HandleFunc("GET /api/", func(w http.ResponseWriter, r *http.Request) {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such API endpoint"})
	})
	if s.ui != nil {
		mux.Handle("GET /", spaHandler(s.ui))
	}
	return mux
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getVersion(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]string{"version": s.version})
}

func (s *Server) getHost(w http.ResponseWriter, _ *http.Request) {
	h, err := s.backend.HostInfo()
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, h)
}

func (s *Server) listDomains(w http.ResponseWriter, _ *http.Request) {
	doms, err := s.backend.ListDomains()
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"domains": doms})
}

func (s *Server) getDomain(w http.ResponseWriter, r *http.Request) {
	d, err := s.backend.DomainDetail(r.PathValue("uuid"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, d)
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
	s.live.Refresh()
	w.WriteHeader(http.StatusAccepted) // e.g. a shutdown is only a request to the guest
}

// stream sends live snapshots as Server-Sent Events ("snapshot" events, JSON payload),
// starting with the recent backlog so charts are full from the first frame.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no") // don't let reverse proxies buffer the stream

	backlog, snaps, unsubscribe := s.live.Subscribe()
	defer unsubscribe()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	head := []byte("retry: 2000\n\n")
	for _, b := range backlog {
		head = appendEvent(head, b)
	}
	if _, err := w.Write(head); err != nil || rc.Flush() != nil {
		return
	}
	for {
		var chunk []byte
		select {
		case <-r.Context().Done():
			return
		case b, ok := <-snaps:
			if !ok {
				return // server shutting down
			}
			chunk = appendEvent(nil, b)
		case <-heartbeat.C:
			chunk = []byte(": keep-alive\n\n")
		}
		if _, err := w.Write(chunk); err != nil {
			return
		}
		if err := rc.Flush(); err != nil {
			return
		}
	}
}

func appendEvent(dst, data []byte) []byte {
	dst = append(dst, "event: snapshot\ndata: "...)
	dst = append(dst, data...)
	return append(dst, '\n', '\n')
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.log.Warn("write response", "err", err)
	}
}

// writeError maps backend errors to HTTP: 404 unknown domain, 409 libvirt refused the
// operation, 502 VirtoMir is up but libvirt is not answering.
func (s *Server) writeError(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	var opErr *virt.OperationError
	switch {
	case errors.Is(err, virt.ErrNotFound):
		status = http.StatusNotFound
	case errors.As(err, &opErr):
		status = http.StatusConflict
	}
	if status == http.StatusBadGateway {
		s.log.Error("request failed", "err", err)
	}
	s.writeJSON(w, status, map[string]string{"error": err.Error()})
}
