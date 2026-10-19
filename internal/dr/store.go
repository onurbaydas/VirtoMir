package dr

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// ErrNotFound marks an unknown ID, ErrInvalid a group or plan that cannot be
// saved, ErrConflict an ID that exists already and ErrBusy a delete that would
// break a reference (a plan over a protection group).
var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid")
	ErrConflict = errors.New("conflict")
	ErrBusy     = errors.New("busy")
)

// maxRuns is how many execution runs the store keeps per the run history.
const maxRuns = 50

// Store keeps protection groups, recovery plans and execution-run history in
// one JSON file (<state-dir>/dr.json).
type Store struct {
	path   string
	mu     sync.RWMutex
	groups map[string]*ProtectionGroup
	plans  map[string]*RecoveryPlan
	runs   []ExecutionRun
}

// state is the on-disk shape of dr.json.
type state struct {
	Groups []ProtectionGroup `json:"groups"`
	Plans  []RecoveryPlan    `json:"plans"`
	Runs   []ExecutionRun    `json:"runs,omitempty"`
}

// Open loads path (a missing file is fine).
func Open(path string) (*Store, error) {
	s := &Store{path: path, groups: map[string]*ProtectionGroup{}, plans: map[string]*RecoveryPlan{}}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, err
	}
	var st state
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for i, g := range st.Groups {
		if g.ID == "" {
			return nil, fmt.Errorf("%s: protection group %d has no ID", path, i)
		}
		if _, dup := s.groups[g.ID]; dup {
			return nil, fmt.Errorf("%s: protection group %q appears twice", path, g.ID)
		}
		if g.VMUUIDs == nil {
			g.VMUUIDs = []string{}
		}
		s.groups[g.ID] = &st.Groups[i]
	}
	for i, p := range st.Plans {
		if p.ID == "" {
			return nil, fmt.Errorf("%s: recovery plan %d has no ID", path, i)
		}
		if _, dup := s.plans[p.ID]; dup {
			return nil, fmt.Errorf("%s: recovery plan %q appears twice", path, p.ID)
		}
		if p.BootGroups == nil {
			p.BootGroups = []BootGroup{}
		}
		for j := range p.BootGroups {
			if p.BootGroups[j].VMs == nil {
				p.BootGroups[j].VMs = []PlanVM{}
			}
		}
		s.plans[p.ID] = &st.Plans[i]
	}
	s.runs = st.Runs
	return s, nil
}

// saveLocked writes the whole store atomically: indented JSON to a .tmp file,
// fsync, close, rename (the pattern of internal/project).
func (s *Store) saveLocked() error {
	if s.path == "" {
		return nil
	}
	st := state{Groups: make([]ProtectionGroup, 0, len(s.groups)), Plans: make([]RecoveryPlan, 0, len(s.plans)), Runs: s.runs}
	for _, g := range s.groups {
		st.Groups = append(st.Groups, *g)
	}
	for _, p := range s.plans {
		st.Plans = append(st.Plans, *p)
	}
	b, err := json.MarshalIndent(st, "", "  ")
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
			f.Close()
			os.Remove(tmp)
			return err
		}
		if err := f.Sync(); err != nil {
			f.Close()
			os.Remove(tmp)
			return err
		}
		return f.Close()
	}(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// ListProtectionGroups returns every protection group, by ID.
func (s *Store) ListProtectionGroups() []ProtectionGroup {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ProtectionGroup, 0, len(s.groups))
	for _, id := range s.groupIDsLocked() {
		out = append(out, s.groups[id].clone())
	}
	return out
}

