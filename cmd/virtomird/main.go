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
	"runtime/debug"
	"syscall"
	"time"

	"github.com/onurbaydas/virtomir/internal/activity"
	"github.com/onurbaydas/virtomir/internal/alerts"
	"github.com/onurbaydas/virtomir/internal/api"
	"github.com/onurbaydas/virtomir/internal/atomicfile"
	"github.com/onurbaydas/virtomir/internal/auth"
	"github.com/onurbaydas/virtomir/internal/baremetal"
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
	"github.com/onurbaydas/virtomir/internal/k8s"
	"github.com/onurbaydas/virtomir/internal/lcm"
	"github.com/onurbaydas/virtomir/internal/live"
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
	// The host's own administrators (wheel on Fedora/RHEL, sudo on Debian/Ubuntu) can already
	// become root, so they sign in as admins without being added to libvirt first.
	flag.StringVar(&c.authGroup, "auth-group", "libvirt,wheel,sudo", "members of these comma-separated groups (and root) are admins")
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
	slog.SetDefault(log) // helpers without a logger of their own log the same way

	if flag.Arg(0) == "join" {
		if err := runJoin(c, flag.Arg(1), log); err != nil {
			log.Error("join", "err", err)
			os.Exit(1)
		}
		return
	}
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
	logStoreErr(log, "API tokens", err)
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
	logStoreErr(log, "metric history", err)
	defer func() {
		if err := hist.Save(); err != nil {
			log.Warn("save metric history", "err", err)
		}
	}()
	safeGo(ctx, log, "save metric history", func(ctx context.Context) { saveHistory(ctx, hist, log) })

	insightsEngine := insights.New(hist)
	safeGo(ctx, log, "record pools", func(ctx context.Context) { recordPools(ctx, client, hist) })
	hub := live.NewHub(client, time.Second, log)
	hub.SetRecorder(hist)
	alertEngine, err := alerts.Open(filepath.Join(c.stateDir, "alerts.json"), log)
	logStoreErr(log, "alerts", err)
	cephStore, err := ceph.Open(filepath.Join(c.stateDir, "ceph.json"))
	if err != nil {
		log.Warn("Ceph", "err", err)
	}
	watchAlerts(ctx, log, hub, alertEngine, client, insightsEngine, cephStore)

	jobManager := jobs.New(ctx)
	jobManager.Log = log
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
	logStoreErr(log, "schedules", err)
	if alertEngine != nil {
		scheduler.OnResult = func(sc schedule.Schedule, err error) {
			if sc.Kind != schedule.Backups {
				return
			}
			name := sc.Domain
			if d, derr := client.DomainDetail(sc.Domain); derr == nil {
				name = d.Name
			}
			alertEngine.ScheduledBackup(sc.Domain, name, err)
		}
	}
	client.ProtectedDirs = func() []string { return []string{c.stateDir, scheduler.Settings().BackupDir} }
	client.OldBackupDirs = func() []string { return scheduler.Settings().OldBackupDirs }
	// No backup job survives a restart: folders a crash left half-written go (unless another
	// host sharing the location wrote to them within the hour).
	if _, err := client.SweepPartialBackups(virt.BackupDir(scheduler.Settings().BackupDir), time.Hour); err != nil {
		log.Warn("remove unfinished backups", "err", err)
	}
	safeGo(ctx, log, "scheduler", scheduler.Run)
	hubDone := make(chan struct{})
	go func() { runSafe(ctx, log, "live hub", hub.Run); close(hubDone) }()
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
		safeGo(ctx, log, "configuration history", func(ctx context.Context) { watchConfig(ctx, client, ch, log) })
		drift := confighist.NewDriftWatcher(confighist.DefaultXMLDir, client, log)
		opts.ConfigDrift = drift
		safeGo(ctx, log, "definition files", drift.Run)
	}
	if q, err := quota.Open(filepath.Join(c.stateDir, "quotas.json")); q == nil {
		log.Warn("quotas", "err", err)
	} else {
		logStoreErr(log, "quotas", err)
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
	opts.Updater.Busy = func() bool { return jobManager.Running() > 0 } // a restart would kill them
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
		safeGo(ctx, log, "zfs replication", zr.Run)
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
		safeGo(ctx, log, "content sync", syncManager.Start)
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
		safeGo(ctx, log, "catalog leases", mgr.Run)
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
	if st, err := physnet.Open(filepath.Join(c.stateDir, "physnet.json")); err != nil {
		log.Warn("physical network", "err", err)
	} else {
		opts.Physnet, opts.PhysRunner, opts.SRIOV = st, physnet.Exec{}, physnet.Host
	}
	if st, err := baremetal.Open(filepath.Join(c.stateDir, "baremetal.json")); err != nil {
		log.Warn("bare-metal onboarding", "err", err)
	} else {
		opts.Baremetal = st
	}
	if st, err := k8s.Open(filepath.Join(c.stateDir, "k8s.json")); err != nil {
		log.Warn("Kubernetes clusters", "err", err)
	} else {
		opts.K8s = st
	}
	apiServer := api.NewServer(opts)
	safeGo(ctx, log, "cluster sync", func(ctx context.Context) { syncCluster(ctx, apiServer) })
	safeGo(ctx, log, "report schedules", apiServer.RunReportSchedules)
	safeGo(ctx, log, "DRS", apiServer.RunDRS)
	safeGo(ctx, log, "DR RPO", apiServer.RunDRRPO)
	apiServer.RunLCM(ctx)
	apiServer.RunK8s(ctx)
	apiServer.RunBaremetal(ctx)
	safeGo(ctx, log, "tunnels", func(ctx context.Context) { keepTunnels(ctx, client, log) })
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
		go func() { runSafe(ctx, log, "high availability", mgr.Run); close(haDone) }()
		defer func() { <-haDone }() // disarms the watchdog before exiting
	}

	srv := &http.Server{
		Addr:              c.listen,
		Handler:           apiServer.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second, // keep-alive connections nobody uses; no WriteTimeout (SSE, websockets, downloads)
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

// restartBackoff is the first pause before runSafe restarts a loop that panicked.
var restartBackoff = 5 * time.Second

// safeGo runs a background loop in its own goroutine under runSafe.
func safeGo(ctx context.Context, log *slog.Logger, name string, run func(context.Context)) {
	go runSafe(ctx, log, name, run)
}

// runSafe calls run(ctx). If it panics, the panic and its stack are logged and run starts
// again after a short pause (a background loop must not take the daemon and every running
// machine operation down with it). It returns when run returns normally or ctx ends.
func runSafe(ctx context.Context, log *slog.Logger, name string, run func(context.Context)) {
	backoff := restartBackoff
	for {
		panicked := func() (panicked bool) {
			defer func() {
				if r := recover(); r != nil {
					log.Error("background task panicked; restarting it", "task", name, "panic", r, "stack", string(debug.Stack()))
					panicked = true
				}
			}()
			run(ctx)
			return false
		}()
		if !panicked || ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, 5*time.Minute)
	}
}

