package content

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onurbaydas/virtomir/internal/cluster"
	"github.com/onurbaydas/virtomir/internal/jobs"
	"github.com/onurbaydas/virtomir/internal/virt"
)

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// TestSubscriptionMatcher tests subscription matching:
// - matches mode "all"
// - matches specific host ID when selected
// - ignores unsubscribed host ID
// - verifies host has a configured TargetPool in HostConfig
func TestSubscriptionMatcher(t *testing.T) {
	st, err := Open("")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	// host-1: TargetPool configured
	if err := st.SetHostConfig(HostConfig{HostID: "host-1", TargetPool: "pool-a"}); err != nil {
		t.Fatalf("SetHostConfig host-1: %v", err)
	}
	// host-2: TargetPool configured
	if err := st.SetHostConfig(HostConfig{HostID: "host-2", TargetPool: "pool-b"}); err != nil {
		t.Fatalf("SetHostConfig host-2: %v", err)
	}
	// host-3: TargetPool configured
	if err := st.SetHostConfig(HostConfig{HostID: "host-3", TargetPool: "pool-c"}); err != nil {
		t.Fatalf("SetHostConfig host-3: %v", err)
	}
	// host-no-pool: TargetPool empty
	if err := st.SetHostConfig(HostConfig{HostID: "host-no-pool", TargetPool: ""}); err != nil {
		t.Fatalf("SetHostConfig host-no-pool: %v", err)
	}
	// host-unknown: not in HostConfigs

	sm := NewSyncManager(st, nil, nil, nil, nil, nil)

	// Mode "all"
	itemAll := Item{
		ID:         "item-1",
		Name:       "debian-all",
		SyncPolicy: SyncPolicy{Mode: SyncAll},
	}
	if !sm.IsSubscribed("host-1", itemAll) {
		t.Errorf("expected host-1 to match mode 'all'")
	}
	if !sm.IsSubscribed("host-2", itemAll) {
		t.Errorf("expected host-2 to match mode 'all'")
	}
	if sm.IsSubscribed("host-no-pool", itemAll) {
		t.Errorf("expected host-no-pool to NOT match because TargetPool is empty")
	}
	if sm.IsSubscribed("host-unknown", itemAll) {
		t.Errorf("expected host-unknown to NOT match because no HostConfig exists")
	}

	// Mode "selected" with specific host IDs
	itemSelected := Item{
		ID:         "item-2",
		Name:       "ubuntu-selected",
		SyncPolicy: SyncPolicy{Mode: SyncSelected, Hosts: []string{"host-1", "host-3"}},
	}
	if !sm.IsSubscribed("host-1", itemSelected) {
		t.Errorf("expected host-1 to match selected hosts")
	}
	if !sm.IsSubscribed("host-3", itemSelected) {
		t.Errorf("expected host-3 to match selected hosts")
	}
	if sm.IsSubscribed("host-2", itemSelected) {
		t.Errorf("expected host-2 to be unsubscribed / ignored")
	}

	// Mode "manual"
	itemManual := Item{
		ID:         "item-3",
		Name:       "rocky-manual",
		SyncPolicy: SyncPolicy{Mode: SyncManual},
	}
	if sm.IsSubscribed("host-1", itemManual) {
		t.Errorf("expected host-1 to NOT be subscribed for mode 'manual'")
	}
}

