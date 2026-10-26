package confighist

import (
	"sync"
	"time"
)

// Debouncer gathers the events of one machine that arrive close together (libvirt sends
// several for one change) and handles them once, delay after the last one.
type Debouncer struct {
	delay time.Duration
	fire  func(uuid string, persistent, live bool)

	mu      sync.Mutex
	pending map[string]*pending
	stopped bool
}

type pending struct {
	timer            *time.Timer
	persistent, live bool
}

// NewDebouncer calls fire(uuid, persistent, live) delay after the last Add for uuid.
func NewDebouncer(delay time.Duration, fire func(uuid string, persistent, live bool)) *Debouncer {
	return &Debouncer{delay: delay, fire: fire, pending: map[string]*pending{}}
}

// Add notes a change of the machine's definition (persistent) and/or running configuration (live).
func (d *Debouncer) Add(uuid string, persistent, live bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped {
		return
	}
	p := d.pending[uuid]
	if p == nil {
		np := &pending{}
		d.pending[uuid] = np
		np.timer = time.AfterFunc(d.delay, func() { d.flush(uuid, np) })
		p = np
	} else {
		p.timer.Reset(d.delay)
	}
	p.persistent = p.persistent || persistent
	p.live = p.live || live
}

func (d *Debouncer) flush(uuid string, p *pending) {
	d.mu.Lock()
	if d.pending[uuid] != p || d.stopped { // already handled, or a newer round
		d.mu.Unlock()
		return
	}
	delete(d.pending, uuid)
	persistent, live := p.persistent, p.live
	d.mu.Unlock()
	d.fire(uuid, persistent, live)
}

// Stop drops what is pending; later Adds are ignored.
func (d *Debouncer) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stopped = true
	for id, p := range d.pending {
		p.timer.Stop()
		delete(d.pending, id)
	}
}
