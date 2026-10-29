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
	"github.com/onurbaydas/virtomir/internal/baremetal"
	"github.com/onurbaydas/virtomir/internal/carve"
	"github.com/onurbaydas/virtomir/internal/catalog"
	"github.com/onurbaydas/virtomir/internal/ceph"
	"github.com/onurbaydas/virtomir/internal/cluster"
	"github.com/onurbaydas/virtomir/internal/compliance"
	"github.com/onurbaydas/virtomir/internal/confighist"
	"github.com/onurbaydas/virtomir/internal/content"
	"github.com/onurbaydas/virtomir/internal/dr"
	"github.com/onurbaydas/virtomir/internal/drs"
	"github.com/onurbaydas/virtomir/internal/dsbrowse"
	"github.com/onurbaydas/virtomir/internal/gpool"
	"github.com/onurbaydas/virtomir/internal/ha"
	"github.com/onurbaydas/virtomir/internal/hostinfo"
	"github.com/onurbaydas/virtomir/internal/hostnet"
	"github.com/onurbaydas/virtomir/internal/insights"
	"github.com/onurbaydas/virtomir/internal/jobs"
	"github.com/onurbaydas/virtomir/internal/k8s"
	"github.com/onurbaydas/virtomir/internal/lcm"
	"github.com/onurbaydas/virtomir/internal/offsite"
	"github.com/onurbaydas/virtomir/internal/physnet"
	"github.com/onurbaydas/virtomir/internal/placement"
	"github.com/onurbaydas/virtomir/internal/project"
	"github.com/onurbaydas/virtomir/internal/quota"
	"github.com/onurbaydas/virtomir/internal/reports"
	"github.com/onurbaydas/virtomir/internal/respool"
	"github.com/onurbaydas/virtomir/internal/schedule"
	"github.com/onurbaydas/virtomir/internal/sdn"
	"github.com/onurbaydas/virtomir/internal/storageeff"
	"github.com/onurbaydas/virtomir/internal/storconn"
	"github.com/onurbaydas/virtomir/internal/update"
	"github.com/onurbaydas/virtomir/internal/virt"
	"github.com/onurbaydas/virtomir/internal/vmware"
	"github.com/onurbaydas/virtomir/internal/wifibridge"
	"github.com/onurbaydas/virtomir/internal/zfsrep"
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
	DetachDevice(uuid string, ref virt.DeviceRef) (virt.DeviceResult, error)

	ListSnapshots(uuid string) ([]virt.Snapshot, error)
	CreateSnapshot(uuid string, spec virt.SnapshotSpec) (string, error)
	RevertSnapshot(uuid, name string, safety bool) (virt.RevertResult, error)
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
	SetDiskQoS(uuid, target string, qos virt.DiskIoTune) error
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
	// ConfigDrift nil: definition files edited by hand are not noticed.
	ConfigDrift *confighist.DriftWatcher
	Cluster     *cluster.Store
	Fingerprint string
	HostName    string
	ListenPort  string

	// HA nil: no high availability (the manager is connected later with SetHA).
	HA       *ha.Store
	Reports  *reports.Store
	Respools *respool.Store
	// HostInfo nil: no chassis, sensors, GPU load, interfaces or packet capture.
	HostInfo *hostinfo.Reader
	// Updater nil: no update check. StateDir is where downloads go.
	Updater  *update.Checker
	StateDir string
	// HostNet nil: no bridges, bonds or VLAN interfaces from the UI (ADR-0042).
	HostNet *hostnet.Manager
	// Offsite nil: no off-site copies of backups (ADR-0043).
	Offsite *offsite.Store
	// VMwareStore nil: VMware migration connection store is not available (ADR-0061).
	VMwareStore ConnectionStorer
	// VMwareWaves nil: VMware migration wave registry is not available.
	VMwareWaves WaveRunner
	// VMwareLister nil: GovmomiLister is used.
	VMwareLister InventoryLister
	// VMwarePreflight nil: Converter with backend client is used.
	VMwarePreflight Preflighter
	// VMwareConverter nil: Converter with backend client is used.
	VMwareConverter VMConverter
	// DRS nil: no distributed resource scheduler.
	DRS *drs.Engine
	// Placement nil: no placement rules.
	Placement *placement.Store
	// Projects nil: no multi-tenancy (ADR-0060).
	Projects *project.Store
	// RateLimiter nil: standard token-bucket limiter (60/min) is used.
	RateLimiter *auth.RateLimiter
	// ZFSRep nil: no ZFS replication.
	ZFSRep *zfsrep.Manager
	// Catalog nil: no self-service catalog (ADR-0065). LeaseMgr nil: catalog machines
	// never expire.
	Catalog  *catalog.Store
	LeaseMgr *catalog.LeaseManager
	// FIPSInfo holds kernel FIPS mode and TLS enforcement details (ADR-0064).
	FIPSInfo compliance.FIPSInfo
	// ComplianceScanner nil: standard scanner rooted at "/" is used (ADR-0064).
	ComplianceScanner *compliance.Scanner
	// Insights nil: no operations-intelligence engine (ADR-0066).
	Insights *insights.Engine
	// Content nil: no content library (ADR-0063).
	Content     *content.Store
	ContentSync *content.SyncManager
	// DR nil: no disaster recovery (ADR-0069, -dr-experimental).
	DRStore        DRStorer
	DROrchestrator DROrchestrator
	DRFailover     DRFailoverRunner
	// GPools nil: no GPU pools (ADR-0071).
	GPools *gpool.Store
	// SDN nil: no software-defined networking on OVN (ADR-0072). SDNRunner runs ovs-vsctl,
	// ovn-nbctl and ovn-sbctl; SDNNorthbound is the OVN northbound database ("" = local).
	SDN           *sdn.Store
	SDNRunner     sdn.Runner
	SDNNorthbound string
	// LCM nil: no rolling cluster upgrades (ADR-0070).
	LCM *lcm.Store
	// Upgrader nil: no OS package upgrades from VirtoMir.
	Upgrader SystemUpgrader
	// Ceph nil: no hyper-converged Ceph or storage policies (ADR-0073); CephRunner runs
	// cephadm, ceph, rbd and lsblk.
	Ceph       *ceph.Store
	CephRunner ceph.Runner
	// StorageEff nil: no storage efficiency, tiers or NVMe/TCP (ADR-0074); EffRunner runs
	// zfs, zpool and nvme.
	StorageEff *storageeff.Store
	EffRunner  storageeff.Runner
	// Physnet nil: no SR-IOV, BGP, IP pools or dynamic DNS (ADR-0075); PhysRunner runs
	// vtysh, SRIOV reads the host's sysfs (physnet.Host when zero).
	Physnet    *physnet.Store
	PhysRunner physnet.Runner
	SRIOV      physnet.SRIOV
	// K8s nil: no Kubernetes clusters (ADR-0076).
	K8s *k8s.Store
	// Baremetal nil: no bare-metal onboarding (ADR-0077); BMRunner runs systemd-run and
	// systemctl for proxy DHCP.
	Baremetal *baremetal.Store
	BMRunner  baremetal.Runner
}

