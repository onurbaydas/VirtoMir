// Wave migration of multiple VMware machines in parallel (ADR-0061).
//
// A wave runs conversions under a concurrency limit, reporting per-item progress
// and an overall "N of M done" summary string to the jobs framework. Partial failures
// leave the completed machines defined in VirtoMir and allow retrying just the failed ones.
package vmware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// WaveItemState is the status of one VM in a migration wave.
type WaveItemState string

const (
	WaveItemPending WaveItemState = "pending"
	WaveItemRunning WaveItemState = "running"
	WaveItemDone    WaveItemState = "done"
	WaveItemFailed  WaveItemState = "failed"
)

// WaveItem is one VM scheduled or processed in a migration wave.
type WaveItem struct {
	VMID    string        `json:"vmId"`
	Name    string        `json:"name"`
	State   WaveItemState `json:"state"`
	Message string        `json:"message,omitempty"`
}

// Wave is a batch of VM conversions executed together.
type Wave struct {
	ID       string     `json:"id"`
	Items    []WaveItem `json:"items"`
	Parallel int        `json:"parallel"`
	Created  time.Time  `json:"created"`
	conn     Connection `json:"-"`
}

func (w *Wave) snapshot() Wave {
	snap := *w
	snap.Items = slices.Clone(w.Items)
	return snap
}

// OneConverter converts single VMs from VMware to VirtoMir.
type OneConverter interface {
	ConvertOne(ctx context.Context, conn Connection, vmID string, report func(string)) error
}

// WaveConverter is the production OneConverter that resolves the VM from the inventory
// Lister, runs Preflight then Convert with virt-v2v.
type WaveConverter struct {
	Lister        Lister
	Converter     *Converter
	Opts          ConvertOpts
	PoolFreeBytes uint64

	mu            sync.Mutex
	cachedInv     *Inventory
	reservedBytes uint64
	// listMu lets one worker of a wave list the inventory while the others wait for it,
	// instead of each asking vCenter.
	listMu sync.Mutex
}

// NewWaveConverter returns a OneConverter that lists inventory, preflights and converts with virt-v2v.
func NewWaveConverter(conv *Converter, lister Lister, opts ConvertOpts) *WaveConverter {
	return &WaveConverter{
		Converter: conv,
		Lister:    lister,
		Opts:      opts,
	}
}

func (c *WaveConverter) poolFree() uint64 {
	if c.PoolFreeBytes > 0 {
		return c.PoolFreeBytes
	}
	if c.Converter != nil && c.Converter.client != nil {
		if pools, err := c.Converter.client.ListPools(); err == nil {
			for _, p := range pools {
				if p.Path == c.Opts.PoolPath {
					return p.AvailableBytes
				}
			}
		}
	}
	return 0
}

// ConvertOne resolves the VM by ID or Name from inventory, runs Preflight, and executes Convert.
func (c *WaveConverter) ConvertOne(ctx context.Context, conn Connection, vmID string, report func(string)) error {
	c.listMu.Lock()
	c.mu.Lock()
	inv := c.cachedInv
	c.mu.Unlock()

	if inv == nil {
		lister := c.Lister
		if lister == nil {
			lister = GovmomiLister{}
		}
		listed, err := lister.List(ctx, conn)
		if err != nil {
			c.listMu.Unlock()
			return err
		}
		c.mu.Lock()
		c.cachedInv = &listed
		inv = c.cachedInv
		c.mu.Unlock()
	}
	c.listMu.Unlock()

	var target *VM
	for i := range inv.VMs {
		if inv.VMs[i].ID == vmID || (inv.VMs[i].ID == "" && inv.VMs[i].Name == vmID) {
			target = &inv.VMs[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("VM %q not found in VMware inventory", vmID)
	}

	var need uint64
	for _, d := range target.Disks {
		need += d.CapacityBytes
	}
	needed := need + need/10

	c.mu.Lock()
	baseFree := c.poolFree()
	var effectiveFree uint64
	if baseFree > c.reservedBytes {
		effectiveFree = baseFree - c.reservedBytes
	}

	conv := c.Converter
	if conv == nil {
		conv = NewConverter(nil)
	}

	if err := conv.Preflight(ctx, conn, *target, effectiveFree); err != nil {
		c.mu.Unlock()
		return err
	}
	c.reservedBytes += needed
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		if c.reservedBytes >= needed {
			c.reservedBytes -= needed
		} else {
			c.reservedBytes = 0
		}
		c.mu.Unlock()
	}()

	opts := c.Opts
	// Each VM in a wave gets its own name in VirtoMir to prevent collisions.
	opts.Name = target.Name
	if opts.Name == "" {
		opts.Name = vmID
	}

	_, err := conv.Convert(ctx, conn, *target, opts, report)
	return err
}

var _ OneConverter = (*WaveConverter)(nil)

// KeepWaves is the maximum number of waves remembered in memory.
const KeepWaves = 20

// Registry tracks recent VMware migration waves in memory, newest first.
type Registry struct {
	mu    sync.Mutex
	waves []*Wave // newest first, at most KeepWaves
}

// NewRegistry returns an empty wave registry.
func NewRegistry() *Registry {
	return &Registry{}
}

// Start launches a new migration wave, running ConvertOne for each VM under a semaphore
// clamped to 1..4 parallel workers (default 2). Returns an error only if every item failed.
func (r *Registry) Start(ctx context.Context, conn Connection, vmIDs []string, parallel int, conv OneConverter, report func(string)) (Wave, error) {
	seen := make(map[string]bool, len(vmIDs))
	var uniqueIDs []string
	for _, id := range vmIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		uniqueIDs = append(uniqueIDs, id)
	}
	if len(uniqueIDs) == 0 {
		return Wave{}, invalidf("no VMs specified for wave migration")
	}

	if parallel <= 0 {
		parallel = 2
	} else if parallel > 4 {
		parallel = 4
	}

	items := make([]WaveItem, len(uniqueIDs))
	for i, id := range uniqueIDs {
		items[i] = WaveItem{
			VMID:  id,
			Name:  id,
			State: WaveItemPending,
		}
	}

	w := &Wave{
		ID:       newID(),
		Items:    items,
		Parallel: parallel,
		Created:  time.Now().UTC(),
		conn:     conn,
	}

	r.mu.Lock()
	r.waves = append([]*Wave{w}, r.waves...)
	if len(r.waves) > KeepWaves {
		r.waves = r.waves[:KeepWaves]
	}
	r.mu.Unlock()

	indices := make([]int, len(items))
	for i := range indices {
		indices[i] = i
	}

	return r.run(ctx, w, indices, conv, report)
}

