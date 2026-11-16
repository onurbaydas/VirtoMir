package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/onurbaydas/virtomir/internal/activity"
	"github.com/onurbaydas/virtomir/internal/cluster"
	"github.com/onurbaydas/virtomir/internal/content"
	"github.com/onurbaydas/virtomir/internal/jobs"
	"github.com/onurbaydas/virtomir/internal/virt"
	"libvirt.org/go/libvirtxml"
)

// ContentItemSummary enriches a catalog item with version metadata and local host sync state.
type ContentItemSummary struct {
	content.Item
	VersionCount  int                  `json:"versionCount"`
	LatestVersion *content.ItemVersion `json:"latestVersion,omitempty"`
	SyncStatus    string               `json:"syncStatus"` // ready, syncing, missing
}

func (s *Server) contentRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/content/items", s.listContentItems)
	mux.HandleFunc("POST /api/v1/content/items", s.publishContentItem)
	mux.HandleFunc("GET /api/v1/content/items/{id}", s.getContentItem)
	mux.HandleFunc("PUT /api/v1/content/items/{id}", s.updateContentItem)
	mux.HandleFunc("DELETE /api/v1/content/items/{id}", s.deleteContentItem)
	mux.HandleFunc("POST /api/v1/content/items/{id}/versions", s.publishContentVersion)
	mux.HandleFunc("DELETE /api/v1/content/items/{id}/versions/{vid}", s.deleteContentVersion)
	mux.HandleFunc("GET /api/v1/content/items/{id}/versions/{vid}/download", s.downloadContentVersion)
	mux.HandleFunc("POST /api/v1/content/items/{id}/sync", s.syncContentItem)
	mux.HandleFunc("GET /api/v1/content/config", s.getContentConfig)
	mux.HandleFunc("PUT /api/v1/content/config", s.updateContentConfig)
	mux.HandleFunc("POST /api/v1/content/items/{id}/deploy", s.deployContentItem)
}

func (s *Server) selfHostID() string {
	if s.cluster != nil && s.cluster.SelfID() != "" {
		return s.cluster.SelfID()
	}
	if s.hostName != "" {
		return s.hostName
	}
	h, _ := os.Hostname()
	return h
}

func (s *Server) hostTargetPool() string {
	selfID := s.selfHostID()
	if s.content != nil {
		if cfg, ok := s.content.GetHostConfig(selfID); ok && strings.TrimSpace(cfg.TargetPool) != "" {
			return strings.TrimSpace(cfg.TargetPool)
		}
	}
	return "default"
}

func (s *Server) resolvePoolPath(poolName string) (string, error) {
	if st, err := os.Stat(poolName); err == nil && st.IsDir() {
		return poolName, nil
	}
	if s.backend != nil {
		pools, err := s.backend.ListPools()
		if err == nil {
			for _, p := range pools {
				if p.Name == poolName {
					if p.Path != "" {
						return p.Path, nil
					}
					return "", fmt.Errorf("storage pool %q has no filesystem path", poolName)
				}
			}
		}
	}
	return "", fmt.Errorf("storage pool %q not found", poolName)
}

func artifactFileMatches(path string, size int64, sha256Hex string) bool {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return false
	}
	if size > 0 {
		return st.Size() == size
	}
	if sha256Hex != "" {
		hash, _, err := content.StreamFileSHA256(context.Background(), path)
		return err == nil && strings.EqualFold(hash, sha256Hex)
	}
	return st.Size() > 0
}

func (s *Server) evaluateSyncStatus(it content.Item, v *content.ItemVersion, targetPool string) string {
	if v == nil {
		return "missing"
	}
	if s.jobs != nil {
		expectedTitle := fmt.Sprintf("Sync %s (v%s)", it.Name, v.Version)
		expectedTitleID := fmt.Sprintf("Sync %s (v%s)", it.ID, v.Version)
		for _, j := range s.jobs.List() {
			if j.Kind == "content-sync" && j.State == jobs.Running {
				if j.Title == expectedTitle || j.Title == expectedTitleID {
					return "syncing"
				}
			}
		}
	}
	poolPath, err := s.resolvePoolPath(targetPool)
	if err != nil {
		return "missing"
	}
	finalFile := fmt.Sprintf("content-%s-v%s.%s", it.Name, v.Version, v.Format)
	candidates := []string{filepath.Join(poolPath, finalFile)}
	if v.SourceVol != "" {
		candidates = append(candidates, filepath.Join(poolPath, v.SourceVol))
	}
	for _, p := range candidates {
		if artifactFileMatches(p, v.Size, v.SHA256) {
			return "ready"
		}
	}
	return "missing"
}

