// Package zfsrep replicates ZFS datasets and zvols to another host (ADR-0056): on a schedule it takes
// a snapshot and sends what changed since the last one both sides have
// (zfs send -i @base | ssh target zfs receive -F), then prunes old replication snapshots on
// both sides. Commands run without a shell; names are validated because ssh hands the remote
// part to the target's shell.
package zfsrep

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/onurbaydas/virtomir/internal/atomicfile"
)

// SnapPrefix marks the snapshots replication makes; pruning only ever touches these.
const SnapPrefix = "virtomir-rep-"

// DefaultKeep is how many replication snapshots stay on each side when Keep is 0.
const DefaultKeep = 3

// Spec is one replication: SourcePool/SourceVolume on this host to TargetPool/SourceVolume
// on TargetHost ([user@]host, reached with ssh as root's key allows).
type Spec struct {
	ID              string `json:"id"`
	SourcePool      string `json:"sourcePool"`   // ZFS dataset holding the volume, e.g. tank/vms
	SourceVolume    string `json:"sourceVolume"` // dataset or zvol below it, e.g. web-disk0
	TargetHost      string `json:"targetHost"`
	TargetPool      string `json:"targetPool"`
	IntervalMinutes int    `json:"intervalMinutes"`
	Enabled         bool   `json:"enabled"`
	// Keep is how many replication snapshots stay on each side (DefaultKeep when 0).
	Keep int `json:"keep,omitempty"`
}

// Source and Target are the full dataset names.
func (s Spec) Source() string { return s.SourcePool + "/" + s.SourceVolume }
func (s Spec) Target() string { return s.TargetPool + "/" + s.SourceVolume }

func (s Spec) keep() int {
	if s.Keep <= 0 {
		return DefaultKeep
	}
	return s.Keep
}

// Status is a replication and how its last pass went.
type Status struct {
	Spec             Spec      `json:"spec"`
	State            string    `json:"state"` // idle, syncing, failed
	LastSync         time.Time `json:"lastSync,omitzero"`
	LastSnapshot     string    `json:"lastSnapshot,omitempty"`
	BytesTransferred uint64    `json:"bytesTransferred,omitempty"`
	Error            string    `json:"error,omitempty"`
	// LastAttempt is when the last pass started (successful or not); the schedule counts from it.
	LastAttempt time.Time `json:"lastAttempt,omitzero"`
}

// Runner runs the commands (tests replace it).
type Runner interface {
	// Run runs name with args and returns its standard output.
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
	// Pipe runs send with its standard output connected to recv's input and returns how
	// many bytes went through.
	Pipe(ctx context.Context, send, recv []string) (uint64, error)
}

// ErrInvalid marks a spec that cannot be saved; ErrNotFound an unknown ID; ErrBusy a pass
// that is already running.
var (
	ErrInvalid  = errors.New("invalid")
	ErrNotFound = errors.New("not found")
	ErrBusy     = errors.New("busy")
)

