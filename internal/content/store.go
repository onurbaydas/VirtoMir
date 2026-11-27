package content

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// Classed errors for API and operational mapping (404 Not Found, 422 Unprocessable Entity, 409 Conflict).
var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid")
	ErrConflict = errors.New("conflict")
)

type classed struct {
	class  error
	second error
	msg    string
}

func (e classed) Error() string { return e.msg }

func (e classed) Is(target error) bool {
	return target == e.class || (e.second != nil && target == e.second)
}

func (e classed) Unwrap() error { return e.class }

func notFoundf(format string, a ...any) error {
	return classed{class: ErrNotFound, msg: fmt.Sprintf(format, a...)}
}

func invalidf(format string, a ...any) error {
	return classed{class: ErrInvalid, msg: fmt.Sprintf(format, a...)}
}

func conflictf(format string, a ...any) error {
	return classed{class: ErrConflict, second: ErrInvalid, msg: fmt.Sprintf(format, a...)}
}

var (
	sha256RE = regexp.MustCompile(`^[a-f0-9]{64}$`)
	idRE     = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)
)

func randomHex(bytes int) string {
	b := make([]byte, bytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// RandomHex returns a cryptographically random hexadecimal string of length 2*bytes.
func RandomHex(bytes int) string {
	return randomHex(bytes)
}

type storeData struct {
	Items       []Item       `json:"items"`
	HostConfigs []HostConfig `json:"hostConfigs,omitempty"`
}

// Store manages the persistence and lifecycle of content library items and per-host configs.
type Store struct {
	path        string
	mu          sync.RWMutex
	items       map[string]*Item
	hostConfigs map[string]HostConfig
}

// Open loads or initializes the content library store from the given JSON file path.
// If the path is empty, an in-memory store is returned. Missing file returns an empty store without error.
func Open(path string) (*Store, error) {
	s := &Store{
		path:        path,
		items:       make(map[string]*Item),
		hostConfigs: make(map[string]HostConfig),
	}
	if path == "" {
		return s, nil
	}

	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, err
	}

	var data storeData
	if err := json.Unmarshal(raw, &data); err != nil {
		// Fallback in case the file contains a raw array of items
		var list []Item
		if errList := json.Unmarshal(raw, &list); errList == nil {
			data.Items = list
		} else {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}

	for i := range data.Items {
		it := data.Items[i]
		normalizeItem(&it)
		if it.ID == "" {
			return nil, fmt.Errorf("%s: item missing ID", path)
		}
		if _, dup := s.items[it.ID]; dup {
			return nil, fmt.Errorf("%s: duplicate item ID %q", path, it.ID)
		}
		s.items[it.ID] = &it
	}

	for _, hc := range data.HostConfigs {
		hID := strings.TrimSpace(hc.HostID)
		if hID != "" {
			hc.HostID = hID
			s.hostConfigs[hID] = hc
		}
	}

	return s, nil
}

func normalizeItem(it *Item) {
	it.ID = strings.TrimSpace(it.ID)
	it.Name = strings.TrimSpace(it.Name)
	if it.KeepVersions <= 0 {
		it.KeepVersions = 3
	}
	if it.SyncPolicy.Mode == "" {
		it.SyncPolicy.Mode = SyncManual
	}
	if it.SyncPolicy.Hosts == nil {
		it.SyncPolicy.Hosts = []string{}
	}
	if it.Versions == nil {
		it.Versions = []ItemVersion{}
	}
	for i := range it.Versions {
		v := &it.Versions[i]
		v.ID = strings.TrimSpace(v.ID)
		if v.ID == "" {
			v.ID = randomHex(8)
		}
		if v.Created.IsZero() {
			v.Created = it.Created
		}
	}
	if it.Hosts == nil {
		it.Hosts = []HostSyncState{}
	}
}

func (s *Store) saveLocked() error {
	if s.path == "" {
		return nil
	}

	items := make([]Item, 0, len(s.items))
	for _, it := range s.items {
		items = append(items, it.clone())
	}
	sort.Slice(items, func(i, j int) bool {
		if !strings.EqualFold(items[i].Name, items[j].Name) {
			return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
		}
		return items[i].ID < items[j].ID
	})

	hostConfigs := make([]HostConfig, 0, len(s.hostConfigs))
	for _, hc := range s.hostConfigs {
		hostConfigs = append(hostConfigs, hc)
	}
	sort.Slice(hostConfigs, func(i, j int) bool {
		return hostConfigs[i].HostID < hostConfigs[j].HostID
	})

	data := storeData{
		Items:       items,
		HostConfigs: hostConfigs,
	}

	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(s.path), 0o750); err != nil {
		return err
	}

	tmp := s.path + ".tmp"
	if err := func() error {
		f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		if _, err := f.Write(b); err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
			return err
		}
		if err := f.Sync(); err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
			return err
		}
		return f.Close()
	}(); err != nil {
		_ = os.Remove(tmp)
		return err
	}

	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// ListItems returns all catalog items sorted by name.