func (s *Server) findLocalArtifactFile(it content.Item, v content.ItemVersion) (string, error) {
	targetPool := s.hostTargetPool()
	poolPath, _ := s.resolvePoolPath(targetPool)
	expectedName := fmt.Sprintf("content-%s-v%s.%s", it.Name, v.Version, v.Format)

	var candidates []string
	if poolPath != "" {
		candidates = append(candidates, filepath.Join(poolPath, expectedName))
		if v.SourceVol != "" {
			candidates = append(candidates, filepath.Join(poolPath, v.SourceVol))
		}
	}

	if v.SourcePool != "" && v.SourcePool != targetPool {
		if srcPath, err := s.resolvePoolPath(v.SourcePool); err == nil && srcPath != "" {
			candidates = append(candidates, filepath.Join(srcPath, expectedName))
			if v.SourceVol != "" {
				candidates = append(candidates, filepath.Join(srcPath, v.SourceVol))
			}
		}
	}

	if v.TemplateXML != "" {
		var x libvirtxml.Domain
		if err := x.Unmarshal(v.TemplateXML); err == nil {
			disks := virt.TemplateDisks(&x)
			if len(disks) > 0 && disks[0].Source != nil && disks[0].Source.File != nil && disks[0].Source.File.File != "" {
				candidates = append(candidates, disks[0].Source.File.File)
			}
		}
	}

	for _, p := range candidates {
		if artifactFileMatches(p, v.Size, v.SHA256) {
			return p, nil
		}
	}
	return "", content.ErrNotFound
}

func contentErrStatus(err error) int {
	if errors.Is(err, content.ErrNotFound) {
		return http.StatusNotFound
	}
	if errors.Is(err, content.ErrConflict) {
		return http.StatusConflict
	}
	if errors.Is(err, content.ErrInvalid) || errors.Is(err, content.ErrRefused) {
		return http.StatusUnprocessableEntity
	}
	return http.StatusInternalServerError
}

func (s *Server) logContentActivity(r *http.Request, action, id, details string, status int, err error) {
	if s.activity == nil {
		return
	}
	ident := identity(r)

	msg := action
	if id != "" && details != "" {
		msg = fmt.Sprintf("%s %s (%s)", action, details, id)
	} else if id != "" {
		msg = fmt.Sprintf("%s %s", action, id)
	} else if details != "" {
		msg = fmt.Sprintf("%s %s", action, details)
	}
	if err != nil {
		msg = fmt.Sprintf("%s: %v", msg, err)
	}

	path := r.URL.Path
	if id != "" && !strings.Contains(path, id) {
		path = fmt.Sprintf("%s/%s", strings.TrimSuffix(path, "/"), id)
	}

	s.record(activity.Entry{
		Time:   time.Now().UTC(),
		User:   ident.Name,
		Token:  ident.Token,
		Remote: r.RemoteAddr,
		Method: r.Method,
		Path:   path,
		Status: status,
		Error:  msg,
	})
}

// contentToMembers repeats a content library definition change on the other cluster members.
func (s *Server) contentToMembers(r *http.Request, method, path string, body any) []HostResult {
	if r.URL.Query().Get("local") != "" || s.cluster == nil || !s.cluster.Clustered() {
		return nil
	}
	id := identity(r)
	members, _ := s.cluster.Members()
	results := make([]HostResult, 0, len(members))
	var wg sync.WaitGroup
	callPath := path
	if strings.Contains(callPath, "?") {
		callPath += "&local=1"
	} else {
		callPath += "?local=1"
	}
	for _, n := range members {
		if n.ID == s.cluster.SelfID() {
			continue
		}
		results = append(results, HostResult{Host: n.Name})
		res := &results[len(results)-1]
		wg.Add(1)
		go func(n cluster.Node) {
			defer wg.Done()
			res.OK = true
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			defer cancel()
			if err := s.peer(n).As(id.Name, id.Role.String(), id.Token).Call(ctx, method, callPath, body, nil); err != nil {
				res.OK, res.Error = false, err.Error()
				s.log.Warn("content change not copied to a member", "member", n.Name, "err", err)
			}
		}(n)
	}
	wg.Wait()
	return results
}

