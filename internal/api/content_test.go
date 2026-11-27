package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/onurbaydas/virtomir/internal/activity"
	"github.com/onurbaydas/virtomir/internal/auth"
	"github.com/onurbaydas/virtomir/internal/cluster"
	"github.com/onurbaydas/virtomir/internal/content"
	"github.com/onurbaydas/virtomir/internal/jobs"
	"github.com/onurbaydas/virtomir/internal/virt"
)

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

type contentServerBundle struct {
	server  *Server
	handler http.Handler
	store   *content.Store
	poolDir string
	backend *fakeBackend
	acts    *activity.Log
	admin   *http.Cookie
	op      *http.Cookie
	view    *http.Cookie
}

func setupContentServer(t *testing.T) *contentServerBundle {
	t.Helper()
	dir := t.TempDir()
	poolDir := filepath.Join(dir, "pool")
	_ = os.MkdirAll(poolDir, 0755)

	store, err := content.Open(filepath.Join(dir, "content.json"))
	if err != nil {
		t.Fatal(err)
	}
	_ = store.SetHostConfig(content.HostConfig{
		HostID:     "test-host",
		TargetPool: "lib",
		AutoSync:   true,
	})

	m := auth.New(stubAuthn{}, func(u string) auth.Role {
		return map[string]auth.Role{"onur": auth.Admin, "op": auth.Operator, "view": auth.Viewer}[u]
	})
	tokens, _ := auth.OpenTokens("")
	acts, _ := activity.Open(filepath.Join(dir, "activity.jsonl"))
	backend := &fakeBackend{domains: []virt.Domain{}, pools: []virt.Pool{{Name: "lib", Path: poolDir, Active: true}}}
	jobMgr := jobs.New(t.Context())

	s := NewServer(Options{
		Backend:  backend,
		Live:     &fakeLive{},
		Jobs:     jobMgr,
		Auth:     m,
		Tokens:   tokens,
		Activity: acts,
		Content:  store,
		HostName: "test-host",
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	h := s.Handler()

	return &contentServerBundle{
		server:  s,
		handler: h,
		store:   store,
		poolDir: poolDir,
		backend: backend,
		acts:    acts,
		admin:   signIn(t, h, "onur"),
		op:      signIn(t, h, "op"),
		view:    signIn(t, h, "view"),
	}
}

func TestContentListItems(t *testing.T) {
	b := setupContentServer(t)

	// 1. Empty list
	rec := do(b.handler, "GET", "/api/v1/content/items", "", b.view)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/content/items empty: got %d %s", rec.Code, rec.Body)
	}
	var emptyResp struct {
		Items []ContentItemSummary `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &emptyResp); err != nil {
		t.Fatal(err)
	}
	if len(emptyResp.Items) != 0 {
		t.Fatalf("expected empty items list, got %d items", len(emptyResp.Items))
	}

	// 2. Populated list
	contentBytes := []byte("iso-file-bytes-1234567890")
	fileName := "content-debian-netinst-v12.0.iso"
	if err := os.WriteFile(filepath.Join(b.poolDir, fileName), contentBytes, 0644); err != nil {
		t.Fatal(err)
	}

	created, err := b.store.CreateItem(content.Item{
		Name: "debian-netinst",
		Type: content.TypeISO,
		Versions: []content.ItemVersion{
			{
				ID:         "ver-1",
				Version:    "12.0",
				Format:     "iso",
				Size:       int64(len(contentBytes)),
				SHA256:     sha256Hex(contentBytes),
				SourceHost: "test-host",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	rec = do(b.handler, "GET", "/api/v1/content/items", "", b.view)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/content/items populated: got %d %s", rec.Code, rec.Body)
	}
	var popResp struct {
		Items []ContentItemSummary `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &popResp); err != nil {
		t.Fatal(err)
	}
	if len(popResp.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(popResp.Items))
	}
	item := popResp.Items[0]
	if item.ID != created.ID || item.Name != "debian-netinst" {
		t.Errorf("unexpected item in response: %+v", item)
	}
	if item.VersionCount != 1 {
		t.Errorf("expected VersionCount = 1, got %d", item.VersionCount)
	}
	if item.LatestVersion == nil || item.LatestVersion.Version != "12.0" {
		t.Errorf("unexpected LatestVersion: %+v", item.LatestVersion)
	}
	if item.SyncStatus != "ready" {
		t.Errorf("expected syncStatus = 'ready', got %q", item.SyncStatus)
	}
}