func (s *Store) ListItems() []Item {
	s.mu.RLock()
	defer s.mu.RUnlock()

	list := make([]Item, 0, len(s.items))
	for _, it := range s.items {
		list = append(list, it.clone())
	}
	sort.Slice(list, func(i, j int) bool {
		if !strings.EqualFold(list[i].Name, list[j].Name) {
			return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name)
		}
		return list[i].Created.Before(list[j].Created)
	})
	return list
}

// GetItem retrieves an item by its ID.
func (s *Store) GetItem(id string) (Item, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	it, ok := s.items[strings.TrimSpace(id)]
	if !ok {
		return Item{}, false
	}
	return it.clone(), true
}

// CreateItem validates and registers a new content library item.
// It generates a 16-hex ID if empty, ensures KeepVersions >= 1 (defaults to 3),
// and rejects duplicate names or invalid item types.
func (s *Store) CreateItem(item Item) (Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	item.Name = strings.TrimSpace(item.Name)
	if item.Name == "" {
		return Item{}, invalidf("item name is required")
	}

	switch item.Type {
	case TypeTemplate, TypeISO, TypeImage:
	default:
		return Item{}, invalidf("invalid item type %q (must be template, iso, or image)", item.Type)
	}
	if err := checkFiles(item.Name, item.Versions); err != nil {
		return Item{}, err
	}

	for _, existing := range s.items {
		if strings.EqualFold(existing.Name, item.Name) {
			return Item{}, conflictf("an item named %q already exists", item.Name)
		}
	}

	item.ID = strings.TrimSpace(item.ID)
	if item.ID == "" {
		for {
			item.ID = randomHex(8)
			if _, exists := s.items[item.ID]; !exists {
				break
			}
		}
	} else {
		if !idRE.MatchString(item.ID) {
			return Item{}, invalidf("invalid item ID %q", item.ID)
		}
		if _, exists := s.items[item.ID]; exists {
			return Item{}, conflictf("an item with ID %q already exists", item.ID)
		}
	}

	if item.KeepVersions <= 0 {
		item.KeepVersions = 3
	}

	if item.SyncPolicy.Mode == "" {
		item.SyncPolicy.Mode = SyncManual
	}
	switch item.SyncPolicy.Mode {
	case SyncAll, SyncSelected, SyncManual:
	default:
		return Item{}, invalidf("invalid sync mode %q", item.SyncPolicy.Mode)
	}
	if item.SyncPolicy.Hosts == nil {
		item.SyncPolicy.Hosts = []string{}
	}

	now := time.Now().UTC()
	if item.Created.IsZero() {
		item.Created = now
	}
	if item.Updated.IsZero() {
		item.Updated = item.Created
	}
	if item.Versions == nil {
		item.Versions = []ItemVersion{}
	}
	for i := range item.Versions {
		v := &item.Versions[i]
		v.ID = strings.TrimSpace(v.ID)
		if v.ID == "" {
			v.ID = randomHex(8)
		}
		if v.Created.IsZero() {
			v.Created = item.Created
		}
	}
	if item.Hosts == nil {
		item.Hosts = []HostSyncState{}
	}

	s.items[item.ID] = &item
	if err := s.saveLocked(); err != nil {
		delete(s.items, item.ID)
		return Item{}, err
	}

	return item.clone(), nil
}