// listContentItems returns all catalog items with their version count, latest version metadata,
// and local sync status evaluated against this host's storage pool.
func (s *Server) listContentItems(w http.ResponseWriter, _ *http.Request) {
	if s.content == nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"items": []ContentItemSummary{}})
		return
	}
	items := s.content.ListItems()
	targetPool := s.hostTargetPool()

	res := make([]ContentItemSummary, 0, len(items))
	for _, it := range items {
		summary := ContentItemSummary{
			Item:         it,
			VersionCount: len(it.Versions),
		}
		if latest := it.LatestVersion(); latest != nil {
			summary.LatestVersion = latest
			summary.SyncStatus = s.evaluateSyncStatus(it, latest, targetPool)
		} else {
			summary.SyncStatus = "missing"
		}
		res = append(res, summary)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"items": res})
}

type publishItemReq struct {
	TemplateUUID string                  `json:"templateUuid,omitempty"`
	Pool         string                  `json:"pool,omitempty"`
	Volume       string                  `json:"volume,omitempty"`
	ID           string                  `json:"id,omitempty"`
	Name         string                  `json:"name,omitempty"`
	Type         content.ItemType        `json:"type,omitempty"`
	Description  string                  `json:"description,omitempty"`
	Version      string                  `json:"version,omitempty"`
	KeepVersions int                     `json:"keepVersions,omitempty"`
	SyncPolicy   content.SyncPolicy      `json:"syncPolicy,omitempty"`
	SourceHost   string                  `json:"sourceHost,omitempty"`
	SourcePool   string                  `json:"sourcePool,omitempty"`
	SourceVol    string                  `json:"sourceVol,omitempty"`
	Created      time.Time               `json:"created,omitempty"`
	Updated      time.Time               `json:"updated,omitempty"`
	Versions     []content.ItemVersion   `json:"versions,omitempty"`
	Hosts        []content.HostSyncState `json:"hosts,omitempty"`

	SHA256      string `json:"sha256,omitempty"`
	Size        int64  `json:"size,omitempty"`
	Format      string `json:"format,omitempty"`
	TemplateXML string `json:"templateXml,omitempty"`
}

// publishContentItem registers a new catalog item or publishes from a local template or volume.
func (s *Server) publishContentItem(w http.ResponseWriter, r *http.Request) {
	if s.content == nil {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "content library is not available on this host"})
		return
	}
	var req publishItemReq
	if !s.decode(w, r, &req) {
		return
	}

	selfID := s.selfHostID()
	isLocal := r.URL.Query().Get("local") != ""
	client, ok := s.backend.(*virt.Client)

	var created content.Item
	var err error

	switch {
	case req.TemplateUUID != "" && !isLocal:
		if (!ok || client == nil) && !content.HasDomainLookupHook(r.Context()) {
			s.writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "hypervisor client is not available"})
			return
		}
		created, err = content.PublishTemplate(r.Context(), client, s.content, selfID, content.PublishTemplateReq{
			TemplateUUID: req.TemplateUUID,
			Name:         req.Name,
			Version:      req.Version,
			Description:  req.Description,
			KeepVersions: req.KeepVersions,
			SyncPolicy:   req.SyncPolicy,
		})
	case req.Pool != "" && req.Volume != "" && !isLocal:
		if (!ok || client == nil) && !content.HasVolumeListHook(r.Context()) {
			s.writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "hypervisor client is not available"})
			return
		}
		created, err = content.PublishVolume(r.Context(), client, s.content, selfID, content.PublishVolumeReq{
			Pool:         req.Pool,
			Volume:       req.Volume,
			Type:         req.Type,
			Name:         req.Name,
			Version:      req.Version,
			Description:  req.Description,
			KeepVersions: req.KeepVersions,
			SyncPolicy:   req.SyncPolicy,
		})
	case len(req.Versions) > 0:
		item := content.Item{
			ID:           req.ID,
			Name:         req.Name,
			Type:         req.Type,
			Description:  req.Description,
			SourceHost:   req.SourceHost,
			KeepVersions: req.KeepVersions,
			SyncPolicy:   req.SyncPolicy,
			Created:      req.Created,
			Updated:      req.Updated,
			Versions:     req.Versions,
			Hosts:        req.Hosts,
		}
		if item.SourceHost == "" {
			item.SourceHost = selfID
		}
		for i := range item.Versions {
			if item.Versions[i].ID == "" {
				item.Versions[i].ID = content.RandomHex(8)
			}
			if item.Versions[i].SourceHost == "" {
				item.Versions[i].SourceHost = item.SourceHost
			}
		}
		created, err = s.content.CreateItem(item)
		if isLocal && errors.Is(err, content.ErrConflict) {
			created, err = s.content.UpdateItem(item)
		}
		if err == nil && s.contentSync != nil && isLocal {
			go s.contentSync.OnItemUpdated(context.Background(), created, selfID)
		}
	default:
		item := content.Item{
			ID:           req.ID,
			Name:         req.Name,
			Type:         req.Type,
			Description:  req.Description,
			SourceHost:   req.SourceHost,
			KeepVersions: req.KeepVersions,
			SyncPolicy:   req.SyncPolicy,
			Created:      req.Created,
			Updated:      req.Updated,
			Hosts:        req.Hosts,
		}
		if item.SourceHost == "" {
			item.SourceHost = selfID
		}
		if req.Version != "" || req.SHA256 != "" {
			verID := req.ID
			if verID == "" {
				verID = content.RandomHex(8)
			}
			ver := content.ItemVersion{
				ID:          verID,
				Version:     req.Version,
				Description: req.Description,
				SHA256:      req.SHA256,
				Size:        req.Size,
				Format:      req.Format,
				TemplateXML: req.TemplateXML,
				SourceHost:  item.SourceHost,
				SourcePool:  req.SourcePool,
				SourceVol:   req.SourceVol,
				Created:     req.Created,
			}
			created, err = content.PublishArtifact(s.content, item, ver)
		} else {
			created, err = s.content.CreateItem(item)
		}
		if isLocal && errors.Is(err, content.ErrConflict) {
			created, err = s.content.UpdateItem(item)
		}
		if err == nil && s.contentSync != nil && isLocal {
			go s.contentSync.OnItemUpdated(context.Background(), created, selfID)
		}
	}

	if err != nil {
		s.logContentActivity(r, "publish-item", req.ID, req.Name, contentErrStatus(err), err)
		s.writeJSON(w, contentErrStatus(err), map[string]string{"error": err.Error()})
		return
	}

	results := s.contentToMembers(r, http.MethodPost, "/api/v1/content/items", created)
	s.logContentActivity(r, "publish-item", created.ID, created.Name, http.StatusCreated, nil)

	answer := map[string]any{"item": created}
	if results != nil {
		answer["results"] = results
	}
	s.done(w, http.StatusCreated, answer)
}

