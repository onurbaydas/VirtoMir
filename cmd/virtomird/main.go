// Command virtomird is the VirtoMir daemon. It runs on the libvirt host and serves
// the VirtoMir API and the web UI.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/onurbaydas/virtomir/internal/activity"
	"github.com/onurbaydas/virtomir/internal/alerts"
	"github.com/onurbaydas/virtomir/internal/api"
	"github.com/onurbaydas/virtomir/internal/auth"
	"github.com/onurbaydas/virtomir/internal/catalog"
	"github.com/onurbaydas/virtomir/internal/ceph"
	"github.com/onurbaydas/virtomir/internal/cluster"
	"github.com/onurbaydas/virtomir/internal/compliance"
	"github.com/onurbaydas/virtomir/internal/confighist"
	"github.com/onurbaydas/virtomir/internal/content"
	"github.com/onurbaydas/virtomir/internal/drs"
	"github.com/onurbaydas/virtomir/internal/gpool"
	"github.com/onurbaydas/virtomir/internal/ha"
	"github.com/onurbaydas/virtomir/internal/history"
	"github.com/onurbaydas/virtomir/internal/hostinfo"
	"github.com/onurbaydas/virtomir/internal/hostnet"
	"github.com/onurbaydas/virtomir/internal/insights"
	"github.com/onurbaydas/virtomir/internal/jobs"
	"github.com/onurbaydas/virtomir/internal/lcm"
	"github.com/onurbaydas/virtomir/internal/live"
	"github.com/onurbaydas/virtomir/internal/offsite"
	"github.com/onurbaydas/virtomir/internal/placement"
	"github.com/onurbaydas/virtomir/internal/project"
	"github.com/onurbaydas/virtomir/internal/quota"
	"github.com/onurbaydas/virtomir/internal/reports"
	"github.com/onurbaydas/virtomir/internal/respool"
	"github.com/onurbaydas/virtomir/internal/schedule"
	"github.com/onurbaydas/virtomir/internal/sdn"
	"github.com/onurbaydas/virtomir/internal/storageeff"
	"github.com/onurbaydas/virtomir/internal/systemd"
	"github.com/onurbaydas/virtomir/internal/update"
	"github.com/onurbaydas/virtomir/internal/virt"
	"github.com/onurbaydas/virtomir/internal/vmware"
	"github.com/onurbaydas/virtomir/internal/zfsrep"
	"github.com/onurbaydas/virtomir/web"
)

// version is set at build time: -ldflags "-X main.version=..."
var version = "dev"

type config struct {
	listen, socket           string
	authMode, authGroup      string
	operatorGroup            string
	viewerGroup              string
	pamService               string
	tlsMode, tlsCert, tlsKey string
	fipsMode                 string
	stateDir                 string
	name                     string
	updateRepo               string
	drExperimental           bool
	ovnNorthbound            string
}