func (s *Store) groupIDsLocked() []string {
	ids := make([]string, 0, len(s.groups))
	for id := range s.groups {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (g ProtectionGroup) clone() ProtectionGroup {
	q := g
	q.VMUUIDs = append([]string{}, g.VMUUIDs...)
	return q
}

// GetProtectionGroup returns one protection group.
func (s *Store) GetProtectionGroup(id string) (ProtectionGroup, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g, ok := s.groups[id]
	if !ok {
		return ProtectionGroup{}, false
	}
	return g.clone(), true
}

// validateGroup checks a group as Create or Update receives it; the defaults
// (RPO 60, interval 15) are applied before it.
func validateGroup(g ProtectionGroup) error {
	switch {
	case g.Name == "":
		return fmt.Errorf("%w: the protection group needs a name", ErrInvalid)
	case g.TargetHost == "":
		return fmt.Errorf("%w: the protection group needs a target host", ErrInvalid)
	case g.TargetPool == "":
		return fmt.Errorf("%w: the protection group needs a target pool", ErrInvalid)
	case len(g.VMUUIDs) == 0:
		return fmt.Errorf("%w: the protection group needs at least one machine", ErrInvalid)
	case g.RPOMinutes < 1:
		return fmt.Errorf("%w: the RPO target is at least 1 minute", ErrInvalid)
	case g.ReplicationInterval < 1:
		return fmt.Errorf("%w: the replication interval is at least 1 minute", ErrInvalid)
	}
	return nil
}

// CreateProtectionGroup adds a group; an empty ID gets a generated one.
func (s *Store) CreateProtectionGroup(g ProtectionGroup) (ProtectionGroup, error) {
	if g.RPOMinutes == 0 {
		g.RPOMinutes = 60
	}
	if g.ReplicationInterval == 0 {
		g.ReplicationInterval = 15
	}
	if err := validateGroup(g); err != nil {
		return ProtectionGroup{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if g.ID == "" {
		g.ID = newID()
	}
	if _, ok := s.groups[g.ID]; ok {
		return ProtectionGroup{}, fmt.Errorf("%w: protection group %q exists already", ErrConflict, g.ID)
	}
	if g.VMUUIDs == nil {
		g.VMUUIDs = []string{}
	}
	now := time.Now().UTC()
	g.Created, g.Updated, g.Status = now, now, StatusProtected
	if g.ActiveHost == "" {
		g.ActiveHost = g.SourceHost
	}
	s.groups[g.ID] = &g
	if err := s.saveLocked(); err != nil {
		delete(s.groups, g.ID)
		return ProtectionGroup{}, err
	}
	return s.groups[g.ID].clone(), nil
}

// UpdateProtectionGroup changes a group's fields (not its created time or status).
func (s *Store) UpdateProtectionGroup(g ProtectionGroup) error {
	if g.RPOMinutes == 0 {
		g.RPOMinutes = 60
	}
	if g.ReplicationInterval == 0 {
		g.ReplicationInterval = 15
	}
	if err := validateGroup(g); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.groups[g.ID]
	if !ok {
		return fmt.Errorf("%w: protection group %q", ErrNotFound, g.ID)
	}
	prev := cur.clone()
	next := g.clone()
	if next.ActiveHost == "" {
		next.ActiveHost = cur.ActiveHost
	}
	next.Created, next.Updated, next.Status = cur.Created, time.Now().UTC(), cur.Status
	*cur = next
	if err := s.saveLocked(); err != nil {
		*cur = prev
		return err
	}
	return nil
}

// setStatus changes only a protection group's status (the RPO engine's job),
// leaving every other field alone.
func (s *Store) setStatus(id, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.groups[id]
	if !ok {
		return fmt.Errorf("%w: protection group %q", ErrNotFound, id)
	}
	if g.Status == status {
		return nil
	}
	prev := g.clone()
	g.Status, g.Updated = status, time.Now().UTC()
	if err := s.saveLocked(); err != nil {
		*g = prev
		return err
	}
	return nil
}

// SetGroupActiveHost updates a protection group's active host and status.
func (s *Store) SetGroupActiveHost(id, activeHost, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.groups[id]
	if !ok {
		return fmt.Errorf("%w: protection group %q", ErrNotFound, id)
	}
	prev := g.clone()
	if activeHost != "" {
		g.ActiveHost = activeHost
	}
	if status != "" {
		g.Status = status
	}
	g.Updated = time.Now().UTC()
	if err := s.saveLocked(); err != nil {
		*g = prev
		return err
	}
	return nil
}

// DeleteProtectionGroup removes a group; plans over it have to go first.
func (s *Store) DeleteProtectionGroup(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.groups[id]; !ok {
		return fmt.Errorf("%w: protection group %q", ErrNotFound, id)
	}
	for id2 := range s.plans {
		if s.plans[id2].ProtectionGroupID == id {
			return fmt.Errorf("%w: recovery plan %q uses this protection group; delete the plan first", ErrBusy, s.plans[id2].Name)
		}
	}
	prev := s.groups[id].clone()
	delete(s.groups, id)
	if err := s.saveLocked(); err != nil {
		s.groups[id] = &prev
		return err
	}
	return nil
}

// ListPlans returns recovery plans (filtered by protection group when given), by name.
func (s *Store) ListPlans(protectionGroupID string) []RecoveryPlan {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []RecoveryPlan{}
	for _, p := range s.plans {
		if protectionGroupID != "" && p.ProtectionGroupID != protectionGroupID {
			continue
		}
		out = append(out, p.clone())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (p RecoveryPlan) clone() RecoveryPlan {
	q := p
	q.BootGroups = make([]BootGroup, len(p.BootGroups))
	for i, bg := range p.BootGroups {
		bgc := bg
		bgc.VMs = append([]PlanVM{}, bg.VMs...)
		for j := range bgc.VMs {
			bgc.VMs[j].NetworkMappings = append([]NetworkMap{}, bg.VMs[j].NetworkMappings...)
			if bg.VMs[j].IPReaddressing != nil {
				ip := *bg.VMs[j].IPReaddressing
				ip.Nameservers = append([]string{}, bg.VMs[j].IPReaddressing.Nameservers...)
				bgc.VMs[j].IPReaddressing = &ip
			}
		}
		q.BootGroups[i] = bgc
	}
	return q
}

// GetPlan returns one recovery plan.
func (s *Store) GetPlan(id string) (RecoveryPlan, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.plans[id]
	if !ok {
		return RecoveryPlan{}, false
	}
	return p.clone(), true
}

// validatePlan checks a plan as Create or Update receives it.
func (s *Store) validatePlan(p RecoveryPlan) error {
	switch {
	case p.Name == "":
		return fmt.Errorf("%w: the recovery plan needs a name", ErrInvalid)
	case len(p.BootGroups) == 0:
		return fmt.Errorf("%w: the recovery plan needs at least one boot group", ErrInvalid)
	}
	if _, ok := s.groups[p.ProtectionGroupID]; !ok {
		return fmt.Errorf("%w: recovery plan %q points at protection group %q, which does not exist", ErrInvalid, p.Name, p.ProtectionGroupID)
	}
	orders := map[int]bool{}
	for _, bg := range p.BootGroups {
		if len(bg.VMs) == 0 {
			return fmt.Errorf("%w: boot group %q has no machines", ErrInvalid, bg.Name)
		}
		if orders[bg.Order] {
			return fmt.Errorf("%w: two boot groups start at order %d; boot group orders must be unique", ErrInvalid, bg.Order)
		}
		orders[bg.Order] = true
	}
	return nil
}

// CreatePlan adds a recovery plan; an empty ID gets a generated one.
func (s *Store) CreatePlan(p RecoveryPlan) (RecoveryPlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.ID == "" {
		p.ID = newID()
	}
	if _, ok := s.plans[p.ID]; ok {
		return RecoveryPlan{}, fmt.Errorf("%w: recovery plan %q exists already", ErrConflict, p.ID)
	}
	if err := s.validatePlan(p); err != nil {
		return RecoveryPlan{}, err
	}
	if p.BootGroups == nil {
		p.BootGroups = []BootGroup{}
	}
	now := time.Now().UTC()
	p.Created, p.Updated = now, now
	s.plans[p.ID] = &p
	if err := s.saveLocked(); err != nil {
		delete(s.plans, p.ID)
		return RecoveryPlan{}, err
	}
	return s.plans[p.ID].clone(), nil
}

// UpdatePlan changes a recovery plan's fields (not its created time).
func (s *Store) UpdatePlan(p RecoveryPlan) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.plans[p.ID]
	if !ok {
		return fmt.Errorf("%w: recovery plan %q", ErrNotFound, p.ID)
	}
	if err := s.validatePlan(p); err != nil {
		return err
	}
	prev := cur.clone()
	next := p.clone()
	next.Created, next.Updated = cur.Created, time.Now().UTC()
	*cur = next
	if err := s.saveLocked(); err != nil {
		*cur = prev
		return err
	}
	return nil
}

// DeletePlan removes a recovery plan.
func (s *Store) DeletePlan(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.plans[id]
	if !ok {
		return fmt.Errorf("%w: recovery plan %q", ErrNotFound, id)
	}
	delete(s.plans, id)
	if err := s.saveLocked(); err != nil {
		s.plans[id] = p
		return err
	}
	return nil
}

// RecordRun appends an execution run to the history; only the newest maxRuns stay.
func (s *Store) RecordRun(run ExecutionRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.runs
	next := append(append([]ExecutionRun{}, s.runs...), run)
	if n := len(next); n > maxRuns {
		next = append([]ExecutionRun{}, next[n-maxRuns:]...)
	}
	s.runs = next
	if err := s.saveLocked(); err != nil {
		s.runs = prev
		return err
	}
	return nil
}

// GetRun returns one execution run from the history (newest first).
func (s *Store) GetRun(id string) (ExecutionRun, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := len(s.runs) - 1; i >= 0; i-- {
		if s.runs[i].ID == id {
			return s.runs[i], true
		}
	}
	return ExecutionRun{}, false
}

// ListRuns returns execution runs, newest first (optionally filtered by plan).
func (s *Store) ListRuns(planID string) []ExecutionRun {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ExecutionRun, 0, len(s.runs))
	for i := len(s.runs) - 1; i >= 0; i-- {
		r := s.runs[i]
		if planID != "" && r.PlanID != planID {
			continue
		}
		out = append(out, r)
	}
	return out
}

// newID is a random 16-hex-character identifier.
func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