type Server struct {
	replays           replayCache // writes sent again after a dropped connection (replay.go)
	wifi              *wifibridge.Manager
	wifiOnce          sync.Once
	backend           Backend
	live              Live
	jobs              *jobs.Manager
	auth              *auth.Manager
	ui                fs.FS
	version           string
	log               *slog.Logger
	history           HistoryQuerier
	tokens            *auth.Tokens
	activity          *activity.Log
	alerts            *alerts.Engine
	schedules         *schedule.Scheduler
	fipsInfo          compliance.FIPSInfo
	complianceScanner *compliance.Scanner
	scannerOnce       sync.Once
	auditVerifyMu     sync.Mutex
	auditVerified     bool
	auditVerifyAt     time.Time
	insights          *insights.Engine

	confighist   *confighist.Store
	configDrift  *confighist.DriftWatcher
	quotas       *quota.Store
	oidc         oidcFlows
	oidcClient   *http.Client // nil: default (tests use a client trusting their provider)
	groupsOf     func(string) []string
	cluster      *cluster.Store
	fingerprint  string
	hostName     string
	listenPort   string
	transportsMu sync.Mutex
	transports   map[string]http.RoundTripper // pinned, per member certificate

	ha              *ha.Store
	haMgr           *ha.Manager
	reports         *reports.Store
	respools        *respool.Store
	hostinfo        *hostinfo.Reader
	updater         *update.Checker
	stateDir        string
	hostnet         *hostnet.Manager
	offsite         *offsite.Store
	vmwareStore     ConnectionStorer
	vmwareWaves     WaveRunner
	vmwareLister    InventoryLister
	vmwarePreflight Preflighter
	vmwareConverter VMConverter
	drs             *drs.Engine
	placement       *placement.Store
	projects        *project.Store
	rateLimiter     *auth.RateLimiter
	zfsRep          *zfsrep.Manager
	catalog         *catalog.Store
	leaseMgr        *catalog.LeaseManager
	content         *content.Store
	contentSync     *content.SyncManager
	drStore         DRStorer
	drOrchestrator  DROrchestrator
	drFailover      DRFailoverRunner
	lcm             *lcm.Store
	gpools          *gpool.Store
	sdn             *sdn.Store
	sdnRunner       sdn.Runner
	sdnDB           string
	ceph            *ceph.Store
	cephRunner      ceph.Runner
	storageEff      *storageeff.Store
	effRunner       storageeff.Runner
	physnet         *physnet.Store
	physRunner      physnet.Runner
	sriov           physnet.SRIOV
	k8s             *k8s.Store
	k8sOrch         *k8s.Orchestrator
	baremetal       *baremetal.Store
	bmRunner        baremetal.Runner
	fencer          *ha.Fencer
	netboot         netboot
	lcmRun          lcmState
	upgrader        SystemUpgrader
	// inner is the API without the sign-in guard: in-process calls that already carry an
	// identity (the rolling upgrade acting on this host).
	inner  http.Handler
	runCtx context.Context
}