func (s *Server) evaluateItemHosts(it content.Item) []content.HostSyncState {
	selfID := s.selfHostID()
	targetPool := s.hostTargetPool()

	latestVer := it.LatestVersion()

	selfStatus := s.evaluateSyncStatus(it, latestVer, targetPool)
	var localState string
	switch selfStatus {
	case "syncing":
		localState = "syncing"
	case "ready":
		localState = "synced"
	default:
		if latestVer != nil && (latestVer.SourceHost == "" || latestVer.SourceHost == selfID) {
			localState = "synced"
		} else if it.SourceHost == selfID {
			localState = "synced"
		} else {
			localState = "pending"
		}
	}

	var localErr string
	for _, h := range it.Hosts {
		if h.Host == selfID {
			if h.State == "failed" && localState == "pending" {
				localState = "failed"
				localErr = h.Error
			}
			break
		}
	}

	hostMap := make(map[string]content.HostSyncState)
	for _, h := range it.Hosts {
		hostMap[h.Host] = h
	}

	hostMap[selfID] = content.HostSyncState{
		Host:    selfID,
		State:   localState,
		Error:   localErr,
		Updated: time.Now().UTC(),
	}

	srcHost := it.SourceHost
	if latestVer != nil && latestVer.SourceHost != "" {
		srcHost = latestVer.SourceHost
	}
	if srcHost != "" {
		if cur, exists := hostMap[srcHost]; !exists {
			hostMap[srcHost] = content.HostSyncState{
				Host:    srcHost,
				State:   "synced",
				Updated: it.Updated,
			}
		} else if cur.State == "pending" {
			cur.State = "synced"
			hostMap[srcHost] = cur
		}
	}

	if s.cluster != nil && s.cluster.Clustered() {
		members, _ := s.cluster.Members()
		for _, m := range members {
			hID := m.ID
			if hID == "" {
				hID = m.Name
			}
			if _, exists := hostMap[hID]; !exists {
				state := "pending"
				if hID == srcHost {
					state = "synced"
				}
				hostMap[hID] = content.HostSyncState{
					Host:    hID,
					State:   state,
					Updated: it.Updated,
				}
			}
		}
	}
	for _, hID := range it.SyncPolicy.Hosts {
		if _, exists := hostMap[hID]; !exists {
			state := "pending"
			if hID == srcHost {
				state = "synced"
			}
			hostMap[hID] = content.HostSyncState{
				Host:    hID,
				State:   state,
				Updated: it.Updated,
			}
		}
	}

	hosts := make([]content.HostSyncState, 0, len(hostMap))
	for _, h := range hostMap {
		hosts = append(hosts, h)
	}
	sort.Slice(hosts, func(i, j int) bool {
		return hosts[i].Host < hosts[j].Host
	})

	return hosts
}