// logStoreErr reports a problem opening a state file. An unreadable file has been moved
// aside (the store starts empty), which an admin must know about: that is an error.
func logStoreErr(log *slog.Logger, what string, err error) {
	if err == nil {
		return
	}
	var ce *atomicfile.CorruptError
	if errors.As(err, &ce) {
		log.Error(what+": state file unreadable, moved aside and started empty",
			"file", ce.Path, "backup", ce.Backup, "err", ce.Err)
		return
	}
	log.Warn(what, "err", err)
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

// watchConfig keeps the configuration history (ADR-0021) current from libvirt's events, a
// moment after each change. Events are lost while the connection is down, so every
// (re)subscription starts with a full comparison, and a slow scan runs as a safety net.
func watchConfig(ctx context.Context, client *virt.Client, ch *confighist.Store, log *slog.Logger) {
	deb := confighist.NewDebouncer(time.Second, ch.Changed)
	defer deb.Stop()
	go func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				ch.Sweep()
			}
		}
	}()
	for ctx.Err() == nil {
		events, err := client.ConfigEvents(ctx)
		if err != nil {
			log.Debug("configuration events unavailable, retrying", "err", err)
		} else {
			ch.Sweep() // catch up on what happened before or between subscriptions
			for ev := range events {
				if ev.Stopped {
					ch.Forget(ev.UUID)
					continue
				}
				deb.Add(ev.UUID, ev.Persistent, ev.Live)
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}
	}
}