// Retry re-runs only the failed items of an existing wave under the same concurrency rules.
func (r *Registry) Retry(ctx context.Context, waveID string, conv OneConverter, report func(string)) (Wave, error) {
	r.mu.Lock()
	var target *Wave
	for _, w := range r.waves {
		if w.ID == waveID {
			target = w
			break
		}
	}
	if target == nil {
		r.mu.Unlock()
		return Wave{}, fmt.Errorf("wave %q not found", waveID)
	}

	for _, item := range target.Items {
		if item.State == WaveItemRunning || item.State == WaveItemPending {
			r.mu.Unlock()
			return Wave{}, fmt.Errorf("wave %q is already running", waveID)
		}
	}

	var failedIndices []int
	for i, item := range target.Items {
		if item.State == WaveItemFailed {
			failedIndices = append(failedIndices, i)
			target.Items[i].State = WaveItemPending
			target.Items[i].Message = ""
		}
	}

	if len(failedIndices) == 0 {
		snap := target.snapshot()
		r.mu.Unlock()
		return snap, nil
	}
	r.mu.Unlock()

	return r.run(ctx, target, failedIndices, conv, report)
}

// Get returns a snapshot of the wave with id, or false if not found.
func (r *Registry) Get(id string) (Wave, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, w := range r.waves {
		if w.ID == id {
			return w.snapshot(), true
		}
	}
	return Wave{}, false
}

// List returns all known waves, newest first.
func (r *Registry) List() []Wave {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Wave, len(r.waves))
	for i, w := range r.waves {
		out[i] = w.snapshot()
	}
	return out
}

func (r *Registry) run(ctx context.Context, w *Wave, indices []int, conv OneConverter, report func(string)) (Wave, error) {
	if report == nil {
		report = func(string) {}
	}
	// Workers report in parallel; callers (job progress, tests) need not be safe for concurrent use.
	var reportMu sync.Mutex
	unsafeReport := report
	report = func(msg string) {
		reportMu.Lock()
		defer reportMu.Unlock()
		unsafeReport(msg)
	}
	if conv == nil {
		conv = &WaveConverter{}
	}

	r.mu.Lock()
	initialReport := waveReport(w.Items)
	conn := w.conn
	parallel := w.Parallel
	r.mu.Unlock()
	report(initialReport)

	if len(indices) == 0 {
		r.mu.Lock()
		snap := w.snapshot()
		r.mu.Unlock()
		return snap, nil
	}

	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup

	for _, idx := range indices {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()

			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				r.mu.Lock()
				w.Items[i].State = WaveItemFailed
				w.Items[i].Message = ctx.Err().Error()
				rep := waveReport(w.Items)
				r.mu.Unlock()
				report(rep)
				return
			}

			// Transition to running
			r.mu.Lock()
			w.Items[i].State = WaveItemRunning
			w.Items[i].Message = ""
			rep := waveReport(w.Items)
			r.mu.Unlock()
			report(rep)

			itemReport := func(msg string) {
				r.mu.Lock()
				w.Items[i].Message = msg
				r.mu.Unlock()
			}

			err := conv.ConvertOne(ctx, conn, w.Items[i].VMID, itemReport)

			r.mu.Lock()
			if err != nil {
				w.Items[i].State = WaveItemFailed
				w.Items[i].Message = err.Error()
			} else {
				w.Items[i].State = WaveItemDone
				w.Items[i].Message = ""
			}
			rep = waveReport(w.Items)
			r.mu.Unlock()
			report(rep)
		}(idx)
	}

	wg.Wait()

	r.mu.Lock()
	snap := w.snapshot()
	r.mu.Unlock()

	var failedCount int
	for _, i := range indices {
		if snap.Items[i].State == WaveItemFailed {
			failedCount++
		}
	}

	if len(indices) > 0 && failedCount == len(indices) {
		if ctx.Err() != nil {
			return snap, fmt.Errorf("all %d items failed: %w", failedCount, ctx.Err())
		}
		return snap, fmt.Errorf("all %d items failed", failedCount)
	}
	return snap, nil
}

func waveReport(items []WaveItem) string {
	var done, failed int
	for _, item := range items {
		switch item.State {
		case WaveItemDone:
			done++
		case WaveItemFailed:
			failed++
		}
	}
	s := fmt.Sprintf("%d of %d done", done, len(items))
	if failed > 0 {
		s += fmt.Sprintf(", %d failed", failed)
	}
	return s
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