// getContentItem returns item details with all its versions and per-host sync state.
func (s *Server) getContentItem(w http.ResponseWriter, r *http.Request) {
	if s.content == nil {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "content library is not available on this host"})
		return
	}
	id := r.PathValue("id")
	it, ok := s.content.GetItem(id)
	if !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "item " + id + " not found"})
		return
	}
	it.Hosts = s.evaluateItemHosts(it)
	s.writeJSON(w, http.StatusOK, it)
}

// updateContentItem updates name, description, keepVersions, or syncPolicy of an item.
func (s *Server) updateContentItem(w http.ResponseWriter, r *http.Request) {
	if s.content == nil {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "content library is not available on this host"})
		return
	}
	var it content.Item
	if !s.decode(w, r, &it) {
		return
	}
	it.ID = r.PathValue("id")
	updated, err := s.content.UpdateItem(it)
	if err != nil {
		s.logContentActivity(r, "update-item", it.ID, it.Name, contentErrStatus(err), err)
		s.writeJSON(w, contentErrStatus(err), map[string]string{"error": err.Error()})
		return
	}
	results := s.contentToMembers(r, http.MethodPut, "/api/v1/content/items/"+it.ID, updated)
	s.logContentActivity(r, "update-item", updated.ID, updated.Name, http.StatusOK, nil)

	answer := map[string]any{"item": updated}
	if results != nil {
		answer["results"] = results
	}
	s.done(w, http.StatusOK, answer)
}

// deleteContentItem removes an item from the library and prunes its files locally.
func (s *Server) deleteContentItem(w http.ResponseWriter, r *http.Request) {
	if s.content == nil {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "content library is not available on this host"})
		return
	}
	id := r.PathValue("id")
	it, ok := s.content.GetItem(id)
	if !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "item " + id + " not found"})
		return
	}

	if err := s.content.DeleteItem(id); err != nil {
		s.logContentActivity(r, "delete-item", id, it.Name, contentErrStatus(err), err)
		s.writeJSON(w, contentErrStatus(err), map[string]string{"error": err.Error()})
		return
	}

	poolPath, _ := s.resolvePoolPath(s.hostTargetPool())
	if poolPath != "" {
		for _, v := range it.Versions {
			_ = content.PruneLocalFile(poolPath, it.Name, v.Version, v.Format)
		}
	}

	results := s.contentToMembers(r, http.MethodDelete, "/api/v1/content/items/"+id, nil)
	s.logContentActivity(r, "delete-item", id, it.Name, http.StatusNoContent, nil)

	if slices.ContainsFunc(results, func(h HostResult) bool { return !h.OK }) {
		s.done(w, http.StatusOK, map[string]any{"results": results})
		return
	}
	s.done(w, http.StatusNoContent, nil)
}