func NewServer(o Options) *Server {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.GroupsOf == nil {
		o.GroupsOf = auth.UserGroups
	}
	if o.Jobs == nil {
		o.Jobs = jobs.New(context.Background())
	}
	rl := o.RateLimiter
	if rl == nil {
		rl = auth.NewRateLimiter(60, 1*time.Minute)
	}
	vl := o.VMwareLister
	if vl == nil {
		vl = vmware.GovmomiLister{}
	}
	vp := o.VMwarePreflight
	vc := o.VMwareConverter
	if vp == nil && vc != nil {
		if p, ok := vc.(Preflighter); ok {
			vp = p
		}
	}
	if vc == nil && vp != nil {
		if c, ok := vp.(VMConverter); ok {
			vc = c
		}
	}
	cs := o.ComplianceScanner
	if cs == nil {
		cs = compliance.NewScanner(nil)
	}
	s := &Server{backend: o.Backend, live: o.Live, jobs: o.Jobs, auth: o.Auth, ui: o.UI, version: o.Version, log: o.Log,
		history: o.History, tokens: o.Tokens, activity: o.Activity, alerts: o.Alerts, schedules: o.Schedules, fipsInfo: o.FIPSInfo,
		complianceScanner: cs,
		cluster:           o.Cluster, fingerprint: o.Fingerprint, hostName: o.HostName, listenPort: o.ListenPort,
		confighist: o.ConfigHistory, configDrift: o.ConfigDrift, quotas: o.Quotas, groupsOf: o.GroupsOf,
		ha: o.HA, reports: o.Reports, respools: o.Respools, hostinfo: o.HostInfo, updater: o.Updater, stateDir: o.StateDir, hostnet: o.HostNet, offsite: o.Offsite,
		vmwareStore: o.VMwareStore, vmwareWaves: o.VMwareWaves, vmwareLister: vl, vmwarePreflight: vp, vmwareConverter: vc,
		drs: o.DRS, placement: o.Placement, projects: o.Projects, rateLimiter: rl, zfsRep: o.ZFSRep,
		catalog: o.Catalog, leaseMgr: o.LeaseMgr,
		content: o.Content, contentSync: o.ContentSync, insights: o.Insights,
		drStore: o.DRStore, drOrchestrator: o.DROrchestrator, drFailover: o.DRFailover,
		lcm: o.LCM, upgrader: o.Upgrader, gpools: o.GPools,
		sdn: o.SDN, sdnRunner: o.SDNRunner, sdnDB: o.SDNNorthbound,
		ceph: o.Ceph, cephRunner: o.CephRunner,
		storageEff: o.StorageEff, effRunner: o.EffRunner,
		physnet: o.Physnet, physRunner: o.PhysRunner, sriov: o.SRIOV, k8s: o.K8s,
		baremetal: o.Baremetal, bmRunner: o.BMRunner, fencer: ha.NewFencer()}
	if s.bmRunner == nil {
		s.bmRunner = baremetal.Exec{}
	}
	if s.k8s != nil {
		s.k8sOrch = k8s.New(s.k8s, s.k8sDriverFor, s.log)
	}
	if s.sdnRunner == nil {
		s.sdnRunner = sdn.Exec{}
	}
	if s.cephRunner == nil {
		s.cephRunner = ceph.Exec{}
	}
	if s.effRunner == nil {
		s.effRunner = storageeff.Exec{}
	}
	if s.physRunner == nil {
		s.physRunner = physnet.Exec{}
	}
	if s.sriov.Root == "" {
		s.sriov = physnet.Host
	}
	if s.contentSync != nil {
		s.contentSync.SetPeers(s)
	}
	return s
}