// UpdateItem updates name, description, keepVersions, syncPolicy, versions, and hosts of an existing item.
func (s *Store) UpdateItem(item Item) (Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cur, ok := s.items[strings.TrimSpace(item.ID)]
	if !ok {
		return Item{}, notFoundf("item %q not found", item.ID)
	}

	name := strings.TrimSpace(item.Name)
	if name == "" {
		return Item{}, invalidf("item name is required")
	}

	for _, existing := range s.items {
		if existing.ID != cur.ID && strings.EqualFold(existing.Name, name) {
			return Item{}, conflictf("an item named %q already exists", name)
		}
	}

	keep := item.KeepVersions
	if keep <= 0 {
		keep = 3
	}

	syncPolicy := item.SyncPolicy
	if syncPolicy.Mode == "" {
		syncPolicy.Mode = SyncManual
	}
	switch syncPolicy.Mode {
	case SyncAll, SyncSelected, SyncManual:
	default:
		return Item{}, invalidf("invalid sync mode %q", syncPolicy.Mode)
	}
	if syncPolicy.Hosts == nil {
		syncPolicy.Hosts = []string{}
	}

	if err := checkFiles(name, append(slices.Clone(cur.Versions), item.Versions...)); err != nil {
		return Item{}, err
	}

	prev := cur.clone()
	cur.Name = name
	cur.Description = item.Description
	cur.KeepVersions = keep
	cur.SyncPolicy = syncPolicy
	if item.SourceHost != "" {
		cur.SourceHost = item.SourceHost
	}

	if len(item.Versions) > 0 {
		for _, nv := range item.Versions {
			nv.ID = strings.TrimSpace(nv.ID)
			if nv.ID == "" {
				nv.ID = randomHex(8)
			}
			if nv.Created.IsZero() {
				nv.Created = time.Now().UTC()
			}
			found := false
			for idx, cv := range cur.Versions {
				if (cv.ID != "" && cv.ID == nv.ID) || (cv.Version != "" && cv.Version == nv.Version) {
					cur.Versions[idx] = nv
					found = true
					break
				}
			}
			if !found {
				cur.Versions = append(cur.Versions, nv)
			}
		}
		if len(cur.Versions) > keep {
			excess := len(cur.Versions) - keep
			cur.Versions = append([]ItemVersion{}, cur.Versions[excess:]...)
		}
	}

	if len(item.Hosts) > 0 {
		for _, nh := range item.Hosts {
			nh.Host = strings.TrimSpace(nh.Host)
			if nh.Host == "" {
				continue
			}
			found := false
			for idx, ch := range cur.Hosts {
				if ch.Host == nh.Host {
					cur.Hosts[idx] = nh
					found = true
					break
				}
			}
			if !found {
				cur.Hosts = append(cur.Hosts, nh)
			}
		}
	}

	cur.Updated = time.Now().UTC()

	if err := s.saveLocked(); err != nil {
		*cur = prev
		return Item{}, err
	}
	return cur.clone(), nil
}

// SetHostSyncState updates or appends the sync state for a host on an item.
func (s *Store) SetHostSyncState(itemID string, state HostSyncState) (Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cur, ok := s.items[strings.TrimSpace(itemID)]
	if !ok {
		return Item{}, notFoundf("item %q not found", itemID)
	}

	state.Host = strings.TrimSpace(state.Host)
	if state.Host == "" {
		return Item{}, invalidf("host is required")
	}
	if state.Updated.IsZero() {
		state.Updated = time.Now().UTC()
	}

	prev := cur.clone()
	found := false
	for i, h := range cur.Hosts {
		if h.Host == state.Host {
			cur.Hosts[i] = state
			found = true
			break
		}
	}
	if !found {
		cur.Hosts = append(cur.Hosts, state)
	}

	cur.Updated = time.Now().UTC()
	if err := s.saveLocked(); err != nil {
		*cur = prev
		return Item{}, err
	}
	return cur.clone(), nil
}

// DeleteItem removes an item from the store. Returns ErrNotFound if missing.
func (s *Store) DeleteItem(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	id = strings.TrimSpace(id)
	cur, ok := s.items[id]
	if !ok {
		return notFoundf("item %q not found", id)
	}

	delete(s.items, id)
	if err := s.saveLocked(); err != nil {
		s.items[id] = cur
		return err
	}
	return nil
}