// TestAutoSyncTrigger verifies that calling OnItemUpdated on a subscribed host spawns
// a content-sync job in jobs.Manager.
func TestAutoSyncTrigger(t *testing.T) {
	st, err := Open("")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	targetDir := t.TempDir()
	selfID := "host-local"
	sourceHost := "host-remote"

	if err := st.SetHostConfig(HostConfig{HostID: selfID, TargetPool: targetDir}); err != nil {
		t.Fatalf("SetHostConfig: %v", err)
	}

	payload := []byte("auto-sync-test-payload-data")
	payloadSHA := sha256Hex(payload)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	peer := cluster.NewPeer(cluster.Node{ID: sourceHost, Address: server.URL}, server.Client())
	peerResolver := MapPeerResolver{
		sourceHost: peer,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	jm := jobs.New(ctx)
	sm := NewSyncManager(st, jm, nil, nil, peerResolver, nil)

	item := Item{
		ID:         "item-ubuntu",
		Name:       "ubuntu-server",
		Type:       TypeImage,
		SyncPolicy: SyncPolicy{Mode: SyncAll},
		Versions: []ItemVersion{
			{
				ID:         "v2404",
				Version:    "24.04",
				Format:     "qcow2",
				Size:       int64(len(payload)),
				SHA256:     payloadSHA,
				SourceHost: sourceHost,
			},
		},
	}

	// Call OnItemUpdated
	sm.OnItemUpdated(ctx, item, selfID)

	// Verify job was spawned in jobs.Manager
	jobList := jm.List()
	if len(jobList) == 0 {
		t.Fatalf("expected a content-sync job to be spawned in jobs.Manager, got 0")
	}

	var foundJob *jobs.Job
	for i := range jobList {
		if jobList[i].Kind == "content-sync" {
			foundJob = &jobList[i]
			break
		}
	}
	if foundJob == nil {
		t.Fatalf("expected job with kind 'content-sync', found: %+v", jobList)
	}

	expectedTitle := "Sync ubuntu-server (v24.04)"
	if foundJob.Title != expectedTitle {
		t.Errorf("job.Title = %q, want %q", foundJob.Title, expectedTitle)
	}

	// Wait for job to complete
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		j, ok := jm.Get(foundJob.ID)
		if ok && j.State != jobs.Running {
			if j.State != jobs.Succeeded {
				t.Fatalf("job failed: %s", j.Message)
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Verify downloaded file is now in target directory
	expectedFile := filepath.Join(targetDir, "content-ubuntu-server-v24.04.qcow2")
	if st, err := os.Stat(expectedFile); err != nil || st.Size() != int64(len(payload)) {
		t.Errorf("expected downloaded file %s with size %d, err: %v", expectedFile, len(payload), err)
	}
}

// TestRetentionCleanup verifies that when a new version syncs and exceeds KeepVersions,
// the pruned older version's volume file is deleted from disk.
func TestRetentionCleanup(t *testing.T) {
	st, err := Open("")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	targetDir := t.TempDir()
	selfID := "host-alpha"
	sourceHost := "host-beta"

	if err := st.SetHostConfig(HostConfig{HostID: selfID, TargetPool: targetDir}); err != nil {
		t.Fatalf("SetHostConfig: %v", err)
	}

	itemName := "alpine"
	// Pre-create local files for v1.0 and v2.0
	fileV1 := filepath.Join(targetDir, "content-alpine-v1.0.qcow2")
	fileV2 := filepath.Join(targetDir, "content-alpine-v2.0.qcow2")
	if err := os.WriteFile(fileV1, []byte("v1-content"), 0644); err != nil {
		t.Fatalf("write v1 file: %v", err)
	}
	if err := os.WriteFile(fileV2, []byte("v2-content"), 0644); err != nil {
		t.Fatalf("write v2 file: %v", err)
	}

	payloadV3 := []byte("v3-newest-image-content")
	payloadV3SHA := sha256Hex(payloadV3)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(payloadV3)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payloadV3)
	}))
	defer server.Close()

	peer := cluster.NewPeer(cluster.Node{ID: sourceHost, Address: server.URL}, server.Client())
	peerResolver := MapPeerResolver{
		sourceHost: peer,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	jm := jobs.New(ctx)
	sm := NewSyncManager(st, jm, nil, nil, peerResolver, nil)

	// Hook to verify pool refresh was triggered
	refreshedPool := ""
	sm.SetRefreshPoolHook(func(poolName string) error {
		refreshedPool = poolName
		return nil
	})

	// KeepVersions = 2, but Versions contains 3 versions (v1, v2, v3).
	// Therefore v1 is in excess and must be pruned upon syncing v3.
	item := Item{
		ID:           "item-alpine",
		Name:         itemName,
		Type:         TypeImage,
		KeepVersions: 2,
		SyncPolicy:   SyncPolicy{Mode: SyncAll},
		Versions: []ItemVersion{
			{
				ID:         "v1",
				Version:    "1.0",
				Format:     "qcow2",
				Size:       int64(len("v1-content")),
				SHA256:     sha256Hex([]byte("v1-content")),
				SourceHost: sourceHost,
			},
			{
				ID:         "v2",
				Version:    "2.0",
				Format:     "qcow2",
				Size:       int64(len("v2-content")),
				SHA256:     sha256Hex([]byte("v2-content")),
				SourceHost: sourceHost,
			},
			{
				ID:         "v3",
				Version:    "3.0",
				Format:     "qcow2",
				Size:       int64(len(payloadV3)),
				SHA256:     payloadV3SHA,
				SourceHost: sourceHost,
			},
		},
	}

	sm.OnItemUpdated(ctx, item, selfID)

	// Wait for the sync job to complete
	deadline := time.Now().Add(5 * time.Second)
	completed := false
	for time.Now().Before(deadline) {
		jobList := jm.List()
		if len(jobList) > 0 && jobList[0].State != jobs.Running {
			if jobList[0].State != jobs.Succeeded {
				t.Fatalf("sync job failed: %s", jobList[0].Message)
			}
			completed = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !completed {
		t.Fatalf("sync job timed out")
	}

	// 1. Older pruned version (v1.0) must be deleted from disk
	if _, err := os.Stat(fileV1); !os.IsNotExist(err) {
		t.Errorf("expected pruned file %s to be deleted, but it still exists", fileV1)
	}

	// 2. Kept version (v2.0) must still exist
	if _, err := os.Stat(fileV2); err != nil {
		t.Errorf("expected kept file %s to exist, err: %v", fileV2, err)
	}

	// 3. New version (v3.0) must exist
	fileV3 := filepath.Join(targetDir, "content-alpine-v3.0.qcow2")
	if st, err := os.Stat(fileV3); err != nil || st.Size() != int64(len(payloadV3)) {
		t.Errorf("expected synced file %s with size %d, err: %v", fileV3, len(payloadV3), err)
	}

	// 4. Refresh pool hook was called
	if refreshedPool != targetDir {
		t.Errorf("refreshedPool = %q, want %q", refreshedPool, targetDir)
	}
}

// TestReplicateToMembers verifies that ReplicateToMembers calls all mock peers with ?local=1
// and records failed hosts.
func TestReplicateToMembers(t *testing.T) {
	clusterPath := filepath.Join(t.TempDir(), "cluster.json")
	cl, err := cluster.Open(clusterPath, "node-1", "fp-node-1")
	if err != nil {
		t.Fatalf("cluster.Open: %v", err)
	}

	var peerACalled atomic.Bool
	var peerAPath atomic.Value

	serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peerACalled.Store(true)
		peerAPath.Store(r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer serverA.Close()

	// serverB returns 500 error to simulate member failure
	serverB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal error"}`))
	}))
	defer serverB.Close()

	nodeSelf := cl.Self("https://10.0.0.1:8420")
	nodeA := cluster.Node{ID: "node-a", Name: "member-a", Address: serverA.URL}
	nodeB := cluster.Node{ID: "node-b", Name: "member-b", Address: serverB.URL}

	if err := cl.Replace([]cluster.Node{nodeSelf, nodeA, nodeB}, 1); err != nil {
		t.Fatalf("cl.Replace: %v", err)
	}
	if !cl.Clustered() {
		t.Fatalf("expected cluster to be clustered")
	}

	peerA := cluster.NewPeer(nodeA, serverA.Client())
	peerB := cluster.NewPeer(nodeB, serverB.Client())

	resolver := MapPeerResolver{
		"node-a": peerA,
		"node-b": peerB,
	}

	ctx := context.Background()
	body := map[string]string{"name": "test-item"}

	// 1. Standard replication: node-a succeeds with ?local=1, node-b fails
	failed := ReplicateToMembers(ctx, cl, resolver, "admin", "admin", "token-123", http.MethodPost, "/api/v1/content/items", body)

	if !peerACalled.Load() {
		t.Errorf("expected peer A to be called")
	}
	pathCalled, _ := peerAPath.Load().(string)
	if !strings.HasSuffix(pathCalled, "?local=1") {
		t.Errorf("expected peer A to be called with ?local=1, got: %s", pathCalled)
	}

	if !slices.Contains(failed, "member-b") {
		t.Errorf("expected failed members to contain 'member-b', got: %v", failed)
	}
	if slices.Contains(failed, "member-a") {
		t.Errorf("did not expect successful member-a in failed list: %v", failed)
	}

	// 2. Unreachable / unresolvable member
	nodeC := cluster.Node{ID: "node-c", Name: "member-c", Address: "https://invalid"}
	_ = cl.Replace([]cluster.Node{nodeSelf, nodeA, nodeC}, 2)
	failedWithMissing := ReplicateToMembers(ctx, cl, resolver, "admin", "admin", "token-123", http.MethodPost, "/api/v1/content/items", body)
	if !slices.Contains(failedWithMissing, "member-c") {
		t.Errorf("expected unresolvable member-c to be in failed list, got: %v", failedWithMissing)
	}

	// 3. Skip replication when ?local=1 is already set
	peerACalled.Store(false)
	skipRes := ReplicateToMembers(ctx, cl, resolver, "admin", "admin", "token-123", http.MethodPost, "/api/v1/content/items?local=1", body)
	if skipRes != nil {
		t.Errorf("expected nil result when ?local=1 is present, got: %v", skipRes)
	}
	if peerACalled.Load() {
		t.Errorf("expected peer A NOT to be called when ?local=1 is already set")
	}

	// 4. Skip replication when standalone
	alonePath := filepath.Join(t.TempDir(), "alone.json")
	clAlone, _ := cluster.Open(alonePath, "node-alone", "fp-alone")
	aloneRes := ReplicateToMembers(ctx, clAlone, resolver, "admin", "admin", "token-123", http.MethodPost, "/api/v1/content/items", body)
	if aloneRes != nil {
		t.Errorf("expected nil result when standalone, got: %v", aloneRes)
	}
}