func TestContentPublishItem(t *testing.T) {
	b := setupContentServer(t)

	body := `{
		"name": "ubuntu-cloud",
		"type": "image",
		"description": "Ubuntu 24.04 Cloud Image",
		"version": "24.04",
		"sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"size": 524288000,
		"format": "qcow2"
	}`

	rec := do(b.handler, "POST", "/api/v1/content/items", body, b.admin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/content/items: got %d %s, want 201", rec.Code, rec.Body)
	}

	var resp struct {
		Item content.Item `json:"item"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Item.Name != "ubuntu-cloud" {
		t.Errorf("item name = %q, want 'ubuntu-cloud'", resp.Item.Name)
	}
	if resp.Item.Type != content.TypeImage {
		t.Errorf("item type = %q, want 'image'", resp.Item.Type)
	}
	if len(resp.Item.Versions) != 1 || resp.Item.Versions[0].Version != "24.04" {
		t.Errorf("item versions unexpected: %+v", resp.Item.Versions)
	}

	stored, ok := b.store.GetItem(resp.Item.ID)
	if !ok {
		t.Fatalf("item %q was not saved to store", resp.Item.ID)
	}
	if stored.Name != "ubuntu-cloud" {
		t.Errorf("stored item name = %q, want 'ubuntu-cloud'", stored.Name)
	}
}

func TestContentDownloadRange(t *testing.T) {
	b := setupContentServer(t)

	sampleData := []byte("0123456789abcdefghijklmnopqrstuvwxyz")
	dataLen := int64(len(sampleData))
	fileName := "content-alpine-v3.20.iso"
	filePath := filepath.Join(b.poolDir, fileName)
	if err := os.WriteFile(filePath, sampleData, 0644); err != nil {
		t.Fatal(err)
	}

	item, err := b.store.CreateItem(content.Item{
		Name: "alpine",
		Type: content.TypeISO,
		Versions: []content.ItemVersion{
			{
				ID:         "v-alpine",
				Version:    "3.20",
				Format:     "iso",
				Size:       dataLen,
				SHA256:     sha256Hex(sampleData),
				SourceHost: "test-host",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	dlPath := fmt.Sprintf("/api/v1/content/items/%s/versions/%s/download", item.ID, item.Versions[0].ID)

	// 1. Full download without Range header (HTTP 200)
	rec := do(b.handler, "GET", dlPath, "", b.view)
	if rec.Code != http.StatusOK {
		t.Fatalf("download without Range: got %d %s, want 200", rec.Code, rec.Body)
	}
	if rec.Header().Get("Accept-Ranges") != "bytes" {
		t.Errorf("Accept-Ranges header missing or wrong: %q", rec.Header().Get("Accept-Ranges"))
	}
	if rec.Body.String() != string(sampleData) {
		t.Errorf("download body mismatch: got %q, want %q", rec.Body.String(), string(sampleData))
	}

	// 2. Partial download with Range header (HTTP 206)
	rec = do(b.handler, "GET", dlPath, "", b.view, "Range", "bytes=0-9")
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("download with Range: got %d %s, want 206", rec.Code, rec.Body)
	}
	expectedRange := fmt.Sprintf("bytes 0-9/%d", dataLen)
	if cr := rec.Header().Get("Content-Range"); cr != expectedRange {
		t.Errorf("Content-Range = %q, want %q", cr, expectedRange)
	}
	if rec.Body.String() != string(sampleData[0:10]) {
		t.Errorf("partial body mismatch: got %q, want %q", rec.Body.String(), string(sampleData[0:10]))
	}

	// Mid-range download: bytes=10-19
	rec = do(b.handler, "GET", dlPath, "", b.view, "Range", "bytes=10-19")
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("download mid-range: got %d %s, want 206", rec.Code, rec.Body)
	}
	if rec.Body.String() != string(sampleData[10:20]) {
		t.Errorf("mid-range body mismatch: got %q, want %q", rec.Body.String(), string(sampleData[10:20]))
	}
}

func TestContentDeployJob(t *testing.T) {
	b := setupContentServer(t)

	item, err := b.store.CreateItem(content.Item{
		Name: "test-tpl",
		Type: content.TypeISO,
		Versions: []content.ItemVersion{
			{
				ID:         "v-1",
				Version:    "1.0",
				Format:     "iso",
				Size:       1024,
				SHA256:     strings.Repeat("a", 64),
				SourceHost: "test-host",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	deployBody := `{
		"name": "deployed-vm-1",
		"pool": "default"
	}`

	rec := do(b.handler, "POST", "/api/v1/content/items/"+item.ID+"/deploy", deployBody, b.op)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST deploy: got %d %s, want 202", rec.Code, rec.Body)
	}

	var job jobs.Job
	if err := json.Unmarshal(rec.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	if job.Kind != "content-deploy" {
		t.Errorf("job.Kind = %q, want 'content-deploy'", job.Kind)
	}
	if job.ID == "" {
		t.Error("job.ID is empty")
	}

	// Wait for job to finish
	var finishedJob jobs.Job
	for i := 0; i < 100; i++ {
		j, ok := b.server.jobs.Get(job.ID)
		if ok && j.State != jobs.Running {
			finishedJob = j
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if finishedJob.State == jobs.Running || finishedJob.State == "" {
		t.Fatal("deploy job did not complete in time")
	}
}

func TestContentRoleGating(t *testing.T) {
	b := setupContentServer(t)

	item, err := b.store.CreateItem(content.Item{
		Name: "gate-item",
		Type: content.TypeISO,
		Versions: []content.ItemVersion{
			{
				ID:         "v1",
				Version:    "1.0",
				Format:     "iso",
				Size:       1024,
				SHA256:     strings.Repeat("b", 64),
				SourceHost: "test-host",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	publishPayload := `{"name":"another-item","type":"iso","version":"1.0","sha256":"` + strings.Repeat("c", 64) + `","size":100,"format":"iso"}`
	deployPayload := `{"name":"gate-vm","pool":"default"}`

	// 1. Viewer: cannot publish (403), cannot delete (403), cannot deploy (403), CAN list (200)
	if rec := do(b.handler, "POST", "/api/v1/content/items", publishPayload, b.view); rec.Code != http.StatusForbidden {
		t.Errorf("viewer publish: got %d, want 403", rec.Code)
	}
	if rec := do(b.handler, "DELETE", "/api/v1/content/items/"+item.ID, "", b.view); rec.Code != http.StatusForbidden {
		t.Errorf("viewer delete: got %d, want 403", rec.Code)
	}
	if rec := do(b.handler, "POST", "/api/v1/content/items/"+item.ID+"/deploy", deployPayload, b.view); rec.Code != http.StatusForbidden {
		t.Errorf("viewer deploy: got %d, want 403", rec.Code)
	}
	if rec := do(b.handler, "GET", "/api/v1/content/items", "", b.view); rec.Code != http.StatusOK {
		t.Errorf("viewer list: got %d, want 200", rec.Code)
	}

	// 2. Operator: cannot publish (403), cannot delete (403), CAN deploy (202), CAN list (200)
	if rec := do(b.handler, "POST", "/api/v1/content/items", publishPayload, b.op); rec.Code != http.StatusForbidden {
		t.Errorf("operator publish: got %d, want 403", rec.Code)
	}
	if rec := do(b.handler, "DELETE", "/api/v1/content/items/"+item.ID, "", b.op); rec.Code != http.StatusForbidden {
		t.Errorf("operator delete: got %d, want 403", rec.Code)
	}
	if rec := do(b.handler, "POST", "/api/v1/content/items/"+item.ID+"/deploy", deployPayload, b.op); rec.Code != http.StatusAccepted {
		t.Errorf("operator deploy: got %d, want 202", rec.Code)
	}

	// 3. Admin: CAN publish (201), CAN deploy (202), CAN delete (204)
	if rec := do(b.handler, "POST", "/api/v1/content/items", publishPayload, b.admin); rec.Code != http.StatusCreated {
		t.Errorf("admin publish: got %d %s, want 201", rec.Code, rec.Body)
	}
	if rec := do(b.handler, "POST", "/api/v1/content/items/"+item.ID+"/deploy", deployPayload, b.admin); rec.Code != http.StatusAccepted {
		t.Errorf("admin deploy: got %d, want 202", rec.Code)
	}
	if rec := do(b.handler, "DELETE", "/api/v1/content/items/"+item.ID, "", b.admin); rec.Code != http.StatusNoContent {
		t.Errorf("admin delete: got %d %s, want 204", rec.Code, rec.Body)
	}
}

func newContentTestNode(t *testing.T, name string) (*testNode, *content.Store) {
	t.Helper()
	dir := t.TempDir()
	cert, err := auth.SelfSignedCert(filepath.Join(dir, "tls"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(nil)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	fp := cluster.Fingerprint(cert.Certificate[0])
	clStore, err := cluster.Open(filepath.Join(dir, "cluster.json"), name, fp)
	if err != nil {
		t.Fatal(err)
	}
	cntStore, err := content.Open(filepath.Join(dir, "content.json"))
	if err != nil {
		t.Fatal(err)
	}

	m := auth.New(stubAuthn{}, func(u string) auth.Role {
		return map[string]auth.Role{"onur": auth.Admin, "op": auth.Operator, "view": auth.Viewer}[u]
	})
	acts, _ := activity.Open("")
	b := &fakeBackend{domains: []virt.Domain{}}
	lv := &fakeLive{}
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())

	s := NewServer(Options{
		Backend:  b,
		Live:     lv,
		Auth:     m,
		Activity: acts,
		Version:  "v-" + name,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Cluster:  clStore, Fingerprint: fp, HostName: name, ListenPort: port,
		Content: cntStore,
	})
	srv.Config.Handler = s.Handler()

	return &testNode{url: srv.URL, store: clStore, backend: b, acts: acts, lv: lv, api: s, srv: srv}, cntStore
}

func TestContentClusterLocalReplication(t *testing.T) {
	nodeA, storeA := newContentTestNode(t, "alpha")
	nodeB, storeB := newContentTestNode(t, "beta")
	joinNodes(t, nodeA, nodeB)

	adminA := nodeA.signIn(t, "onur")

	// 1. Send request with ?local=1 on Node A: updates local store on Node A without forwarding to Node B
	localItemBody := `{
		"id": "local-item-1",
		"name": "local-item",
		"type": "iso",
		"version": "1.0",
		"sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"size": 1024,
		"format": "iso"
	}`

	resp, body := adminA.req("POST", "/api/v1/content/items?local=1", localItemBody)
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		t.Fatalf("POST ?local=1 on node A: got %d %s", resp.StatusCode, body)
	}

	// Verify present in Store A
	if _, ok := storeA.GetItem("local-item-1"); !ok {
		t.Error("expected local-item-1 in Store A")
	}

	// Verify NOT replicated to Store B (since local=1 was specified)
	if _, ok := storeB.GetItem("local-item-1"); ok {
		t.Error("expected local-item-1 NOT to be replicated to Store B when ?local=1 was used")
	}

	// 2. Normal publish (without local=1) on Node A replicates to Node B
	replicatedItemBody := `{
		"id": "cluster-item-2",
		"name": "cluster-item",
		"type": "image",
		"version": "2.0",
		"sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"size": 2048,
		"format": "qcow2"
	}`

	resp, body = adminA.req("POST", "/api/v1/content/items", replicatedItemBody)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST normal on node A: got %d %s", resp.StatusCode, body)
	}

	// Verify present in Store A
	if _, ok := storeA.GetItem("cluster-item-2"); !ok {
		t.Error("expected cluster-item-2 in Store A")
	}

	// Verify replicated to Store B
	if _, ok := storeB.GetItem("cluster-item-2"); !ok {
		t.Error("expected cluster-item-2 to be replicated to Store B")
	}
}

func TestContentSyncItem(t *testing.T) {
	b := setupContentServer(t)

	contentBytes := []byte("sync-test-data-payload-123")
	fileName := "content-sync-item-v1.0.iso"
	filePath := filepath.Join(b.poolDir, fileName)
	if err := os.WriteFile(filePath, contentBytes, 0644); err != nil {
		t.Fatal(err)
	}

	itemPresent, err := b.store.CreateItem(content.Item{
		Name: "sync-item",
		Type: content.TypeISO,
		Versions: []content.ItemVersion{
			{
				ID:         "v-present",
				Version:    "1.0",
				Format:     "iso",
				Size:       int64(len(contentBytes)),
				SHA256:     sha256Hex(contentBytes),
				SourceHost: "test-host",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// 1. Sync when artifact is already present in target pool -> Success
	rec := do(b.handler, "POST", "/api/v1/content/items/"+itemPresent.ID+"/sync", "", b.op)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("sync present item: got %d %s, want 202", rec.Code, rec.Body)
	}
	var job jobs.Job
	if err := json.Unmarshal(rec.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		j, ok := b.server.jobs.Get(job.ID)
		if ok && j.State != jobs.Running {
			job = j
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if job.State != jobs.Succeeded {
		t.Fatalf("sync present item job failed: state=%s msg=%s", job.State, job.Message)
	}

	// 2. Sync when artifact is missing on source host -> Job fails
	itemMissing, err := b.store.CreateItem(content.Item{
		Name: "missing-item",
		Type: content.TypeISO,
		Versions: []content.ItemVersion{
			{
				ID:         "v-missing",
				Version:    "1.0",
				Format:     "iso",
				Size:       5000,
				SHA256:     strings.Repeat("e", 64),
				SourceHost: "test-host",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	rec = do(b.handler, "POST", "/api/v1/content/items/"+itemMissing.ID+"/sync", "", b.op)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("sync missing item: got %d %s, want 202", rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		j, ok := b.server.jobs.Get(job.ID)
		if ok && j.State != jobs.Running {
			job = j
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if job.State != jobs.Failed {
		t.Fatalf("expected sync job for missing artifact to fail, got %s", job.State)
	}
	if !strings.Contains(job.Message, "not found") {
		t.Errorf("job message = %q, expected 'not found'", job.Message)
	}

	// 3. 404 on nonexistent item
	rec = do(b.handler, "POST", "/api/v1/content/items/nonexistent-item-id/sync", "", b.op)
	if rec.Code != http.StatusNotFound {
		t.Errorf("sync nonexistent item: got %d, want 404", rec.Code)
	}

	// 4. 404 on nonexistent version ID
	rec = do(b.handler, "POST", "/api/v1/content/items/"+itemPresent.ID+"/sync", `{"versionId":"v-unknown"}`, b.op)
	if rec.Code != http.StatusNotFound {
		t.Errorf("sync nonexistent version: got %d, want 404", rec.Code)
	}
}

func TestContentConfig(t *testing.T) {
	b := setupContentServer(t)

	// 1. GET /content/config returns host config
	rec := do(b.handler, "GET", "/api/v1/content/config", "", b.view)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /content/config: got %d %s, want 200", rec.Code, rec.Body)
	}
	var cfg content.HostConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.HostID != "test-host" {
		t.Errorf("HostID = %q, want 'test-host'", cfg.HostID)
	}
	if cfg.TargetPool != "lib" {
		t.Errorf("TargetPool = %q, want lib", cfg.TargetPool)
	}
	if !cfg.AutoSync {
		t.Errorf("AutoSync = %v, want true", cfg.AutoSync)
	}

	// 2. PUT /content/config role gating: viewer & operator forbidden (403)
	payload := `{"targetPool":"custom-pool","autoSync":false}`
	if rec := do(b.handler, "PUT", "/api/v1/content/config", payload, b.view); rec.Code != http.StatusForbidden {
		t.Errorf("viewer PUT /content/config: got %d, want 403", rec.Code)
	}
	if rec := do(b.handler, "PUT", "/api/v1/content/config", payload, b.op); rec.Code != http.StatusForbidden {
		t.Errorf("operator PUT /content/config: got %d, want 403", rec.Code)
	}

	// 3. PUT /content/config with different hostId -> 400 Bad Request
	foreignPayload := `{"hostId":"foreign-node-id","targetPool":"custom-pool","autoSync":false}`
	rec = do(b.handler, "PUT", "/api/v1/content/config", foreignPayload, b.admin)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT /content/config foreign host: got %d %s, want 400", rec.Code, rec.Body)
	}

	// 4. PUT /content/config with admin -> 200 OK and updates store
	rec = do(b.handler, "PUT", "/api/v1/content/config", payload, b.admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /content/config valid: got %d %s, want 200", rec.Code, rec.Body)
	}
	rec = do(b.handler, "GET", "/api/v1/content/config", "", b.view)
	if err := json.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.TargetPool != "custom-pool" || cfg.AutoSync != false {
		t.Errorf("config not updated properly: %+v", cfg)
	}
}

func TestContentDownloadErrorPaths(t *testing.T) {
	b := setupContentServer(t)

	item, err := b.store.CreateItem(content.Item{
		Name: "dl-item",
		Type: content.TypeISO,
		Versions: []content.ItemVersion{
			{
				ID:         "v-1",
				Version:    "1.0",
				Format:     "iso",
				Size:       100,
				SHA256:     strings.Repeat("f", 64),
				SourceHost: "test-host",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// 1. Nonexistent item -> 404
	rec := do(b.handler, "GET", "/api/v1/content/items/nonexistent-item/versions/v-1/download", "", b.view)
	if rec.Code != http.StatusNotFound {
		t.Errorf("download nonexistent item: got %d, want 404", rec.Code)
	}

	// 2. Nonexistent version -> 404
	rec = do(b.handler, "GET", "/api/v1/content/items/"+item.ID+"/versions/v-nonexistent/download", "", b.view)
	if rec.Code != http.StatusNotFound {
		t.Errorf("download nonexistent version: got %d, want 404", rec.Code)
	}

	// 3. Artifact file missing on host -> 404
	rec = do(b.handler, "GET", "/api/v1/content/items/"+item.ID+"/versions/v-1/download", "", b.view)
	if rec.Code != http.StatusNotFound {
		t.Errorf("download missing artifact file: got %d, want 404", rec.Code)
	}

	// 4. Invalid HTTP method (POST on download with admin) -> 405 Method Not Allowed
	rec = do(b.handler, "POST", "/api/v1/content/items/"+item.ID+"/versions/v-1/download", "", b.admin)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST to download endpoint: got %d, want 405", rec.Code)
	}

	// 5. POST to download with viewer -> 403 Forbidden
	rec = do(b.handler, "POST", "/api/v1/content/items/"+item.ID+"/versions/v-1/download", "", b.view)
	if rec.Code != http.StatusForbidden {
		t.Errorf("POST to download endpoint with viewer: got %d, want 403", rec.Code)
	}
}

// TestContentDownloadPeerAuth verifies the host-to-host content transfer auth (ADR-0063):
// a cluster member downloading with its peer credential succeeds, everyone without
// peer credentials is refused.
func TestContentDownloadPeerAuth(t *testing.T) {
	pub, pubStore := newContentTestNode(t, "pubhost")
	sub, _ := newContentTestNode(t, "subhost")
	joinNodes(t, sub, pub)

	poolDir := t.TempDir()
	pub.backend.pools = []virt.Pool{{Name: "lib", Path: poolDir, Active: true}}
	if err := pubStore.SetHostConfig(content.HostConfig{
		HostID:     pub.store.SelfID(),
		TargetPool: "lib",
		AutoSync:   true,
	}); err != nil {
		t.Fatal(err)
	}

	sample := []byte("peer-download-payload-0123456789")
	if err := os.WriteFile(filepath.Join(poolDir, "content-cirros-lib-v1.0.iso"), sample, 0644); err != nil {
		t.Fatal(err)
	}
	item, err := pubStore.CreateItem(content.Item{
		Name: "cirros-lib",
		Type: content.TypeISO,
		Versions: []content.ItemVersion{
			{
				ID:         "ver-peer-1",
				Version:    "1.0",
				Format:     "iso",
				Size:       int64(len(sample)),
				SHA256:     sha256Hex(sample),
				SourceHost: pub.store.SelfID(),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ver := item.Versions[0]

	// 1. The subscriber's own peer connection downloads the version; until the guard
	//    allowed it, every such sync ended in "peer returned HTTP 403".
	peer, ok := sub.api.PeerFor(pub.store.SelfID())
	if !ok {
		t.Fatal("subscriber has no peer connection to the publisher")
	}
	target := t.TempDir()
	got, err := content.Pull(context.Background(), peer, item, ver, target, nil)
	if err != nil {
		t.Fatalf("peer download: %v", err)
	}
	if read, err := os.ReadFile(got); err != nil {
		t.Fatal(err)
	} else if !bytes.Equal(read, sample) {
		t.Errorf("downloaded file = %q, want %q", read, sample)
	}

	// 2. Range/resume support survives the auth path.
	resp, err := peer.Download(context.Background(), content.DownloadPath(item.ID, ver.ID),
		map[string]string{"Range": "bytes=10-13"})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		t.Errorf("range download: got %d, want 206", resp.StatusCode)
	}
	if string(body) != string(sample[10:14]) {
		t.Errorf("range download body = %q, want %q", body, sample[10:14])
	}

	// 3. Anonymous download without any credential -> 401 (sign-in required).
	anon := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	dl := pub.url + content.DownloadPath(item.ID, ver.ID)
	if resp, err := anon.Get(dl); err != nil {
		t.Fatal(err)
	} else {
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("anonymous download: got %d, want 401", resp.StatusCode)
		}
	}

	// 4. A forged peer credential from a host that is not a member -> 401.
	req, _ := http.NewRequest(http.MethodGet, dl, nil)
	req.Header.Set("Authorization", "VirtoMirPeer forged.deadbeef")
	if resp, err := anon.Do(req); err != nil {
		t.Fatal(err)
	} else {
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("forged peer credential: got %d, want 401", resp.StatusCode)
		}
	}
}

func TestContentEvaluateSyncStatusValidation(t *testing.T) {
	b := setupContentServer(t)

	contentBytes := []byte("exact-matching-bytes-sha")
	fileName := "content-val-item-v1.0.iso"
	filePath := filepath.Join(b.poolDir, fileName)
	if err := os.WriteFile(filePath, contentBytes, 0644); err != nil {
		t.Fatal(err)
	}

	// 1. Size <= 0 with matching SHA256 -> ready
	itemReady, err := b.store.CreateItem(content.Item{
		Name: "val-item",
		Type: content.TypeISO,
		Versions: []content.ItemVersion{
			{
				ID:         "v-1",
				Version:    "1.0",
				Format:     "iso",
				Size:       0, // size <= 0
				SHA256:     sha256Hex(contentBytes),
				SourceHost: "test-host",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	status := b.server.evaluateSyncStatus(itemReady, &itemReady.Versions[0], "lib")
	if status != "ready" {
		t.Errorf("expected 'ready' when SHA256 matches and Size <= 0, got %q", status)
	}

	// 2. Size <= 0 with non-matching SHA256 -> missing (not ready!)
	itemMismatch, err := b.store.CreateItem(content.Item{
		Name: "val-item-mismatch",
		Type: content.TypeISO,
		Versions: []content.ItemVersion{
			{
				ID:         "v-1",
				Version:    "1.0",
				Format:     "iso",
				Size:       0, // size <= 0
				SHA256:     strings.Repeat("0", 64),
				SourceHost: "test-host",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	statusMismatch := b.server.evaluateSyncStatus(itemMismatch, &itemMismatch.Versions[0], "lib")
	if statusMismatch != "missing" {
		t.Errorf("expected 'missing' when SHA256 mismatches, got %q", statusMismatch)
	}

	// 3. Substring job title does NOT trigger false positive "syncing"
	// Suppose a job is running for "debian"
	_ = b.server.jobs.Start("content-sync", "Sync debian (v1.0)", func(ctx context.Context, report func(string)) (string, error) {
		time.Sleep(200 * time.Millisecond)
		return "", nil
	})

	itemDeb := content.Item{
		ID:   "deb-id",
		Name: "deb", // substring of "debian"
		Type: content.TypeISO,
	}
	debVer := content.ItemVersion{ID: "v1", Version: "1.0", Format: "iso"}
	debStatus := b.server.evaluateSyncStatus(itemDeb, &debVer, "lib")
	if debStatus == "syncing" {
		t.Errorf("expected 'deb' NOT to match 'Sync debian (v1.0)' job, but got %q", debStatus)
	}
}

// TestContentEvaluateItemHosts verifies the standalone per-host state derivation:
// a locally present copy marks self synced, the source host is synced, subscribed policy
// hosts are pending, and a stored failed state is surfaced with its reason.
func TestContentEvaluateItemHosts(t *testing.T) {
	b := setupContentServer(t)

	contentBytes := []byte("evaluate-hosts-local-copy")
	fileName := "content-eval-item-v1.0.iso"
	if err := os.WriteFile(filepath.Join(b.poolDir, fileName), contentBytes, 0644); err != nil {
		t.Fatal(err)
	}

	item, err := b.store.CreateItem(content.Item{
		Name:       "eval-item",
		Type:       content.TypeISO,
		SourceHost: "host-remote",
		SyncPolicy: content.SyncPolicy{Mode: content.SyncSelected, Hosts: []string{"policy-host"}},
		Versions: []content.ItemVersion{
			{
				ID:         "v1",
				Version:    "1.0",
				Format:     "iso",
				Size:       int64(len(contentBytes)),
				SHA256:     sha256Hex(contentBytes),
				SourceHost: "host-remote",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	states := map[string]string{}
	for _, h := range b.server.evaluateItemHosts(item) {
		states[h.Host] = h.State
	}
	if states["test-host"] != "synced" {
		t.Errorf("self state = %q, want synced (local copy present); all=%v", states["test-host"], states)
	}
	if states["host-remote"] != "synced" {
		t.Errorf("source host state = %q, want synced; all=%v", states["host-remote"], states)
	}
	if states["policy-host"] != "pending" {
		t.Errorf("policy host state = %q, want pending; all=%v", states["policy-host"], states)
	}

	// Missing locally plus a stored failed state for self -> failed with the reason carried through.
	item2, err := b.store.CreateItem(content.Item{
		Name:       "eval-item-missing",
		Type:       content.TypeISO,
		SourceHost: "host-remote",
		Versions: []content.ItemVersion{
			{
				ID:         "v1",
				Version:    "1.0",
				Format:     "iso",
				Size:       1500,
				SHA256:     strings.Repeat("a", 64),
				SourceHost: "host-remote",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.store.SetHostSyncState(item2.ID, content.HostSyncState{
		Host:  "test-host",
		State: "failed",
		Error: "peer unreachable",
	}); err != nil {
		t.Fatal(err)
	}
	item2, _ = b.store.GetItem(item2.ID)

	var selfState, selfErr string
	for _, h := range b.server.evaluateItemHosts(item2) {
		if h.Host == "test-host" {
			selfState, selfErr = h.State, h.Error
		}
	}
	if selfState != "failed" {
		t.Errorf("self state = %q, want failed when missing locally and marked failed", selfState)
	}
	if selfErr != "peer unreachable" {
		t.Errorf("self error = %q, want 'peer unreachable'", selfErr)
	}
}

// TestContentEvaluateItemHostsClusterMerge verifies that every cluster member appears in
// the per-host sync state, with the source host synced and the rest pending.
func TestContentEvaluateItemHostsClusterMerge(t *testing.T) {
	node, _ := newContentTestNode(t, "alpha")

	if !node.store.Clustered() {
		self := node.store.Self("https://10.0.0.1:8420")
		peer := cluster.Node{ID: "peer-1", Name: "peer-one"}
		if err := node.store.Replace([]cluster.Node{self, peer}, 1); err != nil {
			t.Fatalf("cluster.Replace: %v", err)
		}
	}
	if !node.store.Clustered() {
		t.Fatal("expected a two-member cluster")
	}
	selfID := node.store.SelfID()

	it := content.Item{
		ID:         "cluster-eval",
		Name:       "cluster-eval",
		Type:       content.TypeImage,
		SourceHost: "remote-src",
		SyncPolicy: content.SyncPolicy{Mode: content.SyncSelected, Hosts: []string{"policy-host"}},
		Versions: []content.ItemVersion{
			{
				ID:         "v1",
				Version:    "1.0",
				Format:     "qcow2",
				Size:       42,
				SHA256:     strings.Repeat("b", 64),
				SourceHost: "remote-src",
			},
		},
	}

	states := map[string]string{}
	for _, h := range node.api.evaluateItemHosts(it) {
		states[h.Host] = h.State
	}
	if states[selfID] != "pending" {
		t.Errorf("self state = %q, want pending; all=%v", states[selfID], states)
	}
	if states["peer-1"] != "pending" {
		t.Errorf("cluster peer state = %q, want pending; all=%v", states["peer-1"], states)
	}
	if states["remote-src"] != "synced" {
		t.Errorf("source host state = %q, want synced; all=%v", states["remote-src"], states)
	}
	if states["policy-host"] != "pending" {
		t.Errorf("policy host state = %q, want pending; all=%v", states["policy-host"], states)
	}
}

func TestContentActivityLogging(t *testing.T) {
	b := setupContentServer(t)

	body := `{
		"name": "audit-item",
		"type": "iso",
		"version": "1.0",
		"sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"size": 100,
		"format": "iso"
	}`

	rec := do(b.handler, "POST", "/api/v1/content/items", body, b.admin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST item: got %d %s", rec.Code, rec.Body)
	}

	var resp struct {
		Item content.Item `json:"item"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)

	// Check activity log contains action and details
	entries := b.acts.List(10, activity.Filter{Target: resp.Item.ID})
	if len(entries) == 0 {
		t.Fatalf("expected activity entry for item %s, found none", resp.Item.ID)
	}
	foundAction := false
	for _, e := range entries {
		if strings.Contains(e.Error, "publish-item") && strings.Contains(e.Error, "audit-item") {
			foundAction = true
			break
		}
	}
	if !foundAction {
		t.Errorf("activity entry did not record action/details properly: %+v", entries)
	}
}