// publishContentVersion adds a new version to an existing catalog item.
func (s *Server) publishContentVersion(w http.ResponseWriter, r *http.Request) {
	if s.content == nil {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "content library is not available on this host"})
		return
	}
	id := r.PathValue("id")
	it, ok := s.content.GetItem(id)
	if !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "item " + id + " not found"})
		return
	}

	var req publishItemReq
	if !s.decode(w, r, &req) {
		return
	}

	selfID := s.selfHostID()
	client, ok := s.backend.(*virt.Client)

	var updated content.Item
	var pruned []string
	var err error

	switch {
	case req.TemplateUUID != "":
		if !ok || client == nil {
			s.writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "hypervisor client is not available"})
			return
		}
		updated, err = content.PublishTemplate(r.Context(), client, s.content, selfID, content.PublishTemplateReq{
			TemplateUUID: req.TemplateUUID,
			Name:         it.Name,
			Version:      req.Version,
			Description:  req.Description,
			KeepVersions: it.KeepVersions,
			SyncPolicy:   it.SyncPolicy,
		})
	case req.Pool != "" && req.Volume != "":
		if !ok || client == nil {
			s.writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "hypervisor client is not available"})
			return
		}
		updated, err = content.PublishVolume(r.Context(), client, s.content, selfID, content.PublishVolumeReq{
			Pool:         req.Pool,
			Volume:       req.Volume,
			Type:         it.Type,
			Name:         it.Name,
			Version:      req.Version,
			Description:  req.Description,
			KeepVersions: it.KeepVersions,
			SyncPolicy:   it.SyncPolicy,
		})
	default:
		ver := content.ItemVersion{
			ID:          req.ID,
			Version:     req.Version,
			Description: req.Description,
			SHA256:      req.SHA256,
			Size:        req.Size,
			Format:      req.Format,
			TemplateXML: req.TemplateXML,
			SourceHost:  selfID,
		}
		if req.SourceHost != "" {
			ver.SourceHost = req.SourceHost
		}
		updated, pruned, err = s.content.AddVersion(id, ver)
	}

	if err != nil {
		s.logContentActivity(r, "publish-version", id, req.Version, contentErrStatus(err), err)
		s.writeJSON(w, contentErrStatus(err), map[string]string{"error": err.Error()})
		return
	}

	poolPath, _ := s.resolvePoolPath(s.hostTargetPool())
	if poolPath != "" && len(pruned) > 0 {
		for _, pID := range pruned {
			for _, v := range it.Versions {
				if v.ID == pID {
					_ = content.PruneLocalFile(poolPath, it.Name, v.Version, v.Format)
					break
				}
			}
		}
	}

	var latestVer content.ItemVersion
	if len(updated.Versions) > 0 {
		latestVer = updated.Versions[len(updated.Versions)-1]
	}
	results := s.contentToMembers(r, http.MethodPost, "/api/v1/content/items/"+id+"/versions", latestVer)
	s.logContentActivity(r, "publish-version", id, latestVer.Version, http.StatusCreated, nil)

	answer := map[string]any{"item": updated, "version": latestVer}
	if results != nil {
		answer["results"] = results
	}
	s.done(w, http.StatusCreated, answer)
}

// deleteContentVersion deletes a specific version of an item.
func (s *Server) deleteContentVersion(w http.ResponseWriter, r *http.Request) {
	if s.content == nil {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "content library is not available on this host"})
		return
	}
	id := r.PathValue("id")
	vid := r.PathValue("vid")

	it, ok := s.content.GetItem(id)
	if !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "item " + id + " not found"})
		return
	}

	var targetVer *content.ItemVersion
	for _, v := range it.Versions {
		if v.ID == vid || v.Version == vid {
			targetVer = &v
			break
		}
	}
	if targetVer == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "version " + vid + " not found"})
		return
	}

	if err := s.content.DeleteVersion(id, targetVer.ID); err != nil {
		s.logContentActivity(r, "delete-version", id, targetVer.Version, contentErrStatus(err), err)
		s.writeJSON(w, contentErrStatus(err), map[string]string{"error": err.Error()})
		return
	}

	poolPath, _ := s.resolvePoolPath(s.hostTargetPool())
	if poolPath != "" {
		_ = content.PruneLocalFile(poolPath, it.Name, targetVer.Version, targetVer.Format)
	}

	results := s.contentToMembers(r, http.MethodDelete, "/api/v1/content/items/"+id+"/versions/"+targetVer.ID, nil)
	s.logContentActivity(r, "delete-version", id, targetVer.Version, http.StatusNoContent, nil)

	if slices.ContainsFunc(results, func(h HostResult) bool { return !h.OK }) {
		s.done(w, http.StatusOK, map[string]any{"results": results})
		return
	}
	s.done(w, http.StatusNoContent, nil)
}

// downloadContentVersion streams the version artifact using ServeContent supporting HTTP Range.
func (s *Server) downloadContentVersion(w http.ResponseWriter, r *http.Request) {
	if s.content == nil {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "content library is not available on this host"})
		return
	}
	id := r.PathValue("id")
	vid := r.PathValue("vid")

	it, ok := s.content.GetItem(id)
	if !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "item " + id + " not found"})
		return
	}

	var targetVer *content.ItemVersion
	for i := range it.Versions {
		if it.Versions[i].ID == vid || it.Versions[i].Version == vid {
			targetVer = &it.Versions[i]
			break
		}
	}
	if targetVer == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "version " + vid + " not found"})
		return
	}

	filePath, err := s.findLocalArtifactFile(it, *targetVer)
	if err != nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "artifact file not found on host"})
		return
	}

	content.ServeContent(w, r, filePath, targetVer.Size)
}

func copyArtifactFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	tmpDst := dst + ".tmp"
	out, err := os.Create(tmpDst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(tmpDst)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmpDst)
		return err
	}
	return os.Rename(tmpDst, dst)
}

// syncContentItem triggers synchronization of an item version to this host in a background job.
func (s *Server) syncContentItem(w http.ResponseWriter, r *http.Request) {
	if s.content == nil {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "content library is not available on this host"})
		return
	}
	id := r.PathValue("id")
	it, ok := s.content.GetItem(id)
	if !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "item " + id + " not found"})
		return
	}
	if len(it.Versions) == 0 {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "item " + id + " has no versions"})
		return
	}

	var b struct {
		VersionID string `json:"versionId"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&b) // the body is optional

	var targetVer content.ItemVersion
	if b.VersionID != "" {
		found := false
		for _, v := range it.Versions {
			if v.ID == b.VersionID || v.Version == b.VersionID {
				targetVer = v
				found = true
				break
			}
		}
		if !found {
			s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "version " + b.VersionID + " not found"})
			return
		}
	} else if lv := it.LatestVersion(); lv != nil {
		targetVer = *lv
	}
	if targetVer.ID == "" {
		// Legacy data without a stored version ID: address the peer by version string,
		// which its download handler also matches.
		targetVer.ID = targetVer.Version
	}

	targetPool := s.hostTargetPool()
	selfID := s.selfHostID()
	caller := identity(r) // the peer is vouched for this user, like every member call
	jobTitle := fmt.Sprintf("Sync %s (v%s)", it.Name, targetVer.Version)

	job := s.jobs.Start("content-sync", jobTitle, func(jobCtx context.Context, report func(string)) (string, error) {
		poolPath, err := s.resolvePoolPath(targetPool)
		if err != nil {
			return "", err
		}
		if targetVer.SourceHost == "" || targetVer.SourceHost == selfID {
			finalFile := fmt.Sprintf("content-%s-v%s.%s", it.Name, targetVer.Version, targetVer.Format)
			finalPath := filepath.Join(poolPath, finalFile)
			if artifactFileMatches(finalPath, targetVer.Size, targetVer.SHA256) {
				if s.content != nil {
					_, _ = s.content.SetHostSyncState(it.ID, content.HostSyncState{
						Host:    selfID,
						State:   "synced",
						Updated: time.Now().UTC(),
					})
				}
				report("Already present on source host")
				return finalPath, nil
			}
			srcPath, err := s.findLocalArtifactFile(it, targetVer)
			if err != nil {
				if s.content != nil {
					_, _ = s.content.SetHostSyncState(it.ID, content.HostSyncState{
						Host:    selfID,
						State:   "failed",
						Error:   fmt.Sprintf("artifact for %s (v%s) not found on source host: %v", it.Name, targetVer.Version, err),
						Updated: time.Now().UTC(),
					})
				}
				return "", fmt.Errorf("artifact for %s (v%s) not found on source host: %w", it.Name, targetVer.Version, err)
			}
			if srcPath == finalPath {
				if s.content != nil {
					_, _ = s.content.SetHostSyncState(it.ID, content.HostSyncState{
						Host:    selfID,
						State:   "synced",
						Updated: time.Now().UTC(),
					})
				}
				report("Already present on source host")
				return finalPath, nil
			}
			report(fmt.Sprintf("Copying artifact to target pool %s", targetPool))
			if err := copyArtifactFile(srcPath, finalPath); err != nil {
				if s.content != nil {
					_, _ = s.content.SetHostSyncState(it.ID, content.HostSyncState{
						Host:    selfID,
						State:   "failed",
						Error:   fmt.Sprintf("copy artifact to target pool: %v", err),
						Updated: time.Now().UTC(),
					})
				}
				return "", fmt.Errorf("copy artifact to target pool: %w", err)
			}
			if s.backend != nil {
				_ = s.backend.PoolAction(targetPool, "refresh")
			}
			if s.content != nil {
				_, _ = s.content.SetHostSyncState(it.ID, content.HostSyncState{
					Host:    selfID,
					State:   "synced",
					Updated: time.Now().UTC(),
				})
			}
			report("Synced to target pool")
			return finalPath, nil
		}
		if s.content != nil {
			_, _ = s.content.SetHostSyncState(it.ID, content.HostSyncState{
				Host:    selfID,
				State:   "syncing",
				Updated: time.Now().UTC(),
			})
		}
		peer, ok := s.PeerFor(targetVer.SourceHost)
		if !ok || peer == nil {
			if s.content != nil {
				_, _ = s.content.SetHostSyncState(it.ID, content.HostSyncState{
					Host:    selfID,
					State:   "failed",
					Error:   fmt.Sprintf("source host %q not found in peers", targetVer.SourceHost),
					Updated: time.Now().UTC(),
				})
			}
			return "", fmt.Errorf("source host %q not found in peers", targetVer.SourceHost)
		}
		// Downloads go through the authenticated peer connection, on behalf of the
		// user who asked for the sync (ADR-0020 member calls, ADR-0063 content library).
		res, err := content.Pull(jobCtx, peer.As(caller.Name, caller.Role.String(), caller.Token), it, targetVer, poolPath, report)
		if err != nil {
			if s.content != nil {
				_, _ = s.content.SetHostSyncState(it.ID, content.HostSyncState{
					Host:    selfID,
					State:   "failed",
					Error:   err.Error(),
					Updated: time.Now().UTC(),
				})
			}
			return "", err
		}
		if s.content != nil {
			_, _ = s.content.SetHostSyncState(it.ID, content.HostSyncState{
				Host:    selfID,
				State:   "synced",
				Updated: time.Now().UTC(),
			})
		}
		if s.backend != nil {
			_ = s.backend.PoolAction(targetPool, "refresh")
		}
		return res, nil
	})

	s.writeJSON(w, http.StatusAccepted, job)
}

// getContentConfig returns host storage and auto-sync preferences.
func (s *Server) getContentConfig(w http.ResponseWriter, _ *http.Request) {
	if s.content == nil {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "content library is not available on this host"})
		return
	}
	selfID := s.selfHostID()
	cfg, ok := s.content.GetHostConfig(selfID)
	if !ok {
		cfg = content.HostConfig{
			HostID:     selfID,
			TargetPool: "default",
			AutoSync:   true,
		}
	}
	s.writeJSON(w, http.StatusOK, cfg)
}

// updateContentConfig updates host storage and auto-sync preferences.
func (s *Server) updateContentConfig(w http.ResponseWriter, r *http.Request) {
	if s.content == nil {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "content library is not available on this host"})
		return
	}
	var cfg content.HostConfig
	if !s.decode(w, r, &cfg) {
		return
	}
	selfID := s.selfHostID()
	if cfg.HostID != "" && cfg.HostID != selfID {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cannot update configuration for a different host"})
		return
	}
	cfg.HostID = selfID
	if err := s.content.SetHostConfig(cfg); err != nil {
		s.writeJSON(w, contentErrStatus(err), map[string]string{"error": err.Error()})
		return
	}
	s.done(w, http.StatusOK, cfg)
}

// deployContentItem runs zero-touch deployment of a new VM from a library item.
func (s *Server) deployContentItem(w http.ResponseWriter, r *http.Request) {
	if s.content == nil {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "content library is not available on this host"})
		return
	}
	id := r.PathValue("id")
	it, ok := s.content.GetItem(id)
	if !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "item " + id + " not found"})
		return
	}

	var req content.DeployRequest
	if !s.decode(w, r, &req) {
		return
	}
	req.ItemID = id

	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "machine name is required"})
		return
	}
	if err := virt.CheckName("Machine", req.Name); err != nil {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}

	client, ok := s.backend.(*virt.Client)
	selfID := s.selfHostID()
	jobTitle := fmt.Sprintf("Deploy %s from %s", req.Name, it.Name)

	job := s.jobs.Start("content-deploy", jobTitle, func(jobCtx context.Context, report func(string)) (string, error) {
		if (!ok || client == nil) && !content.HasDeployHooks(jobCtx) {
			return "", errors.New("hypervisor client is not available")
		}
		uuid, err := content.Deploy(jobCtx, client, s.content, s, req, selfID, report)
		if err != nil {
			return "", err
		}
		if s.live != nil {
			s.live.Refresh()
		}
		return uuid, nil
	})

	s.logContentActivity(r, "deploy-machine", id, req.Name, http.StatusAccepted, nil)
	s.writeJSON(w, http.StatusAccepted, job)
}
