// Package api exposes VirtoMir's HTTP/JSON API (versioned under /api/v1) and serves the web UI.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/onurbaydas/virtomir/internal/activity"
	"github.com/onurbaydas/virtomir/internal/alerts"
	"github.com/onurbaydas/virtomir/internal/auth"
	"github.com/onurbaydas/virtomir/internal/cluster"
	"github.com/onurbaydas/virtomir/internal/confighist"
	"github.com/onurbaydas/virtomir/internal/jobs"
	"github.com/onurbaydas/virtomir/internal/quota"
	"github.com/onurbaydas/virtomir/internal/schedule"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// Backend is what the API needs from the virtualization layer (implemented by *virt.Client).
type Backend interface {
	HostInfo() (virt.HostInfo, error)
	ListDomains() ([]virt.Domain, error)
	DomainDetail(uuid string) (virt.DomainDetail, error)
	DomainXML(uuid string, inactive bool) (string, error)
	DomainAction(uuid string, action virt.Action) error

	CreateDomain(spec virt.CreateSpec) (virt.CreateResult, error)
	DefineXML(uuid, xml string) (virt.DefineResult, error)
	SetAutostart(uuid string, on bool) error
	Rename(uuid, name string) error
	Delete(uuid string, removeStorage bool) (virt.DeleteResult, error)
	Clone(ctx context.Context, uuid, name string, report func(string)) (string, error)

	ChangeMedia(uuid, target, source string) error
	AttachDisk(uuid string, spec virt.DiskSpec) (string, error)
	AttachInterface(uuid string, spec virt.NICSpec) error
	DetachDevice(uuid string, ref virt.DeviceRef) error

	ListSnapshots(uuid string) ([]virt.Snapshot, error)
	CreateSnapshot(uuid string, spec virt.SnapshotSpec) (string, error)
	RevertSnapshot(uuid, name string) error
	DeleteSnapshot(uuid, name string) error

	ListPools() ([]virt.Pool, error)
	CreatePool(spec virt.PoolSpec) error
	DeletePool(name string) error
	PoolAction(name, action string) error
	SetPoolAutostart(name string, on bool) error
	ListVolumes(pool string) ([]virt.Volume, error)
	CreateVolume(pool string, spec virt.VolumeSpec) (virt.Volume, error)
	DeleteVolume(pool, name string) error
	UploadVolume(ctx context.Context, pool, name string, size int64, r io.Reader) (virt.Volume, error)

	ListNetworks() ([]virt.VNetwork, error)
	CreateNetwork(spec virt.NetworkSpec) error
	DeleteNetwork(name string) error
	NetworkAction(name, action string) error
	SetNetworkAutostart(name string, on bool) error
	NetworkLeases(name string) ([]virt.Lease, error)

	GetSettings(uuid string) (virt.SettingsView, error)
	PreviewSettings(uuid string, s virt.Settings) (virt.Preview, error)
	SaveSettings(uuid string, s virt.Settings) (virt.SaveResult, error)
	ResizeDisk(uuid, target string, sizeGiB uint64) error
	ResizeVolume(pool, name string, sizeGiB uint64) error
	AddDHCPHost(network string, h virt.DHCPHost) error
	DeleteDHCPHost(network, mac string) error
}

// Live is the snapshot stream (implemented by *live.Hub).
type Live interface {
	Subscribe() (backlog [][]byte, updates <-chan []byte, unsubscribe func())
	Refresh()
}

type Options struct {
	Backend Backend
	Live    Live
	Jobs    *jobs.Manager
	// Auth nil means no sign-in (development only; virtomird warns loudly).
	Auth    *auth.Manager
	UI      fs.FS // nil: no UI bundled
	Version string
	Log     *slog.Logger
	// History nil: no metric history (GET /api/v1/history answers 404).
	History HistoryQuerier
	// Tokens nil: no API tokens. Activity nil: changes are only logged, not kept.
	Tokens   *auth.Tokens
	Activity *activity.Log
	// Alerts nil: no alert engine. Schedules nil: no schedules and no backups.
	Alerts    *alerts.Engine
	Schedules *schedule.Scheduler
	// Cluster nil: this host cannot join others (no TLS). Fingerprint is this host's TLS
	// certificate, HostName how members call it, ListenPort where it listens (ADR-0020).
	// Quotas nil: only admins create machines. GroupsOf names a user's groups for group
	// rules (default: the Linux groups).
	Quotas   *quota.Store
	GroupsOf func(user string) []string
	// ConfigHistory nil: machine definitions are not versioned.
	ConfigHistory *confighist.Store
	Cluster       *cluster.Store
	Fingerprint   string
	HostName      string
	ListenPort    string
}

type Server struct {
	backend   Backend
	live      Live
	jobs      *jobs.Manager
	auth      *auth.Manager
	ui        fs.FS
	version   string
	log       *slog.Logger
	history   HistoryQuerier
	tokens    *auth.Tokens
	activity  *activity.Log
	alerts    *alerts.Engine
	schedules *schedule.Scheduler

	confighist   *confighist.Store
	quotas       *quota.Store
	groupsOf     func(string) []string
	cluster      *cluster.Store
	fingerprint  string
	hostName     string
	listenPort   string
	transportsMu sync.Mutex
	transports   map[string]http.RoundTripper // pinned, per member certificate
}

