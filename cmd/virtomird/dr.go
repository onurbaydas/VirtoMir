package main

import (
	"log/slog"
	"path/filepath"

	"github.com/onurbaydas/virtomir/internal/api"
	"github.com/onurbaydas/virtomir/internal/dr"
	"github.com/onurbaydas/virtomir/internal/virt"
	"github.com/onurbaydas/virtomir/internal/zfsrep"
)

// wireDR turns on disaster recovery (ADR-0069, experimental) with this host as the DR
// host. The production host is not reached from here yet, so a real failover is a
// forced one, and a protection group is not synced on demand (zfsrep runs on the
// source host and syncs on its own schedule).
func wireDR(opts *api.Options, c *virt.Client, stateDir string, log *slog.Logger) {
	store, err := dr.Open(filepath.Join(stateDir, "dr.json"))
	if err != nil {
		log.Warn("disaster recovery", "err", err)
		return
	}
	host := virt.DRHost{C: c}
	orch := dr.NewOrchestrator(store, zfsrep.Exec{}, host, dr.NewDefaultHookRunner(), log, stateDir)
	opts.DRStore = store
	opts.DROrchestrator = orch
	opts.DRFailover = dr.NewFailoverEngine(orch, nil, host, zfsrep.Exec{}, nil, dr.NewDefaultHookRunner(), log)
	log.Warn("disaster recovery is on (experimental, -dr-experimental)")
}