// AddVersion appends a new version to an item and automatically prunes older versions beyond KeepVersions.
// It returns the updated Item, the list of pruned version IDs, and any error encountered.
func (s *Store) AddVersion(itemID string, v ItemVersion) (Item, []string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cur, ok := s.items[strings.TrimSpace(itemID)]
	if !ok {
		return Item{}, nil, notFoundf("item %q not found", itemID)
	}

	v.Version = strings.TrimSpace(v.Version)
	if v.Version == "" {
		return Item{}, nil, invalidf("version is required")
	}
	if err := checkFiles(cur.Name, []ItemVersion{v}); err != nil {
		return Item{}, nil, err
	}

	v.SHA256 = strings.TrimSpace(v.SHA256)
	if !sha256RE.MatchString(v.SHA256) {
		return Item{}, nil, invalidf("invalid SHA-256 checksum %q (must be 64 hex characters)", v.SHA256)
	}

	if v.Size <= 0 {
		return Item{}, nil, invalidf("size must be greater than 0")
	}

	v.ID = strings.TrimSpace(v.ID)
	if v.ID == "" {
		v.ID = randomHex(8)
	}

	if v.Created.IsZero() {
		v.Created = time.Now().UTC()
	}

	prev := cur.clone()
	cur.Versions = append(cur.Versions, v)

	keep := cur.KeepVersions
	if keep <= 0 {
		keep = 3
	}

	var pruned []string
	if len(cur.Versions) > keep {
		excess := len(cur.Versions) - keep
		pruned = make([]string, excess)
		for i := 0; i < excess; i++ {
			pruned[i] = cur.Versions[i].ID
		}
		cur.Versions = append([]ItemVersion{}, cur.Versions[excess:]...)
	}

	if v.SourceHost != "" {
		now := time.Now().UTC()
		foundHost := false
		for i, h := range cur.Hosts {
			if h.Host == v.SourceHost {
				cur.Hosts[i] = HostSyncState{Host: v.SourceHost, State: "synced", Updated: now}
				foundHost = true
				break
			}
		}
		if !foundHost {
			cur.Hosts = append(cur.Hosts, HostSyncState{Host: v.SourceHost, State: "synced", Updated: now})
		}
	}

	cur.Updated = time.Now().UTC()

	if err := s.saveLocked(); err != nil {
		*cur = prev
		return Item{}, nil, err
	}

	return cur.clone(), pruned, nil
}

// DeleteVersion deletes a specific version from an item.
// Returns ErrNotFound if the item or version is missing, and refuses deletion if it is the only version.
func (s *Store) DeleteVersion(itemID, versionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cur, ok := s.items[strings.TrimSpace(itemID)]
	if !ok {
		return notFoundf("item %q not found", itemID)
	}

	versionID = strings.TrimSpace(versionID)
	idx := -1
	for i, v := range cur.Versions {
		if v.ID == versionID {
			idx = i
			break
		}
	}
	if idx == -1 {
		return notFoundf("version %q not found in item %q", versionID, itemID)
	}

	if len(cur.Versions) <= 1 {
		return conflictf("cannot delete the only version of item %q; delete the item instead", itemID)
	}

	prev := cur.clone()
	cur.Versions = append(cur.Versions[:idx], cur.Versions[idx+1:]...)
	cur.Updated = time.Now().UTC()

	if err := s.saveLocked(); err != nil {
		*cur = prev
		return err
	}
	return nil
}

// SetHostConfig saves the storage and auto-sync preferences for a host.
func (s *Store) SetHostConfig(cfg HostConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg.HostID = strings.TrimSpace(cfg.HostID)
	if cfg.HostID == "" {
		return invalidf("hostId is required")
	}
	cfg.TargetPool = strings.TrimSpace(cfg.TargetPool)

	prev, had := s.hostConfigs[cfg.HostID]
	s.hostConfigs[cfg.HostID] = cfg

	if err := s.saveLocked(); err != nil {
		if had {
			s.hostConfigs[cfg.HostID] = prev
		} else {
			delete(s.hostConfigs, cfg.HostID)
		}
		return err
	}
	return nil
}

// GetHostConfig retrieves storage and auto-sync preferences for a host.
func (s *Store) GetHostConfig(hostID string) (HostConfig, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cfg, ok := s.hostConfigs[strings.TrimSpace(hostID)]
	return cfg, ok
}

// ListHostConfigs returns all saved host configurations sorted by host ID.
func (s *Store) ListHostConfigs() []HostConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()

	list := make([]HostConfig, 0, len(s.hostConfigs))
	for _, cfg := range s.hostConfigs {
		list = append(list, cfg)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].HostID < list[j].HostID
	})
	return list
}