func NewServer(o Options) *Server {
	if o.GroupsOf == nil {
		o.GroupsOf = auth.UserGroups
	}
	if o.Jobs == nil {
		o.Jobs = jobs.New(context.Background())
	}
	return &Server{backend: o.Backend, live: o.Live, jobs: o.Jobs, auth: o.Auth, ui: o.UI, version: o.Version, log: o.Log,
		history: o.History, tokens: o.Tokens, activity: o.Activity, alerts: o.Alerts, schedules: o.Schedules,
		cluster: o.Cluster, fingerprint: o.Fingerprint, hostName: o.HostName, listenPort: o.ListenPort,
		confighist: o.ConfigHistory, quotas: o.Quotas, groupsOf: o.GroupsOf}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /api/v1/version", s.getVersion)
	mux.HandleFunc("GET /api/v1/host", s.getHost)
	mux.HandleFunc("GET /api/v1/stream", s.stream)
	mux.HandleFunc("GET /api/v1/summary", s.summary)
	mux.HandleFunc("GET /api/v1/jobs", s.listJobs)
	mux.HandleFunc("GET /api/v1/jobs/{id}", s.getJob)

	s.authRoutes(mux)
	s.domainRoutes(mux)
	s.storageRoutes(mux)
	s.networkRoutes(mux)
	s.extraRoutes(mux)
	s.accessRoutes(mux)
	s.consoleRoutes(mux)
	s.alertRoutes(mux)
	s.backupRoutes(mux)
	s.templateRoutes(mux)
	s.clusterRoutes(mux)
	s.moveRoutes(mux)
	s.migrateRoutes(mux)
	s.configHistoryRoutes(mux)
	s.importRoutes(mux)
	s.hostdevRoutes(mux)
	s.quotaRoutes(mux)

	// GET-only fallbacks, so a wrong method on a real endpoint still gets 405.
	mux.HandleFunc("GET /api/", func(w http.ResponseWriter, r *http.Request) {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such API endpoint"})
	})
	if s.ui != nil {
		mux.Handle("GET /", spaHandler(s.ui))
	}
	return s.guard(mux)
}

// sameOrigin: browsers send Origin (or Sec-Fetch-Site) with every cross-site write;
// requests without either header (curl, scripts) are not CSRF.
func sameOrigin(r *http.Request) bool {
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		return err == nil && u.Host == r.Host
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
		return true
	}
	return false
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

func (s *Server) listJobs(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{"jobs": s.jobs.List()})
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	j, ok := s.jobs.Get(r.PathValue("id"))
	if !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such job"})
		return
	}
	s.writeJSON(w, http.StatusOK, j)
}

// stream sends live snapshots as Server-Sent Events ("snapshot" events, JSON payload),
// starting with the recent backlog so charts are full from the first frame.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no") // don't let reverse proxies buffer the stream

	token := auth.SessionToken(r)
	// Only browser sessions expire here; tokens are checked per request, forwarded streams by
	// the member that forwards them.
	id := identity(r)
	session := s.auth != nil && id.Token == "" && id.Via == ""
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
			if session && !s.auth.Valid(token) {
				return // signed out or expired: the browser reconnects and gets 401
			}
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

// summary is the newest live snapshot as one JSON answer: what the cluster overview polls
// from every member instead of keeping a stream open to each.
func (s *Server) summary(w http.ResponseWriter, _ *http.Request) {
	backlog, _, unsubscribe := s.live.Subscribe()
	unsubscribe()
	if len(backlog) == 0 {
		s.writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no sample yet; try again in a second"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(backlog[len(backlog)-1])
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

// maxBody limits JSON and XML request bodies (uploads have their own path).
const maxBody = 2 << 20

// decode reads a JSON body into v; on failure it has already answered 400.
func (s *Server) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body: " + err.Error()})
		return false
	}
	return true
}

// writeError maps backend errors to HTTP: 404 unknown object, 422 invalid input,
// 409 refused operation, 429 login lockout, 502 VirtoMir is up but libvirt is not answering.
func (s *Server) writeError(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	var opErr *virt.OperationError
	var invErr *virt.InvalidError
	switch {
	case errors.Is(err, virt.ErrNotFound):
		status = http.StatusNotFound
	case errors.As(err, &invErr):
		status = http.StatusUnprocessableEntity
	case errors.As(err, &opErr):
		status = http.StatusConflict
	case errors.Is(err, auth.ErrBadCredentials), errors.Is(err, auth.ErrNotAllowed):
		status = http.StatusUnauthorized
	case errors.Is(err, auth.ErrTooManyAttempts):
		status = http.StatusTooManyRequests
	case errors.Is(err, context.Canceled):
		status = 499 // client went away; nobody reads this
	}
	if status == http.StatusBadGateway {
		s.log.Error("request failed", "err", err)
	}
	s.writeJSON(w, status, map[string]string{"error": err.Error()})
}

// done answers a successful change and refreshes the live view.
func (s *Server) done(w http.ResponseWriter, status int, v any) {
	s.live.Refresh()
	if v == nil {
		w.WriteHeader(status)
		return
	}
	s.writeJSON(w, status, v)
}