var (
	datasetRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]*(/[A-Za-z0-9][A-Za-z0-9_.:-]*)*$`)
	hostRE    = regexp.MustCompile(`^([A-Za-z0-9._][A-Za-z0-9._-]*@)?[A-Za-z0-9][A-Za-z0-9.:-]*$`)
)

func (s Spec) validate() error {
	switch {
	case !datasetRE.MatchString(s.SourcePool), !datasetRE.MatchString(s.SourceVolume), !datasetRE.MatchString(s.TargetPool):
		return fmt.Errorf("%w: dataset names may use letters, digits and _ . : - separated by /", ErrInvalid)
	case !hostRE.MatchString(s.TargetHost):
		return fmt.Errorf("%w: the target is a host name or address, optionally user@host", ErrInvalid)
	case s.IntervalMinutes < 1 || s.IntervalMinutes > 7*24*60:
		return fmt.Errorf("%w: the interval is 1 minute to 7 days", ErrInvalid)
	case s.Keep < 0 || s.Keep > 1000:
		return fmt.Errorf("%w: keep 0 to 1000 snapshots", ErrInvalid)
	}
	return nil
}

// Manager keeps the replications in path and runs them.
type Manager struct {
	path string
	run  Runner
	log  *slog.Logger
	now  func() time.Time
	// SSH is the ssh command and options used to reach targets.
	SSH []string

	mu    sync.Mutex
	ctx   context.Context // passes end with it (set by Run)
	tasks []*Status
	busy  map[string]bool
	wg    sync.WaitGroup
}

// Open loads path (a missing file is fine).
func Open(path string, r Runner, log *slog.Logger) (*Manager, error) {
	if log == nil {
		log = slog.Default()
	}
	m := &Manager{path: path, run: r, log: log, now: time.Now, busy: map[string]bool{}, ctx: context.Background(),
		SSH: []string{"ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=15", "-o", "StrictHostKeyChecking=accept-new"}}
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &m.tasks); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, t := range m.tasks {
		if t.State == "syncing" { // interrupted by a restart
			t.State, t.Error = "failed", "interrupted by a restart of VirtoMir"
		}
	}
	return m, nil
}

func (m *Manager) saveLocked() error {
	if m.path == "" {
		return nil
	}
	raw, err := json.MarshalIndent(m.tasks, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(m.path, raw, 0o600)
}

// List returns every replication.
func (m *Manager) List() []Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Status, len(m.tasks))
	for i, t := range m.tasks {
		out[i] = *t
	}
	return out
}

// Put adds a replication (no ID) or changes one.
func (m *Manager) Put(spec Spec) (Status, error) {
	if spec.IntervalMinutes == 0 {
		spec.IntervalMinutes = 15
	}
	if err := spec.validate(); err != nil {
		return Status{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tasks {
		if t.Spec.ID != spec.ID && t.Spec.Source() == spec.Source() && t.Spec.TargetHost == spec.TargetHost && t.Spec.Target() == spec.Target() {
			return Status{}, fmt.Errorf("%w: %s already replicates to %s:%s", ErrInvalid, spec.Source(), spec.TargetHost, spec.Target())
		}
	}
	if spec.ID == "" {
		spec.ID = newID()
		m.tasks = append(m.tasks, &Status{Spec: spec, State: "idle"})
	} else {
		i := slices.IndexFunc(m.tasks, func(t *Status) bool { return t.Spec.ID == spec.ID })
		if i < 0 {
			return Status{}, fmt.Errorf("%w: replication %q", ErrNotFound, spec.ID)
		}
		m.tasks[i].Spec = spec
	}
	if err := m.saveLocked(); err != nil {
		return Status{}, err
	}
	return *m.find(spec.ID), nil
}

// Delete removes a replication; its snapshots stay on both sides.
func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := slices.IndexFunc(m.tasks, func(t *Status) bool { return t.Spec.ID == id })
	if i < 0 {
		return fmt.Errorf("%w: replication %q", ErrNotFound, id)
	}
	if m.busy[id] {
		return fmt.Errorf("%w: a pass is running; delete it when it has finished", ErrBusy)
	}
	m.tasks = slices.Delete(m.tasks, i, i+1)
	return m.saveLocked()
}

func (m *Manager) find(id string) *Status {
	for _, t := range m.tasks {
		if t.Spec.ID == id {
			return t
		}
	}
	return nil
}

// Start begins a pass in the background and returns the replication as "syncing".
func (m *Manager) Start(id string) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ctx := m.ctx
	t := m.find(id)
	if t == nil {
		return Status{}, fmt.Errorf("%w: replication %q", ErrNotFound, id)
	}
	if m.busy[id] {
		return Status{}, fmt.Errorf("%w: a pass is already running", ErrBusy)
	}
	m.busy[id] = true
	t.State, t.Error, t.LastAttempt = "syncing", "", m.now().UTC()
	spec := t.Spec
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		snap, n, err := m.pass(ctx, spec)
		m.mu.Lock()
		defer m.mu.Unlock()
		delete(m.busy, id)
		t := m.find(id)
		if t == nil {
			return // deleted meanwhile
		}
		if err != nil {
			t.State, t.Error = "failed", err.Error()
			m.log.Warn("zfs replication failed", "source", spec.Source(), "target", spec.TargetHost+":"+spec.Target(), "err", err)
		} else {
			t.State, t.LastSync, t.LastSnapshot, t.BytesTransferred = "idle", m.now().UTC(), snap, n
			m.log.Info("zfs replication done", "source", spec.Source(), "target", spec.TargetHost+":"+spec.Target(), "snapshot", snap, "bytes", n)
		}
		_ = m.saveLocked()
	}()
	return *t, nil
}

// Wait blocks until passes started so far have finished (tests, shutdown).
func (m *Manager) Wait() { m.wg.Wait() }

// Run starts enabled replications when their interval has passed, until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	m.mu.Lock()
	m.ctx = ctx
	m.mu.Unlock()
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		m.startDue()
		select {
		case <-ctx.Done():
			m.Wait()
			return
		case <-t.C:
		}
	}
}

func (m *Manager) startDue() {
	now := m.now()
	var due []string
	m.mu.Lock()
	for _, t := range m.tasks {
		if t.Spec.Enabled && !m.busy[t.Spec.ID] && now.Sub(t.LastAttempt) >= time.Duration(t.Spec.IntervalMinutes)*time.Minute {
			due = append(due, t.Spec.ID)
		}
	}
	m.mu.Unlock()
	for _, id := range due {
		_, _ = m.Start(id)
	}
}

// pass replicates once and returns the snapshot it sent and the bytes that went through.
func (m *Manager) pass(ctx context.Context, s Spec) (string, uint64, error) {
	src, dst := s.Source(), s.Target()
	remote := func(args ...string) []string {
		return append(append(slices.Clone(m.SSH), s.TargetHost, "--"), args...)
	}

	local, err := m.snapshots(ctx, []string{"zfs"}, src)
	if err != nil {
		return "", 0, fmt.Errorf("source %s: %w", src, err)
	}
	theirs, err := m.snapshots(ctx, remote("zfs"), dst)
	targetExists := err == nil
	if err != nil && !strings.Contains(err.Error(), "does not exist") {
		return "", 0, fmt.Errorf("target %s:%s: %w", s.TargetHost, dst, err)
	}
	base := ""
	for i := len(local) - 1; i >= 0; i-- { // newest first
		if slices.Contains(theirs, local[i]) {
			base = local[i]
			break
		}
	}
	if base == "" && targetExists {
		return "", 0, fmt.Errorf("%s:%s exists but has no snapshot in common with %s; VirtoMir will not overwrite it (remove it, or pick another target pool)", s.TargetHost, dst, src)
	}

	snap := SnapPrefix + m.now().UTC().Format("20060102-150405")
	if _, err := m.run.Run(ctx, "zfs", "snapshot", src+"@"+snap); err != nil {
		return "", 0, fmt.Errorf("snapshot %s: %w", src, err)
	}
	send := []string{"zfs", "send", src + "@" + snap}
	recv := remote("zfs", "receive", "-u", dst)
	if base != "" {
		send = []string{"zfs", "send", "-i", "@" + base, src + "@" + snap}
		recv = remote("zfs", "receive", "-u", "-F", dst) // -F: roll back changes made on the copy
	}
	n, err := m.run.Pipe(ctx, send, recv)
	if err != nil {
		_, _ = m.run.Run(context.WithoutCancel(ctx), "zfs", "destroy", src+"@"+snap) // the next pass starts from base again
		return "", n, fmt.Errorf("send to %s: %w", s.TargetHost, err)
	}

	// Prune old replication snapshots, newest Keep stay on each side.
	local = append(local, snap)
	theirs = append(theirs, snap)
	for _, old := range expired(local, s.keep()) {
		_, _ = m.run.Run(ctx, "zfs", "destroy", src+"@"+old)
	}
	for _, old := range expired(theirs, s.keep()) {
		cmd := remote("zfs", "destroy", dst+"@"+old)
		_, _ = m.run.Run(ctx, cmd[0], cmd[1:]...)
	}
	return snap, n, nil
}

// snapshots lists dataset's replication snapshots (names after @), oldest first.
func (m *Manager) snapshots(ctx context.Context, zfs []string, dataset string) ([]string, error) {
	args := append(slices.Clone(zfs[1:]), "list", "-H", "-p", "-t", "snapshot", "-o", "name", "-s", "creation", "-d", "1", dataset)
	out, err := m.run.Run(ctx, zfs[0], args...)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if _, snap, ok := strings.Cut(strings.TrimSpace(line), "@"); ok && strings.HasPrefix(snap, SnapPrefix) {
			names = append(names, snap)
		}
	}
	return names, nil
}

// expired is all but the newest keep of names (oldest first).
func expired(names []string, keep int) []string {
	if len(names) <= keep {
		return nil
	}
	return names[:len(names)-keep]
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "rep-" + hex.EncodeToString(b)
}
