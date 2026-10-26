// Package jobs runs long operations (disk copies, uploads) in the background so HTTP
// requests return at once. The UI polls a job until it has finished.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"runtime/debug"
	"slices"
	"sync"
	"time"
)

type State string

const (
	Running   State = "running"
	Succeeded State = "succeeded"
	Failed    State = "failed"
)

type Job struct {
	ID       string     `json:"id"`
	Kind     string     `json:"kind"` // e.g. "clone"
	Title    string     `json:"title"`
	State    State      `json:"state"`
	Message  string     `json:"message,omitempty"` // progress, or the error when failed
	Result   string     `json:"result,omitempty"`  // e.g. the new machine's UUID
	Started  time.Time  `json:"started"`
	Finished *time.Time `json:"finished,omitempty"`
}

// Keep is how many finished jobs are remembered.
const Keep = 50

type Func func(ctx context.Context, report func(msg string)) (result string, err error)

type Manager struct {
	ctx context.Context
	// Log receives the stack of a job that panicked (nil: slog.Default()).
	Log *slog.Logger

	mu   sync.Mutex
	jobs []*Job // oldest first
}

// New returns a manager whose jobs are cancelled when ctx ends (daemon shutdown).
func New(ctx context.Context) *Manager { return &Manager{ctx: ctx} }

// Start runs fn in the background and returns its job right away.
func (m *Manager) Start(kind, title string, fn Func) Job {
	j := &Job{ID: newID(), Kind: kind, Title: title, State: Running, Started: time.Now().UTC()}
	m.mu.Lock()
	m.jobs = append(m.jobs, j)
	m.trim()
	snap := *j
	m.mu.Unlock()

	go func() {
		res, err := m.run(j, fn)
		m.mu.Lock()
		defer m.mu.Unlock()
		now := time.Now().UTC()
		j.Finished = &now
		if err != nil {
			j.State, j.Message = Failed, err.Error()
			return
		}
		j.State, j.Message, j.Result = Succeeded, "", res
	}()
	return snap
}

// run calls fn; a panic in it fails the job (and is logged with its stack) instead of
// taking the whole daemon down.
func (m *Manager) run(j *Job, fn Func) (res string, err error) {
	defer func() {
		if r := recover(); r != nil {
			log := m.Log
			if log == nil {
				log = slog.Default()
			}
			log.Error("job panicked", "job", j.ID, "kind", j.Kind, "title", j.Title, "panic", r, "stack", string(debug.Stack()))
			res, err = "", fmt.Errorf("internal error: %v", r)
		}
	}()
	return fn(m.ctx, func(msg string) {
		m.mu.Lock()
		j.Message = msg
		m.mu.Unlock()
	})
}

// Running counts the jobs that have not finished yet.
func (m *Manager) Running() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, j := range m.jobs {
		if j.State == Running {
			n++
		}
	}
	return n
}

func (m *Manager) Get(id string) (Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range m.jobs {
		if j.ID == id {
			return *j, true
		}
	}
	return Job{}, false
}

// List returns all known jobs, newest first.
func (m *Manager) List() []Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Job, 0, len(m.jobs))
	for _, j := range slices.Backward(m.jobs) {
		out = append(out, *j)
	}
	return out
}

// trim drops the oldest finished jobs beyond Keep; running jobs are never dropped.
func (m *Manager) trim() {
	for len(m.jobs) > Keep {
		i := slices.IndexFunc(m.jobs, func(j *Job) bool { return j.State != Running })
		if i < 0 {
			return
		}
		m.jobs = slices.Delete(m.jobs, i, i+1)
	}
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
