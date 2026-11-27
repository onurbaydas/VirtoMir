// Package content manages the cluster-wide content library for VirtoMir (ADR-0063).
// This file implements cluster-wide replication of library definitions and automatic
// propagation of new versions to subscribed hosts.
package content

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/onurbaydas/virtomir/internal/cluster"
	"github.com/onurbaydas/virtomir/internal/jobs"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// PeerCaller sends an RPC request to a cluster peer.
type PeerCaller interface {
	Call(ctx context.Context, method, path string, in, out any) error
}

// PeerResolver resolves a cluster host ID to an active peer connection.
type PeerResolver interface {
	PeerFor(hostID string) (*cluster.Peer, bool)
}

// MapPeerResolver adapts a map[string]*cluster.Peer to the PeerResolver interface.
type MapPeerResolver map[string]*cluster.Peer

// PeerFor looks up the peer for hostID.
func (m MapPeerResolver) PeerFor(hostID string) (*cluster.Peer, bool) {
	p, ok := m[hostID]
	return p, ok
}

// ReplicateToMembers repeats a content library definition change on all other cluster members.
// It skips replication if the host is standalone or if ?local=1 is already set.
// It iterates over all cluster members except self, calling each peer at path+"?local=1"
// via peer.As(user, role, token).Call() with a 10s timeout.
// It logs warnings on unreachable members and returns a list of failed member names.
func ReplicateToMembers(ctx context.Context, cl *cluster.Store, peers PeerResolver, user, role, token, method, path string, body any) []string {
	if cl == nil || !cl.Clustered() {
		return nil
	}
	if u, err := url.Parse(path); err == nil && u.Query().Get("local") != "" {
		return nil
	}
	if strings.Contains(path, "local=1") {
		return nil
	}
	if peers == nil {
		return nil
	}

	selfID := cl.SelfID()
	members, _ := cl.Members()
	if len(members) <= 1 {
		return nil
	}

	callPath := path
	if strings.Contains(callPath, "?") {
		callPath += "&local=1"
	} else {
		callPath += "?local=1"
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	var failed []string

	for _, n := range members {
		if n.ID == selfID {
			continue
		}
		wg.Add(1)
		go func(node cluster.Node) {
			defer wg.Done()
			peer, ok := peers.PeerFor(node.ID)
			memberName := node.Name
			if memberName == "" {
				memberName = node.ID
			}

			if !ok || peer == nil {
				slog.Warn("content change not copied to member: peer not found", "member", memberName, "id", node.ID)
				mu.Lock()
				failed = append(failed, memberName)
				mu.Unlock()
				return
			}

			callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()

			p := peer.As(user, role, token)
			if err := p.Call(callCtx, method, callPath, body, nil); err != nil {
				slog.Warn("content change not copied to member", "member", memberName, "err", err)
				mu.Lock()
				failed = append(failed, memberName)
				mu.Unlock()
			}
		}(n)
	}

	wg.Wait()
	if len(failed) == 0 {
		return nil
	}
	sort.Strings(failed)
	return failed
}

// SyncManager manages automated synchronization of content library items across cluster hosts.
type SyncManager struct {
	store      *Store
	jobs       *jobs.Manager
	client     *virt.Client
	cluster    *cluster.Store
	peers      PeerResolver
	log        *slog.Logger
	activeMu   sync.Mutex
	activeJobs map[string]string

	poolPathFn    func(poolName string) (string, error)
	refreshPoolFn func(poolName string) error
}

// NewSyncManager creates a SyncManager responsible for cluster-wide content synchronization.
func NewSyncManager(store *Store, jm *jobs.Manager, client *virt.Client, cl *cluster.Store, peers PeerResolver, log *slog.Logger) *SyncManager {
	if log == nil {
		log = slog.Default()
	}
	return &SyncManager{
		store:      store,
		jobs:       jm,
		client:     client,
		cluster:    cl,
		peers:      peers,
		log:        log,
		activeJobs: make(map[string]string),
	}
}

// SetPoolPathResolver configures a custom pool path resolution function (useful for tests).
func (m *SyncManager) SetPoolPathResolver(fn func(poolName string) (string, error)) {
	m.poolPathFn = fn
}

// SetRefreshPoolHook configures a custom storage pool refresh hook (useful for tests).
func (m *SyncManager) SetRefreshPoolHook(fn func(poolName string) error) {
	m.refreshPoolFn = fn
}

// SetPeers configures the peer resolver used to connect to cluster hosts.
func (m *SyncManager) SetPeers(peers PeerResolver) {
	m.peers = peers
}

func (m *SyncManager) logger() *slog.Logger {
	if m.log != nil {
		return m.log
	}
	return slog.Default()
}

func (m *SyncManager) resolvePoolPath(poolName string) (string, error) {
	if m.poolPathFn != nil {
		return m.poolPathFn(poolName)
	}
	if st, err := os.Stat(poolName); err == nil && st.IsDir() {
		return poolName, nil
	}
	if m.client != nil {
		pools, err := m.client.ListPools()
		if err != nil {
			return "", fmt.Errorf("list storage pools: %w", err)
		}
		for _, p := range pools {
			if p.Name == poolName {
				if p.Path != "" {
					return p.Path, nil
				}
				return "", fmt.Errorf("storage pool %q has no filesystem path", poolName)
			}
		}
	}
	return "", fmt.Errorf("storage pool %q not found", poolName)
}

// IsSubscribed reports whether hostID is subscribed to item and has a configured TargetPool in HostConfig.
func (m *SyncManager) IsSubscribed(hostID string, item Item) bool {
	if m.store == nil || strings.TrimSpace(hostID) == "" {
		return false
	}
	cfg, ok := m.store.GetHostConfig(hostID)
	if !ok || strings.TrimSpace(cfg.TargetPool) == "" {
		return false
	}
	if item.SyncPolicy.Mode == SyncAll || item.SyncPolicy.Mode == "all" {
		return true
	}
	return slices.Contains(item.SyncPolicy.Hosts, hostID)
}

// OnItemUpdated checks whether selfID is subscribed to item and triggers an automated sync job
// if the newest version is not yet present in the local TargetPool.
func (m *SyncManager) OnItemUpdated(ctx context.Context, item Item, selfID string) {
	if selfID == "" && m.cluster != nil {
		selfID = m.cluster.SelfID()
	}
	if !m.IsSubscribed(selfID, item) {
		return
	}
	if len(item.Versions) == 0 {
		return
	}

	latestPtr := item.LatestVersion()
	if latestPtr == nil {
		return
	}
	latestVersion := *latestPtr

	if latestVersion.SourceHost == "" || latestVersion.SourceHost == selfID {
		// Publisher holds the source volume and must not download from itself; mark as synced.
		if m.store != nil {
			_, _ = m.store.SetHostSyncState(item.ID, HostSyncState{
				Host:    selfID,
				State:   "synced",
				Updated: time.Now().UTC(),
			})
		}
		return
	}

	cfg, ok := m.store.GetHostConfig(selfID)
	if !ok || strings.TrimSpace(cfg.TargetPool) == "" {
		return
	}

	poolPath, err := m.resolvePoolPath(cfg.TargetPool)
	if err != nil {
		m.logger().Warn("cannot sync content: target pool path not resolved", "pool", cfg.TargetPool, "err", err)
		return
	}

	finalFile, err := FileName(item.Name, latestVersion.Version, latestVersion.Format)
	if err != nil {
		m.logger().Warn("cannot sync content", "item", item.Name, "err", err)
		return
	}
	finalPath := filepath.Join(poolPath, finalFile)
	if st, err := os.Stat(finalPath); err == nil && !st.IsDir() {
		if latestVersion.Size <= 0 || st.Size() == latestVersion.Size {
			// Already present locally
			if m.store != nil {
				_, _ = m.store.SetHostSyncState(item.ID, HostSyncState{
					Host:    selfID,
					State:   "synced",
					Updated: time.Now().UTC(),
				})
			}
			return
		}
	}

	jobKey := item.ID + ":" + latestVersion.ID
	if latestVersion.ID == "" {
		jobKey = item.ID + ":" + latestVersion.Version
	}

	m.activeMu.Lock()
	if m.activeJobs == nil {
		m.activeJobs = make(map[string]string)
	}
	if _, running := m.activeJobs[jobKey]; running {
		m.activeMu.Unlock()
		return
	}
	m.activeJobs[jobKey] = "pending"
	m.activeMu.Unlock()

	if m.peers == nil {
		m.logger().Warn("cannot sync content: peer resolver is nil", "item", item.Name, "version", latestVersion.Version)
		m.activeMu.Lock()
		delete(m.activeJobs, jobKey)
		m.activeMu.Unlock()
		return
	}

	peer, ok := m.peers.PeerFor(latestVersion.SourceHost)
	if !ok || peer == nil {
		m.logger().Warn("cannot sync content: peer for source host not found", "sourceHost", latestVersion.SourceHost, "item", item.Name, "version", latestVersion.Version)
		m.activeMu.Lock()
		delete(m.activeJobs, jobKey)
		m.activeMu.Unlock()
		return
	}

	if m.jobs == nil {
		m.logger().Warn("cannot sync content: jobs manager is nil")
		m.activeMu.Lock()
		delete(m.activeJobs, jobKey)
		m.activeMu.Unlock()
		return
	}

	if m.store != nil {
		_, _ = m.store.SetHostSyncState(item.ID, HostSyncState{
			Host:    selfID,
			State:   "syncing",
			Updated: time.Now().UTC(),
		})
	}

	jobTitle := fmt.Sprintf("Sync %s (v%s)", item.Name, latestVersion.Version)
	job := m.jobs.Start("content-sync", jobTitle, func(jobCtx context.Context, report func(string)) (string, error) {
		defer func() {
			m.activeMu.Lock()
			delete(m.activeJobs, jobKey)
			m.activeMu.Unlock()
		}()

		targetPath, err := Pull(jobCtx, peer, item, latestVersion, poolPath, report)
		if err != nil {
			m.logger().Warn("content sync failed", "item", item.Name, "version", latestVersion.Version, "err", err)
			if m.store != nil {
				_, _ = m.store.SetHostSyncState(item.ID, HostSyncState{
					Host:    selfID,
					State:   "failed",
					Error:   err.Error(),
					Updated: time.Now().UTC(),
				})
			}
			return "", err
		}

		if m.refreshPoolFn != nil {
			if err := m.refreshPoolFn(cfg.TargetPool); err != nil {
				m.logger().Warn("refresh storage pool hook failed", "pool", cfg.TargetPool, "err", err)
			}
		} else if m.client != nil {
			if err := m.client.PoolAction(cfg.TargetPool, "refresh"); err != nil {
				m.logger().Warn("failed to refresh storage pool after sync", "pool", cfg.TargetPool, "err", err)
			}
		}

		keep := item.KeepVersions
		if keep <= 0 {
			keep = 3
		}
		if len(item.Versions) > keep {
			excess := len(item.Versions) - keep
			for i := 0; i < excess; i++ {
				prunedVer := item.Versions[i]
				if err := PruneLocalFile(poolPath, item.Name, prunedVer.Version, prunedVer.Format); err != nil {
					m.logger().Warn("failed to prune local file for older version", "item", item.Name, "version", prunedVer.Version, "err", err)
				} else {
					m.logger().Info("pruned local file for older version", "item", item.Name, "version", prunedVer.Version)
				}
			}
		}

		if m.store != nil {
			_, _ = m.store.SetHostSyncState(item.ID, HostSyncState{
				Host:    selfID,
				State:   "synced",
				Updated: time.Now().UTC(),
			})
		}

		m.logger().Info("content item synced successfully", "item", item.Name, "version", latestVersion.Version, "path", targetPath)
		return targetPath, nil
	})

	m.activeMu.Lock()
	m.activeJobs[jobKey] = job.ID
	m.activeMu.Unlock()
}

// Reconcile sweeps all items in store and triggers OnItemUpdated for each subscribed
// item where the newest version is missing locally and not currently syncing.
func (m *SyncManager) Reconcile(ctx context.Context, selfID string) {
	if m.store == nil {
		return
	}
	if selfID == "" && m.cluster != nil {
		selfID = m.cluster.SelfID()
	}
	items := m.store.ListItems()
	for _, item := range items {
		if ctx.Err() != nil {
			return
		}
		if len(item.Versions) == 0 {
			continue
		}
		if !m.IsSubscribed(selfID, item) {
			continue
		}
		m.OnItemUpdated(ctx, item, selfID)
	}
}

// StartPeriodicReconcile triggers Reconcile immediately and then periodically at interval
// until ctx is cancelled.
func (m *SyncManager) StartPeriodicReconcile(ctx context.Context, selfID string, interval time.Duration) {
	go func() {
		m.Reconcile(ctx, selfID)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.Reconcile(ctx, selfID)
			}
		}
	}()
}

// Start starts periodic content reconciliation in the background.
func (m *SyncManager) Start(ctx context.Context) {
	selfID := ""
	if m.cluster != nil {
		selfID = m.cluster.SelfID()
	}
	m.StartPeriodicReconcile(ctx, selfID, time.Minute)
}