// FIPSInfo returns the FIPS cryptographic posture and TLS restriction details.
func (s *Server) FIPSInfo() compliance.FIPSInfo {
	return s.fipsInfo
}

func (s *Server) Handler() http.Handler {
	mux := newRouteMux()
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
	s.storconnRoutes(mux)
	s.fileRoutes(mux)
	s.wifiRoutes(mux)
	s.networkRoutes(mux)
	s.extraRoutes(mux)
	s.accessRoutes(mux)
	s.consoleRoutes(mux)
	s.alertRoutes(mux)
	s.insightsRoutes(mux)
	s.backupRoutes(mux)
	s.templateRoutes(mux)
	s.clusterRoutes(mux)
	s.moveRoutes(mux)
	s.migrateRoutes(mux)
	s.configHistoryRoutes(mux)
	s.importRoutes(mux)
	s.vmwareRoutes(mux)
	s.hostdevRoutes(mux)
	s.extraDeviceRoutes(mux)
	s.quotaRoutes(mux)
	s.externalAuthRoutes(mux)
	s.metricsRoutes(mux)
	s.twoFactorRoutes(mux)
	s.haRoutes(mux)
	s.reportRoutes(mux)
	s.respoolRoutes(mux)
	s.hostInfoRoutes(mux)
	s.hostNetRoutes(mux)
	s.offsiteRoutes(mux)
	s.setupRoutes(mux)
	s.firewallRoutes(mux)
	s.updateRoutes(mux)
	s.objectXMLRoutes(mux)
	s.drsRoutes(mux)
	s.placementRoutes(mux)
	s.projectRoutes(mux)
	s.catalogRoutes(mux)
	s.complianceRoutes(mux)
	s.contentRoutes(mux)
	s.drRoutes(mux)
	s.lcmRoutes(mux)
	s.gpoolRoutes(mux)
	s.sdnRoutes(mux)
	s.cephRoutes(mux)
	s.storageEffRoutes(mux)
	s.physnetRoutes(mux)
	s.k8sRoutes(mux)
	s.baremetalRoutes(mux)
	s.systemRoutes(mux)

	s.inner = mux
	spec := s.openAPI(mux.routes) // everything above, built once
	mux.HandleFunc("GET /api/v1/openapi.json", func(w http.ResponseWriter, _ *http.Request) {
		s.writeJSON(w, http.StatusOK, spec)
	})

	// GET-only fallbacks, so a wrong method on a real endpoint still gets 405.
	mux.ServeMux.HandleFunc("GET /api/", func(w http.ResponseWriter, r *http.Request) {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such API endpoint"})
	})
	if s.ui != nil {
		mux.Handle("GET /", spaHandler(s.ui))
	}
	return s.guard(s.replay(mux))
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

// auditVerifyTTL caches Verify() results on the summary path: the audit log is
// only re-walked when a caller asks after the TTL expires.
const auditVerifyTTL = 30 * time.Second

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
// 409 refused operation, 429 login lockout; the rest by plainError (503 libvirt unreachable,
// 507 disk full, 404 missing file, 500 otherwise).
func (s *Server) writeError(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	var opErr *virt.OperationError
	var invErr *virt.InvalidError
	var busyErr *virt.BusyError
	var httpErr *httpError
	switch {
	case errors.As(err, &httpErr):
		status = httpErr.status
	case errors.Is(err, virt.ErrNotFound), errors.Is(err, zfsrep.ErrNotFound), errors.Is(err, drs.ErrNotFound), errors.Is(err, placement.ErrNotFound), errors.Is(err, dr.ErrNotFound), errors.Is(err, gpool.ErrNotFound), errors.Is(err, sdn.ErrNotFound),
		errors.Is(err, ceph.ErrNotFound), errors.Is(err, storageeff.ErrNotFound), errors.Is(err, dsbrowse.ErrNotFound), errors.Is(err, physnet.ErrNotFound), errors.Is(err, k8s.ErrNotFound), errors.Is(err, baremetal.ErrNotFound):
		status = http.StatusNotFound
	case errors.As(err, &invErr), errors.Is(err, zfsrep.ErrInvalid), errors.Is(err, drs.ErrInvalid), errors.Is(err, placement.ErrInvalid), errors.Is(err, dr.ErrInvalid), errors.Is(err, gpool.ErrInvalid), errors.Is(err, sdn.ErrInvalid),
		errors.Is(err, ceph.ErrInvalid), errors.Is(err, ceph.ErrDiskNotEmpty), errors.Is(err, storageeff.ErrInvalid), errors.Is(err, carve.ErrInvalid), errors.Is(err, storconn.ErrInvalid), errors.Is(err, dsbrowse.ErrInvalid),
		errors.Is(err, physnet.ErrInvalid), errors.Is(err, k8s.ErrInvalid), errors.Is(err, baremetal.ErrInvalid):
		status = http.StatusUnprocessableEntity
	case errors.As(err, &opErr), errors.As(err, &busyErr), errors.Is(err, zfsrep.ErrBusy), errors.Is(err, dr.ErrBusy), errors.Is(err, dr.ErrConflict), errors.Is(err, gpool.ErrExists), errors.Is(err, gpool.ErrBusy), errors.Is(err, sdn.ErrExists), errors.Is(err, sdn.ErrBusy),
		errors.Is(err, ceph.ErrExists), errors.Is(err, ceph.ErrBusy), errors.Is(err, dsbrowse.ErrInUse),
		errors.Is(err, physnet.ErrExists), errors.Is(err, physnet.ErrBusy), errors.Is(err, physnet.ErrConflict),
		errors.Is(err, k8s.ErrExists), errors.Is(err, k8s.ErrBusy), errors.Is(err, baremetal.ErrExists), errors.Is(err, baremetal.ErrBusy):
		status = http.StatusConflict
	case errors.Is(err, auth.ErrBadCredentials), errors.Is(err, auth.ErrNotAllowed):
		status = http.StatusUnauthorized
	case errors.Is(err, auth.ErrTooManyAttempts):
		status = http.StatusTooManyRequests
	case errors.Is(err, context.Canceled):
		status = 499 // client went away; nobody reads this
	}
	msg := err.Error()
	if status == http.StatusBadGateway { // no typed mapping: say it plainly (errmap.go), log it all
		s.log.Error("request failed", "err", err)
		status, msg = plainError(err)
	}
	s.writeJSON(w, status, map[string]string{"error": msg})
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
