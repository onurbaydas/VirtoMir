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
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/onurbaydas/virtomir/internal/activity"
	"github.com/onurbaydas/virtomir/internal/api"
	"github.com/onurbaydas/virtomir/internal/auth"
	"github.com/onurbaydas/virtomir/internal/history"
	"github.com/onurbaydas/virtomir/internal/jobs"
	"github.com/onurbaydas/virtomir/internal/live"
	"github.com/onurbaydas/virtomir/internal/virt"
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
	stateDir                 string
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
	flag.StringVar(&c.stateDir, "state-dir", "/var/lib/virtomir", "where virtomird keeps its own files (TLS certificate)")
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

	authMgr, err := setupAuth(c, log)
	if err != nil {
		return err
	}
	tlsConf, err := setupTLS(c, log)
	if err != nil {
		return err
	}

	client := virt.NewClient(c.socket, log)
	defer client.Close()

	tokens, err := auth.OpenTokens(filepath.Join(c.stateDir, "tokens.json"))
	if err != nil {
		log.Warn("API tokens", "err", err)
	}
	acts, err := activity.Open(filepath.Join(c.stateDir, "activity.jsonl"))
	if err != nil {
		log.Warn("activity log", "err", err)
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

	hub := live.NewHub(client, time.Second, log)
	hub.SetRecorder(hist)
	hubDone := make(chan struct{})
	go func() { hub.Run(ctx); close(hubDone) }()
	defer func() { <-hubDone }()

	srv := &http.Server{
		Addr: c.listen,
		Handler: api.NewServer(api.Options{
			Backend: client, Live: hub, Jobs: jobs.New(ctx), Auth: authMgr,
			UI: web.Dist(), Version: version, Log: log, History: hist, Tokens: tokens, Activity: acts,
		}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig:         tlsConf,
	}

	errc := make(chan error, 1)
	go func() {
		scheme := "http"
		if tlsConf != nil {
			scheme = "https"
		}
		log.Info("virtomird listening", "addr", scheme+"://"+c.listen, "version", version, "auth", c.authMode)
		if tlsConf != nil {
			errc <- srv.ListenAndServeTLS("", "")
		} else {
			errc <- srv.ListenAndServe()
		}
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
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

func setupAuth(c config, log *slog.Logger) (*auth.Manager, error) {
	switch c.authMode {
	case "none":
		log.Warn("SIGN-IN IS DISABLED (-auth=none): anyone who can reach " + c.listen + " controls every VM on this host. Use only for development.")
		return nil, nil
	case "pam":
		if !auth.PAMAvailable {
			return nil, errors.New("this build has no PAM support; rebuild with CGO_ENABLED=1 -tags pam, or use -auth=none for development")
		}
		groups := auth.Groups{Admin: c.authGroup, Operator: c.operatorGroup, Viewer: c.viewerGroup}
		return auth.New(auth.PAM{Service: c.pamService}, groups.RoleOf), nil
	}
	return nil, fmt.Errorf("unknown -auth %q (pam or none)", c.authMode)
}

func setupTLS(c config, log *slog.Logger) (*tls.Config, error) {
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
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, nil
}