// TestReconcile verifies that Reconcile sweeps items and triggers OnItemUpdated only for
// subscribed items missing the newest version locally, without starting duplicate jobs.
func TestReconcile(t *testing.T) {
	st, err := Open("")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	targetDir := t.TempDir()
	selfID := "host-1"
	sourceHost := "host-2"

	if err := st.SetHostConfig(HostConfig{HostID: selfID, TargetPool: targetDir}); err != nil {
		t.Fatalf("SetHostConfig: %v", err)
	}

	payloadMissing := []byte("missing-payload")
	payloadPresent := []byte("already-present-payload")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(payloadMissing)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payloadMissing)
	}))
	defer server.Close()

	peer := cluster.NewPeer(cluster.Node{ID: sourceHost, Address: server.URL}, server.Client())
	peerResolver := MapPeerResolver{
		sourceHost: peer,
	}

	// Item 1: subscribed and missing locally
	itemMissing, err := st.CreateItem(Item{
		Name:       "missing-item",
		Type:       TypeImage,
		SyncPolicy: SyncPolicy{Mode: SyncAll},
	})
	if err != nil {
		t.Fatalf("CreateItem missing: %v", err)
	}
	itemMissing, _, err = st.AddVersion(itemMissing.ID, ItemVersion{
		ID:         "v1",
		Version:    "1.0",
		Format:     "qcow2",
		Size:       int64(len(payloadMissing)),
		SHA256:     sha256Hex(payloadMissing),
		SourceHost: sourceHost,
	})
	if err != nil {
		t.Fatalf("AddVersion missing: %v", err)
	}

	// Item 2: subscribed but already present locally
	itemPresent, err := st.CreateItem(Item{
		Name:       "present-item",
		Type:       TypeImage,
		SyncPolicy: SyncPolicy{Mode: SyncAll},
	})
	if err != nil {
		t.Fatalf("CreateItem present: %v", err)
	}
	itemPresent, _, err = st.AddVersion(itemPresent.ID, ItemVersion{
		ID:         "v1",
		Version:    "1.0",
		Format:     "qcow2",
		Size:       int64(len(payloadPresent)),
		SHA256:     sha256Hex(payloadPresent),
		SourceHost: sourceHost,
	})
	if err != nil {
		t.Fatalf("AddVersion present: %v", err)
	}
	presentPath := filepath.Join(targetDir, "content-present-item-v1.0.qcow2")
	if err := os.WriteFile(presentPath, payloadPresent, 0644); err != nil {
		t.Fatalf("write present file: %v", err)
	}

	// Item 3: unsubscribed (manual mode)
	itemUnsubscribed, err := st.CreateItem(Item{
		Name:       "unsub-item",
		Type:       TypeImage,
		SyncPolicy: SyncPolicy{Mode: SyncManual},
	})
	if err != nil {
		t.Fatalf("CreateItem unsub: %v", err)
	}
	_, _, err = st.AddVersion(itemUnsubscribed.ID, ItemVersion{
		ID:         "v1",
		Version:    "1.0",
		Format:     "qcow2",
		Size:       int64(len(payloadMissing)),
		SHA256:     sha256Hex(payloadMissing),
		SourceHost: sourceHost,
	})
	if err != nil {
		t.Fatalf("AddVersion unsub: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	jm := jobs.New(ctx)
	sm := NewSyncManager(st, jm, nil, nil, peerResolver, nil)

	// Run Reconcile
	sm.Reconcile(ctx, selfID)

	// Only missing-item should spawn a job
	jobList := jm.List()
	if len(jobList) != 1 {
		t.Fatalf("expected exactly 1 job spawned by Reconcile, got %d: %+v", len(jobList), jobList)
	}
	if !strings.Contains(jobList[0].Title, "missing-item") {
		t.Errorf("expected job for missing-item, got title: %s", jobList[0].Title)
	}

	// Run Reconcile again while job is running -> activeJobs prevents duplicate job
	sm.Reconcile(ctx, selfID)
	if len(jm.List()) != 1 {
		t.Errorf("expected duplicate job to be prevented by activeJobs, got %d jobs", len(jm.List()))
	}

	// The download job writes into targetDir; let it finish before the test removes it.
	cancel()
	deadline := time.Now().Add(5 * time.Second)
	for jm.List()[0].State == jobs.Running && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
}

// waitForJob blocks until jobID leaves the Running state and fails the test on error or timeout.
func waitForJob(t *testing.T, jm *jobs.Manager, jobID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		j, ok := jm.Get(jobID)
		if ok && j.State != jobs.Running {
			if j.State != jobs.Succeeded {
				t.Fatalf("sync job failed: %s", j.Message)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("sync job %s timed out", jobID)
}

// TestPublishReplicateSync_EndToEnd verifies the full publish → replicate → sync flow
// between two fake hosts: publish assigns a stable version ID, cluster replication of
// the item metadata keeps it, the transfer addresses it, the subscriber writes
// content-<item>-v<version>.<format> verified by SHA-256, refreshes the pool, and
// marks the subscriber host as synced.
func TestPublishReplicateSync_EndToEnd(t *testing.T) {
	storePub, err := Open("")
	if err != nil {
		t.Fatalf("Open publisher store: %v", err)
	}
	storeSub, err := Open("")
	if err != nil {
		t.Fatalf("Open subscriber store: %v", err)
	}

	srcDir := t.TempDir()
	srcVol := "cirros-0.6.3-base.img"
	payload := []byte("cirros-0.6.3-base-image-payload-bytes")
	srcPath := filepath.Join(srcDir, srcVol)
	if err := os.WriteFile(srcPath, payload, 0644); err != nil {
		t.Fatalf("write source volume: %v", err)
	}

	// 1. Publish on host A from a local volume
	pubCtx := WithVolumeList(context.Background(), func(ctx context.Context, client *virt.Client, pool string) ([]virt.Volume, error) {
		return []virt.Volume{{Name: srcVol, Path: srcPath}}, nil
	})
	itemPub, err := PublishVolume(pubCtx, nil, storePub, "host-a", PublishVolumeReq{
		Pool:       "images",
		Volume:     srcVol,
		Type:       TypeImage,
		Name:       "cirros-lib",
		Version:    "1.0",
		SyncPolicy: SyncPolicy{Mode: SyncAll},
	})
	if err != nil {
		t.Fatalf("PublishVolume: %v", err)
	}
	if len(itemPub.Versions) != 1 {
		t.Fatalf("published versions = %d, want 1", len(itemPub.Versions))
	}
	published := itemPub.Versions[0]
	if published.ID == "" {
		t.Fatal("publish did not assign a version ID")
	}

	// 2. Replicate the item metadata to host B (full item JSON as pushed by the API)
	raw, err := json.Marshal(itemPub)
	if err != nil {
		t.Fatalf("marshal replicated item: %v", err)
	}
	var replicated Item
	if err := json.Unmarshal(raw, &replicated); err != nil {
		t.Fatalf("unmarshal replicated item: %v", err)
	}
	if replicated.Versions[0].ID != published.ID {
		t.Fatalf("replication lost the version ID: got %q, want %q", replicated.Versions[0].ID, published.ID)
	}
	itemSub, err := storeSub.CreateItem(replicated)
	if err != nil {
		t.Fatalf("subscriber CreateItem: %v", err)
	}
	if itemSub.Versions[0].ID != published.ID {
		t.Errorf("subscriber stored version ID = %q, want %q", itemSub.Versions[0].ID, published.ID)
	}

	// 3. Fake source host serving the download endpoint for that version
	var gotPath atomic.Value
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath.Store(r.URL.RequestURI())
		if r.URL.Path == DownloadPath(itemPub.ID, published.ID) {
			ServeContent(w, r, srcPath, int64(len(payload)))
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	peer := cluster.NewPeer(cluster.Node{ID: "host-a", Address: ts.URL}, ts.Client())
	resolver := MapPeerResolver{"host-a": peer}

	dirB := t.TempDir()
	if err := storeSub.SetHostConfig(HostConfig{HostID: "host-b", TargetPool: dirB}); err != nil {
		t.Fatalf("SetHostConfig subscriber: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	jm := jobs.New(ctx)
	sm := NewSyncManager(storeSub, jm, nil, nil, resolver, nil)

	refreshedPool := ""
	sm.SetRefreshPoolHook(func(poolName string) error {
		refreshedPool = poolName
		return nil
	})

	// 4. Triggering auto-sync on the subscriber (ON /sync {} triggers the same flow with
	// the latest version) pulls once with resume support, refreshes the pool and more.
	sm.OnItemUpdated(ctx, itemSub, "host-b")

	jobList := jm.List()
	if len(jobList) != 1 {
		t.Fatalf("expected 1 sync job, got %d: %+v", len(jobList), jobList)
	}
	if jobList[0].Kind != "content-sync" {
		t.Errorf("job kind = %q, want content-sync", jobList[0].Kind)
	}
	waitForJob(t, jm, jobList[0].ID)

	// Transfer must have used the stable version ID in the download path
	wantPath := DownloadPath(itemPub.ID, published.ID)
	if p, _ := gotPath.Load().(string); p != wantPath {
		t.Errorf("download path = %q, want %q", p, wantPath)
	}

	// 5. The subscriber holds the copy under the canonical name, checksum-verified
	finalPath := filepath.Join(dirB, fmt.Sprintf("content-%s-v%s.%s", itemPub.Name, published.Version, published.Format))
	got, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("read synced file: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("synced content = %q, want %q", string(got), string(payload))
	}
	if refreshedPool != dirB {
		t.Errorf("refreshedPool = %q, want %q", refreshedPool, dirB)
	}

	// 6. Subscriber host marked synced
	it, ok := storeSub.GetItem(itemPub.ID)
	if !ok {
		t.Fatalf("item missing on subscriber")
	}
	foundSynced := false
	for _, h := range it.Hosts {
		if h.Host == "host-b" {
			if h.State != "synced" {
				t.Errorf("subscriber state = %q, want synced", h.State)
			}
			if h.Updated.IsZero() {
				t.Errorf("subscriber state has no timestamp")
			}
			foundSynced = true
		}
	}
	if !foundSynced {
		t.Errorf("subscriber host state missing: %+v", it.Hosts)
	}
}

// TestOnItemUpdatedSelectsLatestVersion verifies that automated sync picks the version with
// the newest Created timestamp through the production code path (Item.LatestVersion and
// SyncManager.OnItemUpdated), even when the versions slice is not ordered by time.
func TestOnItemUpdatedSelectsLatestVersion(t *testing.T) {
	st, err := Open("")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	targetDir := t.TempDir()
	selfID := "host-latest"
	sourceHost := "host-src"
	if err := st.SetHostConfig(HostConfig{HostID: selfID, TargetPool: targetDir}); err != nil {
		t.Fatalf("SetHostConfig: %v", err)
	}

	newest := []byte("newest-version-payload")
	oldest := []byte("oldest-version-payload")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(newest)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(newest)
	}))
	defer server.Close()

	peer := cluster.NewPeer(cluster.Node{ID: sourceHost, Address: server.URL}, server.Client())
	now := time.Now().UTC()

	item := Item{
		ID:         "item-latest",
		Name:       "unordered",
		Type:       TypeImage,
		SyncPolicy: SyncPolicy{Mode: SyncAll},
		Versions: []ItemVersion{
			// Slice order is newest-first: the last element is the OLDEST by Created.
			{ID: "v-new", Version: "2.0", Format: "img", Size: int64(len(newest)), SHA256: sha256Hex(newest), SourceHost: sourceHost, Created: now},
			{ID: "v-old", Version: "1.0", Format: "img", Size: int64(len(oldest)), SHA256: sha256Hex(oldest), SourceHost: sourceHost, Created: now.Add(-time.Hour)},
		},
	}

	if got := item.LatestVersion(); got == nil || got.ID != "v-new" {
		t.Fatalf("LatestVersion = %+v, want v-new", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	jm := jobs.New(ctx)
	sm := NewSyncManager(st, jm, nil, nil, MapPeerResolver{sourceHost: peer}, nil)

	sm.OnItemUpdated(ctx, item, selfID)

	jobList := jm.List()
	if len(jobList) != 1 {
		t.Fatalf("expected 1 job, got %d: %+v", len(jobList), jobList)
	}
	if want := "Sync unordered (v2.0)"; jobList[0].Title != want {
		t.Errorf("job title = %q, want %q (latest version must win)", jobList[0].Title, want)
	}
	waitForJob(t, jm, jobList[0].ID)

	if _, err := os.Stat(filepath.Join(targetDir, "content-unordered-v2.0.img")); err != nil {
		t.Errorf("newest version file was not synced: %v", err)
	}
	if _, err := os.Stat(filepath.Join(targetDir, "content-unordered-v1.0.img")); !os.IsNotExist(err) {
		t.Errorf("older version file should not have been synced: %v", err)
	}
}

// TestSyncJob_ChecksumMismatchFailsWithClearError verifies that a subscriber job whose
// transfer produces a mismatched SHA-256 fails with a clear message, leaves no file
// behind, and records host state failed.
func TestSyncJob_ChecksumMismatchFailsWithClearError(t *testing.T) {
	storeSub, err := Open("")
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}

	dirB := t.TempDir()
	if err := storeSub.SetHostConfig(HostConfig{HostID: "host-b", TargetPool: dirB}); err != nil {
		t.Fatalf("SetHostConfig: %v", err)
	}

	corrupt := []byte("tampered-payload-same-size!")
	expected := []byte("genuine-payload-same-size..")
	if len(corrupt) != len(expected) {
		t.Fatalf("test setup: payload sizes must match")
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(corrupt)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(corrupt)
	}))
	defer ts.Close()

	item, err := storeSub.CreateItem(Item{
		ID:         "item-e2e-bad",
		Name:       "bad-checksum",
		Type:       TypeImage,
		SyncPolicy: SyncPolicy{Mode: SyncAll},
		Versions: []ItemVersion{
			{
				ID:         "ver-bad-1",
				Version:    "1.0",
				Format:     "img",
				Size:       int64(len(expected)),
				SHA256:     sha256Hex(expected),
				SourceHost: "host-a",
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}

	peer := cluster.NewPeer(cluster.Node{ID: "host-a", Address: ts.URL}, ts.Client())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	jm := jobs.New(ctx)
	sm := NewSyncManager(storeSub, jm, nil, nil, MapPeerResolver{"host-a": peer}, nil)

	sm.OnItemUpdated(ctx, item, "host-b")

	jobList := jm.List()
	if len(jobList) != 1 {
		t.Fatalf("expected 1 sync job, got %d", len(jobList))
	}
	deadline := time.Now().Add(5 * time.Second)
	var done jobs.Job
	for time.Now().Before(deadline) {
		if j, ok := jm.Get(jobList[0].ID); ok && j.State != jobs.Running {
			done = j
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if done.State == jobs.Running {
		t.Fatal("sync job timed out")
	}
	if done.State != jobs.Failed {
		t.Errorf("job state = %q, want failed; message=%q", done.State, done.Message)
	}
	if !strings.Contains(done.Message, "checksum mismatch") {
		t.Errorf("job message = %q, want containing 'checksum mismatch'", done.Message)
	}

	if _, err := os.Stat(filepath.Join(dirB, "content-bad-checksum-v1.0.img")); !os.IsNotExist(err) {
		t.Errorf("partial/final file should not survive a checksum mismatch: %v", err)
	}

	it, _ := storeSub.GetItem(item.ID)
	for _, h := range it.Hosts {
		if h.Host == "host-b" && h.State != "failed" {
			t.Errorf("host state = %q, want failed", h.State)
		}
	}
}

// TestPublisherDoesNotSelfDownload verifies that the host which already holds the
// source volume never issues a download (to itself or to anyone) and marks itself
// as in sync.
func TestPublisherDoesNotSelfDownload(t *testing.T) {
	storePub, err := Open("")
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}

	dirA := t.TempDir()
	if err := storePub.SetHostConfig(HostConfig{HostID: "host-a", TargetPool: dirA}); err != nil {
		t.Fatalf("SetHostConfig: %v", err)
	}

	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	// Resolver even maps the publisher's own ID, so a broken self-download
	// attempt would succeed in reaching a peer.
	peer := cluster.NewPeer(cluster.Node{ID: "host-a", Address: ts.URL}, ts.Client())
	resolver := MapPeerResolver{"host-a": peer}

	item, err := storePub.CreateItem(Item{
		ID:         "item-self",
		Name:       "self-published",
		Type:       TypeImage,
		SyncPolicy: SyncPolicy{Mode: SyncAll},
		Versions: []ItemVersion{
			{
				ID:         "ver-self-1",
				Version:    "1.0",
				Format:     "qcow2",
				Size:       1234,
				SHA256:     sha256Hex([]byte("self-published-payload")),
				SourceHost: "host-a",
				SourceVol:  "self-published.qcow2",
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	jm := jobs.New(ctx)
	sm := NewSyncManager(storePub, jm, nil, nil, resolver, nil)

	sm.OnItemUpdated(ctx, item, "host-a")

	time.Sleep(150 * time.Millisecond)
	if n := hits.Load(); n != 0 {
		t.Errorf("publisher issued %d self-download request(s), want 0", n)
	}
	if jobs := jm.List(); len(jobs) != 0 {
		t.Errorf("expected no sync jobs on the publisher, got %+v", jobs)
	}

	it, ok := storePub.GetItem(item.ID)
	if !ok {
		t.Fatal("item missing after self-sync check")
	}
	found := false
	for _, h := range it.Hosts {
		if h.Host == "host-a" {
			if h.State != "synced" {
				t.Errorf("publisher host state = %q, want synced", h.State)
			}
			found = true
		}
	}
	if !found {
		t.Errorf("publisher host state not recorded: %+v", it.Hosts)
	}
}
