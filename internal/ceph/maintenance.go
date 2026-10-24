package ceph

import "context"

// SetNoout sets or clears the cluster's noout flag: with it, OSDs that go down are not
// marked out, so a host that is serviced for a while does not start a rebalance.
func SetNoout(ctx context.Context, r Runner, store *Store, on bool) error {
	verb := "unset"
	if on {
		verb = "set"
	}
	if _, err := r.Run(ctx, "ceph", "osd", verb, "noout"); err != nil {
		return err
	}
	return store.SetNoout(on)
}

// EnterHostMaintenance sets noout and stops the host's Ceph daemons. Ceph refuses when
// stopping them would make data unavailable; its answer is the error.
func EnterHostMaintenance(ctx context.Context, r Runner, store *Store, hostname string) error {
	if !hostRE.MatchString(hostname) {
		return invalidf("%q is not a host name", hostname)
	}
	if err := SetNoout(ctx, r, store, true); err != nil {
		return err
	}
	if _, err := r.Run(ctx, "ceph", "orch", "host", "maintenance", "enter", hostname); err != nil {
		return err
	}
	return store.SetHostMaintenance(hostname, true)
}

// ExitHostMaintenance starts the host's daemons again; the last host to leave
// maintenance clears noout.
func ExitHostMaintenance(ctx context.Context, r Runner, store *Store, hostname string) error {
	if !hostRE.MatchString(hostname) {
		return invalidf("%q is not a host name", hostname)
	}
	if _, err := r.Run(ctx, "ceph", "orch", "host", "maintenance", "exit", hostname); err != nil {
		return err
	}
	if err := store.SetHostMaintenance(hostname, false); err != nil {
		return err
	}
	if cfg, _ := store.Config(); len(cfg.MaintenanceHosts) == 0 {
		return SetNoout(ctx, r, store, false)
	}
	return nil
}