func main() {
	var c config
	flag.StringVar(&c.listen, "listen", "127.0.0.1:8420", "HTTP(S) listen address")
	flag.StringVar(&c.socket, "libvirt-socket", virt.DefaultSocket, "libvirt UNIX socket")
	flag.StringVar(&c.authMode, "auth", "pam", `sign-in: "pam" (Linux accounts) or "none" (development only)`)
	flag.StringVar(&c.authGroup, "auth-group", "libvirt", "members of this group (and root) are admins")
	flag.StringVar(&c.operatorGroup, "operator-group", "virtomir-operator", "members may run machines (power, console, snapshots) but not change them")
	flag.StringVar(&c.viewerGroup, "viewer-group", "virtomir-viewer", "members may look at everything but change nothing")
	flag.StringVar(&c.pamService, "pam-service", "virtomir", "PAM service name (/etc/pam.d/<name>)")
	flag.StringVar(&c.tlsMode, "tls", "auto", `"auto" (self-signed certificate in -state-dir), "files" (-tls-cert/-tls-key) or "off"`)
	flag.StringVar(&c.tlsCert, "tls-cert", "", "certificate file for -tls=files")
	flag.StringVar(&c.tlsKey, "tls-key", "", "private key file for -tls=files")
	flag.StringVar(&c.fipsMode, "fips", "auto", `"auto" (detect kernel /proc/sys/crypto/fips_enabled), "on" or "off" (experimental)`)
	flag.StringVar(&c.stateDir, "state-dir", "/var/lib/virtomir", "where virtomird keeps its own files (TLS certificate)")
	flag.StringVar(&c.name, "name", "", "how this host is called in a cluster (default: its hostname)")
	flag.StringVar(&c.updateRepo, "update-repo", "onurbaydas/VirtoMir", `GitHub repository whose releases the Updates card offers ("" turns the check off); a private one needs VIRTOMIR_GITHUB_TOKEN`)
	flag.BoolVar(&c.drExperimental, "dr-experimental", false, "turn on disaster recovery (protection groups, recovery plans, failover); not tested on real hosts yet")
	flag.StringVar(&c.ovnNorthbound, "ovn-nb", "", `OVN northbound database for the SDN, e.g. "tcp:10.0.0.11:6641" (default: the local one); the SDN is experimental`)
	debug := flag.Bool("debug", false, "verbose logging")
	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	if err := run(c, log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(c config, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Like systemd's StateDirectory=: 0755, so QEMU can pass through to the default backup
	// location. Secrets below it (tls/, tokens) have their own restrictive modes.
	if err := os.MkdirAll(c.stateDir, 0o755); err != nil {
		return fmt.Errorf("state directory: %w", err)
	}

	pjStore, err := project.Open(filepath.Join(c.stateDir, "projects.json"))
	if err != nil {
		log.Warn("projects store", "err", err)
	}

	authMgr, err := setupAuth(c, pjStore, log)
	if err != nil {
		return err
	}
	kernelFIPS := compliance.DetectKernelFIPS("/")
	fipsEnforce := (c.fipsMode == "on") || (c.fipsMode == "auto" && kernelFIPS)
	if fipsEnforce && c.tlsMode == "off" {
		log.Warn("FIPS mode enabled but TLS is off: TLS cipher restrictions cannot be enforced")
		fipsEnforce = false
	}
	tlsConf, err := setupTLS(c, fipsEnforce, log)
	if err != nil {
		return err
	}

	client := virt.NewClient(c.socket, log)
	client.ContainerDir = filepath.Join(c.stateDir, "containers")
	defer client.Close()

	tokens, err := auth.OpenTokens(filepath.Join(c.stateDir, "tokens.json"))
	if err != nil {
		log.Warn("API tokens", "err", err)
	}
	acts, err := activity.Open(filepath.Join(c.stateDir, "activity.jsonl"))
	if err != nil {
		log.Warn("activity log", "err", err)
	}
	if acts != nil {
		if cfg, ok, err := activity.LoadSIEMConfig(filepath.Join(c.stateDir, api.SIEMConfigFile)); err != nil {
			log.Warn("SIEM forwarding", "err", err)
		} else if ok {
			acts.AttachSIEM(activity.NewForwarder(cfg))
		}
	}

	hist, err := history.Open(filepath.Join(c.stateDir, "history.gob"))
	if err != nil {
		log.Warn("metric history", "err", err)
	}
	defer func() {
		if err := hist.Save(); err != nil {
			log.Warn("save metric history", "err", err)
		}
	}()
	go saveHistory(ctx, hist, log)

	insightsEngine := insights.New(hist)
	go recordPools(ctx, client, hist)
	hub := live.NewHub(client, time.Second, log)
	hub.SetRecorder(hist)
	alertEngine, err := alerts.Open(filepath.Join(c.stateDir, "alerts.json"), log)
	if err != nil {
		log.Warn("alerts", "err", err)
	}
	cephStore, err := ceph.Open(filepath.Join(c.stateDir, "ceph.json"))
	if err != nil {
		log.Warn("Ceph", "err", err)
	}
	watchAlerts(ctx, hub, alertEngine, client, insightsEngine, cephStore)

	jobManager := jobs.New(ctx)
	hostName := c.name
	if hostName == "" {
		hostName, _ = os.Hostname()
	}
	offsiteStore, err := offsite.Open(filepath.Join(c.stateDir, "offsite.json"), filepath.Join(c.stateDir, "restic-cache"), hostName)
	if err != nil {
		log.Warn("off-site copies", "err", err)
	}
	scheduler, err := schedule.Open(filepath.Join(c.stateDir, "schedules.json"), filepath.Join(c.stateDir, "backups"),
		scheduleRunner{client, offsiteStore}, jobStarter(jobManager), log)
	if err != nil {
		log.Warn("schedules", "err", err)
	}
	go scheduler.Run(ctx)
	hubDone := make(chan struct{})
	go func() { hub.Run(ctx); close(hubDone) }()
	defer func() { <-hubDone }()

	fipsInfo := compliance.GetFIPSInfo("/", fipsEnforce)
	compScanner := compliance.NewScanner(compliance.NewReader("/"))

	opts := api.Options{
		Backend: client, Live: hub, Jobs: jobManager, Auth: authMgr, Schedules: scheduler,
		UI: web.Dist(), Version: version, Log: log, History: hist, Tokens: tokens, Activity: acts, Alerts: alertEngine,
		Insights: insightsEngine, FIPSInfo: fipsInfo, ComplianceScanner: compScanner,
	}
	if err := setupCluster(c, tlsConf, &opts); err != nil {
		log.Warn("cluster", "err", err)
	}
	if ch, err := confighist.Open(filepath.Join(c.stateDir, "config-history"), client, log); err != nil {
		log.Warn("configuration history", "err", err)
	} else {
		opts.ConfigHistory = ch
		go sweepConfig(ctx, ch)
	}
	if q, err := quota.Open(filepath.Join(c.stateDir, "quotas.json")); err != nil {
		log.Warn("quotas", "err", err)
	} else {
		opts.Quotas = q
	}
	haStore, err := ha.Open(filepath.Join(c.stateDir, "ha.json"))
	if err != nil {
		log.Warn("high availability", "err", err)
	} else {
		opts.HA = haStore
	}
	opts.HostInfo = hostinfo.New()
	if os.Geteuid() == 0 { // writes /etc/netplan, /etc/systemd/network or NetworkManager connections
		opts.HostNet = hostnet.New(c.stateDir, log, opts.HostInfo)
	}
	opts.Offsite = offsiteStore
	if vmwareStore, err := vmware.Open(filepath.Join(c.stateDir, "vmware.json")); err != nil {
		log.Warn("vmware store", "err", err)
	} else {
		opts.VMwareStore = vmwareStore
	}
	opts.VMwareWaves = vmware.NewRegistry()
	opts.VMwareLister = vmware.GovmomiLister{}
	conv := vmware.NewConverter(client)
	opts.VMwarePreflight = conv
	if c, ok := any(conv).(api.VMConverter); ok {
		opts.VMwareConverter = c
	}
	opts.Updater = update.New(c.updateRepo, version, os.Getenv("VIRTOMIR_GITHUB_TOKEN"))
	opts.StateDir = c.stateDir
	if repStore, err := reports.Open(filepath.Join(c.stateDir, "reports")); err != nil {
		log.Warn("reports store", "err", err)
	} else {
		opts.Reports = repStore
	}
	if rpStore, err := respool.Open(filepath.Join(c.stateDir, "respools.json")); err != nil {
		log.Warn("respools store", "err", err)
	} else {
		opts.Respools = rpStore
	}
	if pjStore != nil {
		opts.Projects = pjStore
	}
	if zr, err := zfsrep.Open(filepath.Join(c.stateDir, "zfs-replication.json"), zfsrep.Exec{}, log); err != nil {
		log.Warn("zfs replication", "err", err)
	} else {
		opts.ZFSRep = zr
		go zr.Run(ctx)
	}
	if c.drExperimental {
		wireDR(&opts, client, c.stateDir, log)
	}
	if drsEng, err := drs.New(c.stateDir); err != nil {
		log.Warn("drs engine", "err", err)
	} else {
		opts.DRS = drsEng
	}
	contentStore, err := content.Open(filepath.Join(c.stateDir, "content.json"))
	if err != nil {
		log.Warn("content store", "err", err)
	} else {
		opts.Content = contentStore
		syncManager := content.NewSyncManager(contentStore, jobManager, client, opts.Cluster, nil, log)
		opts.ContentSync = syncManager
		go syncManager.Start(ctx)
	}
	if plStore, err := placement.Open(filepath.Join(c.stateDir, "placement.json")); err != nil {
		log.Warn("placement store", "err", err)
	} else {
		opts.Placement = plStore
	}
	// Self-service catalog (ADR-0065): blueprints, requests and showback in
	// -state-dir/catalog.json; the lease runner powers expired machines off.
	if catStore, err := catalog.Open(filepath.Join(c.stateDir, "catalog.json")); err != nil {
		log.Warn("catalog", "err", err)
	} else {
		opts.Catalog = catStore
		var notifier catalog.AlertNotifier
		if alertEngine != nil {
			notifier = leaseAlerts{alertEngine}
		}
		leaseOpts := []catalog.LeaseOption{}
		if pjStore != nil {
			leaseOpts = append(leaseOpts, catalog.WithProjectStore(pjStore))
		}
		mgr := catalog.NewLeaseManager(catStore, leaseCtrl{client}, notifier, log, leaseOpts...)
		opts.LeaseMgr = mgr
		go mgr.Run(ctx)
	}
	if st, err := lcm.Open(filepath.Join(c.stateDir, "lcm.json")); err != nil {
		log.Warn("rolling upgrades", "err", err)
	} else {
		opts.LCM = st
	}
	opts.Upgrader = &systemd.Upgrader{Log: log}
	if gp, err := gpool.Open(filepath.Join(c.stateDir, "gpools.json")); err != nil {
		log.Warn("GPU pools", "err", err)
	} else {
		opts.GPools = gp
	}
	if st, err := sdn.Open(filepath.Join(c.stateDir, "sdn.json")); err != nil {
		log.Warn("SDN", "err", err)
	} else {
		opts.SDN, opts.SDNRunner, opts.SDNNorthbound = st, sdn.Exec{}, c.ovnNorthbound
	}
	if cephStore != nil {
		opts.Ceph, opts.CephRunner = cephStore, ceph.Exec{}
	}
	if st, err := storageeff.Open(filepath.Join(c.stateDir, "storageeff.json")); err != nil {
		log.Warn("storage efficiency", "err", err)
	} else {
		opts.StorageEff, opts.EffRunner = st, storageeff.Exec{}
		go func() { // NVMe/TCP subsystems connected through VirtoMir come back after a reboot
			for _, err := range storageeff.Reconnect(ctx, storageeff.Exec{}, st) {
				log.Warn("NVMe/TCP", "err", err)
			}
		}()
	}
	apiServer := api.NewServer(opts)
	go syncCluster(ctx, apiServer)
	go apiServer.RunReportSchedules(ctx)
	go apiServer.RunDRS(ctx)
	go apiServer.RunDRRPO(ctx)
	apiServer.RunLCM(ctx)
	go keepTunnels(ctx, client, log)
	if opts.Updater != nil {
		opts.Updater.InitConfig(c.stateDir)
		go opts.Updater.StartAutoUpdater(ctx, c.stateDir, func(action, details string) {
			if acts != nil {
				_ = acts.Add(activity.Entry{
					User:   "system",
					Method: "AUTO",
					Path:   "/api/v1/update",
					Status: 200,
					Error:  details,
				})
			}
		}, log)
	}
	if haStore != nil {
		mgr := ha.NewManager(haStore, apiServer.HATransport(), client, filepath.Join(c.stateDir, "ha"), log)
		if alertEngine != nil {
			mgr.Notify = alertEngine.Notice // failovers and fencing reach the alert channels
		}
		if opts.Placement != nil {
			mgr.FilterTargets = func(r ha.Replica, candidates []ha.Node, runningOn map[string]string) []ha.Node {
				candIDs := make([]string, len(candidates))
				nodeByID := make(map[string]ha.Node, len(candidates))
				for i, n := range candidates {
					candIDs[i] = n.ID
					nodeByID[n.ID] = n
				}
				rules := opts.Placement.Rules()
				allowed, preferred := placement.FilterHATargets(rules, r.UUID, candIDs, runningOn)
				isPref := make(map[string]bool, len(preferred))
				for _, id := range preferred {
					isPref[id] = true
				}
				var out []ha.Node
				for _, id := range preferred {
					if n, ok := nodeByID[id]; ok {
						out = append(out, n)
					}
				}
				for _, id := range allowed {
					if !isPref[id] {
						if n, ok := nodeByID[id]; ok {
							out = append(out, n)
						}
					}
				}
				return out
			}
		}
		apiServer.SetHA(mgr)
		haDone := make(chan struct{})
		go func() { mgr.Run(ctx); close(haDone) }()
		defer func() { <-haDone }() // disarms the watchdog before exiting
	}

	srv := &http.Server{
		Addr:              c.listen,
		Handler:           apiServer.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig:         tlsConf,
	}

	// Bind before reporting ready: a busy port must end the process (and let systemd restart
	// it) instead of leaving the background workers running behind no listener. stop() first,
	// because the HA defer above waits for ctx.
	ln, err := net.Listen("tcp", c.listen)
	if err != nil {
		stop()
		return fmt.Errorf("listen on %s: %w", c.listen, err)
	}
	errc := make(chan error, 1)
	go func() {
		scheme := "http"
		if tlsConf != nil {
			scheme = "https"
		}
		log.Info("virtomird listening", "addr", scheme+"://"+c.listen, "version", version, "auth", c.authMode)
		_ = systemd.Notify(systemd.Ready)
		systemd.StartWatchdog(ctx, func(msg string, args ...any) { log.Info(fmt.Sprintf(msg, args...)) })
		if tlsConf != nil {
			errc <- srv.ServeTLS(ln, "", "")
		} else {
			errc <- srv.Serve(ln)
		}
	}()

	select {
	case err := <-errc:
		stop()
		return err
	case <-ctx.Done():
		log.Info("shutting down")
		_ = systemd.Notify(systemd.Stopping)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

// recordPools adds every active pool's used bytes to the history every 10 minutes, the
// series the pool capacity forecasts read (ADR-0066).
func recordPools(ctx context.Context, c *virt.Client, h *history.Store) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		if pools, err := c.ListPools(); err == nil {
			now := time.Now()
			for _, p := range pools {
				if p.Active && p.CapacityBytes > 0 {
					h.Add(insights.PoolSeries(p.Name), now, float64(p.AllocationBytes))
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// saveHistory writes the metric history every 5 minutes, so a crash loses little.
func saveHistory(ctx context.Context, h *history.Store, log *slog.Logger) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	warned := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := h.Save(); err != nil && !warned {
				log.Warn("save metric history (will keep trying)", "err", err)
				warned = true
			}
		}
	}
}

func setupAuth(c config, pj *project.Store, log *slog.Logger) (*auth.Manager, error) {
	switch c.authMode {
	case "none":
		log.Warn("SIGN-IN IS DISABLED (-auth=none): anyone who can reach " + c.listen + " controls every VM on this host. Use only for development.")
		return nil, nil
	case "pam":
		if !auth.PAMAvailable {
			return nil, errors.New("this build has no PAM support; rebuild with CGO_ENABLED=1 -tags pam, or use -auth=none for development")
		}
		groups := auth.Groups{Admin: c.authGroup, Operator: c.operatorGroup, Viewer: c.viewerGroup}
		roleOf := api.RoleOfWithProjects(groups.RoleOf, pj, auth.UserGroups)
		m := auth.New(auth.PAM{Service: c.pamService}, roleOf)
		// LDAP and OpenID Connect, configured by admins in the UI (ADR-0023).
		ext, err := auth.OpenExternal(filepath.Join(c.stateDir, "signin.json"))
		if err != nil {
			return nil, err
		}
		if pj != nil {
			ext.SetRoleFallback(api.ExternalRoleFallback(pj))
		}
		m.SetExternal(ext)
		tf, err := auth.OpenTwoFactor(filepath.Join(c.stateDir, "twofactor.json"))
		if err != nil {
			return nil, err
		}
		m.SetTwoFactor(tf)
		return m, nil
	}
	return nil, fmt.Errorf("unknown -auth %q (pam or none)", c.authMode)
}

func setupTLS(c config, fipsEnforce bool, log *slog.Logger) (*tls.Config, error) {
	var cert tls.Certificate
	var err error
	switch c.tlsMode {
	case "off":
		if c.authMode != "none" {
			log.Warn("TLS is off: passwords travel unencrypted unless a TLS proxy sits in front of virtomird")
		}
		return nil, nil
	case "auto":
		cert, err = auth.SelfSignedCert(filepath.Join(c.stateDir, "tls"))
	case "files":
		cert, err = tls.LoadX509KeyPair(c.tlsCert, c.tlsKey)
	default:
		return nil, fmt.Errorf("unknown -tls %q (auto, files or off)", c.tlsMode)
	}
	if err != nil {
		return nil, fmt.Errorf("TLS: %w", err)
	}
	tlsConf := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	if fipsEnforce {
		compliance.ApplyFIPSTLS(tlsConf)
		log.Info("FIPS mode enabled: restricting TLS to FIPS 140 approved cipher suites (experimental, not tested on hardware)")
	}
	return tlsConf, nil
}

// setupCluster opens -state-dir/cluster.json. Joining other hosts needs TLS: members
// recognize each other by certificate fingerprint (ADR-0020).
func setupCluster(c config, tlsConf *tls.Config, o *api.Options) error {
	_, port, err := net.SplitHostPort(c.listen)
	if err != nil {
		return err
	}
	o.ListenPort = port
	o.HostName = c.name
	if o.HostName == "" {
		o.HostName, _ = os.Hostname()
	}
	if tlsConf == nil || len(tlsConf.Certificates) == 0 || len(tlsConf.Certificates[0].Certificate) == 0 {
		return nil
	}
	o.Fingerprint = cluster.Fingerprint(tlsConf.Certificates[0].Certificate[0])
	o.Cluster, err = cluster.Open(filepath.Join(c.stateDir, "cluster.json"), o.HostName, o.Fingerprint)
	return err
}

// syncCluster lets a host that missed a membership change catch up.
func syncCluster(ctx context.Context, s *api.Server) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.SyncMembers(ctx)
		}
	}
}

// leaseCtrl adapts virt.Client to the lease runner's machine lifecycle (ADR-0065):
// grace the guest a shutdown, then pull the plug, then delete the machine and its disks.
type leaseCtrl struct{ c *virt.Client }

func (l leaseCtrl) ShutdownDomain(uuid string) error {
	return l.c.DomainAction(uuid, virt.ActionShutdown)
}
func (l leaseCtrl) DestroyDomain(uuid string) error {
	return l.c.DomainAction(uuid, virt.ActionForceOff)
}

func (l leaseCtrl) DeleteDomain(uuid string) error {
	_, err := l.c.Delete(uuid, true) // the leased machine's whole storage goes with it
	return err
}

// leaseAlerts sends lease events to the alert channels: the engine's subject is the
// summary, the message carries the details.
type leaseAlerts struct{ e *alerts.Engine }

func (a leaseAlerts) Notice(domain, title, message string) {
	a.e.Notice(title+" ("+domain+")", message)
}

// sweepConfig notices definition changes made outside VirtoMir (virsh, other tools).
func sweepConfig(ctx context.Context, ch *confighist.Store) {
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		ch.Sweep()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
