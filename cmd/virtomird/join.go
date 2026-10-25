package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/onurbaydas/virtomir/internal/api"
	"github.com/onurbaydas/virtomir/internal/cluster"
)

// runJoin is "virtomird [flags] join [code]" (the code may come in VIRTOMIR_JOIN_CODE, out
// of the process list): this host joins the cluster behind a join code and exits. It is
// how a host installed over the network joins on its first boot (ADR-0077). The service
// keeps running meanwhile, as the inviting member calls this host back to check it (same
// certificate, so the same fingerprint); restart it afterwards to load the membership.
func runJoin(c config, raw string, log *slog.Logger) error {
	if raw == "" {
		raw = os.Getenv("VIRTOMIR_JOIN_CODE")
	}
	code, err := cluster.ParseCode(raw)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.stateDir, 0o755); err != nil {
		return fmt.Errorf("state directory: %w", err)
	}
	tlsConf, err := setupTLS(c, false, log)
	if err != nil {
		return err
	}
	var o api.Options
	if err := setupCluster(c, tlsConf, &o); err != nil {
		return err
	}
	if o.Cluster == nil {
		return errors.New("joining a cluster needs TLS (-tls auto or files)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := o.Cluster.Join(ctx, code, o.ListenPort); err != nil {
		return err
	}
	log.Info("joined the cluster", "via", code.Address, "as", o.HostName)
	return nil
}
