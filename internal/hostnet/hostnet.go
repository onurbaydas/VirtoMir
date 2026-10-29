// Package hostnet creates and removes bridges, bonds and VLAN interfaces on the host
// (ADR-0042) through whatever already manages the host's network: netplan, NetworkManager
// or systemd-networkd. VirtoMir never runs a network stack of its own.
//
// A wrong network change can cut the host off, including the browser that made it. So
// every change is a transaction that must be confirmed from the browser within a deadline
// (by default two minutes); a confirmation that cannot reach the host is no confirmation,
// and the change is rolled back: files restored, created devices deleted, the previous
// configuration applied again. The pending transaction is saved first, so a daemon that
// dies in between rolls back when it starts.
package hostnet

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/onurbaydas/virtomir/internal/hostinfo"
)

// ErrInvalid and ErrRefused classify errors for the API (400 / 409).
var (
	ErrInvalid = errors.New("invalid")
	ErrRefused = errors.New("refused")
)

// classed is a sentence for the user that errors.Is matches to its class.
type classed struct {
	class error
	msg   string
}

func (e classed) Error() string        { return e.msg }
func (e classed) Is(target error) bool { return target == e.class }

func invalidf(format string, a ...any) error {
	return classed{ErrInvalid, fmt.Sprintf(format, a...)}
}

func refusedf(format string, a ...any) error {
	return classed{ErrRefused, fmt.Sprintf(format, a...)}
}

// Spec is a new host interface.
type Spec struct {
	Kind string `json:"kind"` // bridge, bond, vlan
	Name string `json:"name"`
	// Ports: bridge ports or bond members.
	Ports    []string `json:"ports,omitempty"`
	BondMode string   `json:"bondMode,omitempty"` // active-backup (default), 802.3ad, balance-alb, balance-tlb, balance-xor, broadcast, balance-rr
	STP      bool     `json:"stp,omitempty"`
	// vlan: Parent interface and VLAN ID.
	Parent string `json:"parent,omitempty"`
	VLAN   int    `json:"vlan,omitempty"`
	// TakeAddressFrom names a port whose IP configuration (DHCP or static addresses,
	// routes, DNS) moves to the new bridge or bond: the usual way to put the host's own
	// uplink into a bridge for machines without losing the host's address.
	TakeAddressFrom string `json:"takeAddressFrom,omitempty"`
}

var bondModes = []string{"active-backup", "802.3ad", "balance-alb", "balance-tlb", "balance-xor", "broadcast", "balance-rr"}

var ifNameRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,14}$`)

// Record is an interface VirtoMir created, with what is needed to remove it cleanly.
type Record struct {
	Spec
	Backend string    `json:"backend"`
	Created time.Time `json:"created"`
	// netplan: the address keys taken from TakeAddressFrom, to give back on removal.
	Moved string `json:"moved,omitempty"`
	// netplan: port definitions VirtoMir added (ports netplan did not know before).
	Added []string `json:"added,omitempty"`
	// NetworkManager: the connection each port had before, to bring back on removal, and
	// the commands that made it (to restore it if a removal is rolled back).
	OldConns map[string]string `json:"oldConns,omitempty"`
	Recreate []cmd             `json:"recreate,omitempty"`
}

// Pending is a change waiting for confirmation.
type Pending struct {
	ID       string    `json:"id"`
	What     string    `json:"what"`
	Backend  string    `json:"backend"`
	Started  time.Time `json:"started"`
	Deadline time.Time `json:"deadline"`

	Files   []fileSnap `json:"files,omitempty"`
	Undo    []cmd      `json:"undo,omitempty"`
	Reapply []cmd      `json:"reapply,omitempty"`
}

type fileSnap struct {
	Path    string      `json:"path"`
	Existed bool        `json:"existed"`
	Data    []byte      `json:"data,omitempty"`
	Mode    os.FileMode `json:"mode,omitempty"`
}

// cmd is one command; Optional ones may fail (e.g. deleting a device that is already gone).
type cmd struct {
	Args     []string `json:"args"`
	Optional bool     `json:"optional,omitempty"`
}

func must(args ...string) cmd { return cmd{Args: args} }
func try(args ...string) cmd  { return cmd{Args: args, Optional: true} }

// plan is what a backend wants done for one change.
type plan struct {
	writes  map[string][]byte // path → content; nil deletes the file
	modes   map[string]os.FileMode
	apply   []cmd // after the files are written
	undo    []cmd // on rollback, before the old files are applied again
	reapply []cmd // on rollback, after the old files are back
	record  *Record
}

func newPlan() plan {
	return plan{writes: map[string][]byte{}, modes: map[string]os.FileMode{}}
}

// Runner runs a command; tests replace it.
type Runner func(ctx context.Context, args ...string) ([]byte, error)

func execRunner(ctx context.Context, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// Manager applies changes one at a time.
type Manager struct {
	Root     string // "/" on a host; tests use a temporary directory
	StateDir string
	Run      Runner
	Log      *slog.Logger
	// Interfaces lists the host's interfaces (hostinfo); Window is the confirmation time.
	Interfaces func() []hostinfo.Interface
	Window     time.Duration
	LookPath   func(string) (string, error)

	mu      sync.Mutex
	pending *Pending
	timer   *time.Timer
}

// New returns a manager for the real host. It rolls back a change left unconfirmed by a
// previous run.
func New(stateDir string, log *slog.Logger, h *hostinfo.Reader) *Manager {
	m := &Manager{Root: "/", StateDir: stateDir, Run: execRunner, Log: log, Window: 2 * time.Minute, LookPath: exec.LookPath,
		Interfaces: func() []hostinfo.Interface { return h.Interfaces(nil) }}
	m.Recover()
	return m
}

func (m *Manager) path(p string) string { return filepath.Join(m.Root, p) }

func (m *Manager) pendingFile() string { return filepath.Join(m.StateDir, "hostnet-pending.json") }
func (m *Manager) recordsFile() string { return filepath.Join(m.StateDir, "hostnet.json") }

// Records lists the interfaces VirtoMir created.
func (m *Manager) Records() []Record {
	var out []Record
	if b, err := os.ReadFile(m.recordsFile()); err == nil {
		_ = json.Unmarshal(b, &out)
	}
	if out == nil {
		out = []Record{}
	}
	return out
}

// Status is what the UI shows: the backend in charge, the pending change, VirtoMir's own
// interfaces.
type Status struct {
	Backend string   `json:"backend"` // netplan, networkmanager, networkd, "" (unsupported)
	Reason  string   `json:"reason,omitempty"`
	Pending *Pending `json:"pending,omitempty"`
	Created []Record `json:"created"`
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := Status{Created: m.Records()}
	if b, err := m.backend(); err == nil {
		st.Backend = b.name()
	} else {
		st.Reason = err.Error()
	}
	if m.pending != nil {
		p := *m.pending
		p.Files, p.Undo, p.Reapply = nil, nil, nil
		st.Pending = &p
	}
	return st
}

// backend picks what manages this host's network.
func (m *Manager) backend() (backend, error) {
	look := m.LookPath
	if look == nil {
		look = exec.LookPath
	}
	if files, _ := filepath.Glob(m.path("etc/netplan/*.yaml")); len(files) > 0 {
		if _, err := look("netplan"); err == nil {
			return &netplanBackend{m: m}, nil
		}
	}
	if m.active("NetworkManager") {
		if _, err := look("nmcli"); err == nil {
			return &nmBackend{m: m}, nil
		}
	}
	if m.active("systemd-networkd") {
		if _, err := look("networkctl"); err == nil {
			return &networkdBackend{m: m}, nil
		}
	}
	return nil, refusedf("This host's network is not managed by netplan, NetworkManager or systemd-networkd (e.g. ifupdown, /etc/network/interfaces), so VirtoMir leaves it alone. Create bridges and bonds there by hand.")
}

func (m *Manager) active(unit string) bool {
	out, err := m.Run(context.Background(), "systemctl", "is-active", unit)
	return err == nil && strings.TrimSpace(string(out)) == "active"
}

// validate checks a spec against the host as it is now.
func (m *Manager) validate(s *Spec) (map[string]hostinfo.Interface, error) {
	ifs := map[string]hostinfo.Interface{}
	for _, it := range m.Interfaces() {
		ifs[it.Name] = it
	}
	if !ifNameRE.MatchString(s.Name) {
		return nil, invalidf("An interface name is 1-15 letters, digits, '.', '-' or '_', starting with a letter (e.g. br0, bond0, eth0.10).")
	}
	if _, ok := ifs[s.Name]; ok {
		return nil, refusedf("%s already exists on this host.", s.Name)
	}
	gw := m.defaultRouteIfaces()
	check := func(p string) error {
		it, ok := ifs[p]
		if !ok {
			return invalidf("%s is not an interface on this host.", p)
		}
		switch it.Kind {
		case "loopback", "tap", "veth":
			return refusedf("%s is a %s interface and cannot be used here.", p, it.Kind)
		}
		if strings.HasPrefix(p, "virbr") || strings.HasPrefix(p, "vnet") {
			return refusedf("%s belongs to libvirt.", p)
		}
		if it.Master != "" {
			return refusedf("%s is already part of %s.", p, it.Master)
		}
		if it.Wireless && s.Kind == "bridge" {
			return refusedf("%s is Wi-Fi: an access point drops frames from other machines' MAC addresses, so it cannot be a bridge port. Give machines NAT through it instead.", p)
		}
		return nil
	}
	switch s.Kind {
	case "bridge", "bond":
		if len(s.Ports) == 0 && s.Kind == "bond" {
			return nil, invalidf("A bond needs at least one member.")
		}
		seen := map[string]bool{}
		for _, p := range s.Ports {
			if seen[p] {
				return nil, invalidf("%s is listed twice.", p)
			}
			seen[p] = true
			if err := check(p); err != nil {
				return nil, err
			}
			if s.Kind == "bond" && ifs[p].Kind != "physical" {
				return nil, refusedf("Bond members must be physical ports; %s is a %s.", p, ifs[p].Kind)
			}
			if p == s.TakeAddressFrom {
				continue
			}
			// A port loses its own addresses: refuse that for the host's uplink and for any
			// port with an address, unless its address moves along.
			if gw[p] {
				return nil, refusedf("%s carries this host's default route. Put it into the %s only with \"take over its address\", or the host loses its connection.", p, s.Kind)
			}
			if a := globalAddrs(ifs[p].Addresses); len(a) > 0 {
				return nil, refusedf("%s has the address %s, which it would lose. Use \"take over its address\" for it, or remove the address first.", p, strings.Join(a, ", "))
			}
		}
		if s.TakeAddressFrom != "" && !seen[s.TakeAddressFrom] {
			return nil, invalidf("The address can only be taken from one of the ports.")
		}
		if s.Kind == "bond" {
			if s.BondMode == "" {
				s.BondMode = "active-backup"
			}
			if !slices.Contains(bondModes, s.BondMode) {
				return nil, invalidf("Unknown bond mode %q (%s).", s.BondMode, strings.Join(bondModes, ", "))
			}
		} else if s.BondMode != "" {
			return nil, invalidf("A bond mode only applies to bonds.")
		}
		if s.Kind == "bond" && s.STP {
			return nil, invalidf("STP applies to bridges.")
		}
		s.Parent, s.VLAN = "", 0
	case "vlan":
		if s.VLAN < 1 || s.VLAN > 4094 {
			return nil, invalidf("A VLAN ID is between 1 and 4094.")
		}
		it, ok := ifs[s.Parent]
		if !ok {
			return nil, invalidf("%s is not an interface on this host.", s.Parent)
		}
		if it.Kind == "loopback" || it.Kind == "tap" || it.Master != "" && ifs[it.Master].Kind == "bond" {
			return nil, refusedf("%s cannot carry a VLAN.", s.Parent)
		}
		if len(s.Ports) > 0 || s.TakeAddressFrom != "" || s.BondMode != "" || s.STP {
			return nil, invalidf("A VLAN interface has a parent and an ID, nothing else.")
		}
	default:
		return nil, invalidf("Kind must be bridge, bond or vlan.")
	}
	return ifs, nil
}

func globalAddrs(as []string) []string {
	var out []string
	for _, a := range as {
		p, err := netip.ParsePrefix(a)
		if err != nil || p.Addr().IsLinkLocalUnicast() || p.Addr().IsLoopback() {
			continue
		}
		out = append(out, a)
	}
	return out
}

// defaultRouteIfaces are the interfaces with an IPv4 or IPv6 default route.
func (m *Manager) defaultRouteIfaces() map[string]bool {
	out := map[string]bool{}
	if b, err := os.ReadFile(m.path("proc/net/route")); err == nil {
		for _, l := range strings.Split(string(b), "\n")[1:] {
			f := strings.Fields(l)
			if len(f) > 2 && f[1] == "00000000" && f[7] == "00000000" {
				out[f[0]] = true
			}
		}
	}
	if b, err := os.ReadFile(m.path("proc/net/ipv6_route")); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			f := strings.Fields(l)
			if len(f) == 10 && f[0] == strings.Repeat("0", 32) && f[1] == "00" && f[9] != "lo" {
				out[f[9]] = true
			}
		}
	}
	return out
}

// Create adds an interface; it must be confirmed with Confirm before the window ends.
func (m *Manager) Create(ctx context.Context, s Spec) (*Pending, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending != nil {
		return nil, refusedf("Another network change is waiting for confirmation.")
	}
	b, err := m.backend()
	if err != nil {
		return nil, err
	}
	ifs, err := m.validate(&s)
	if err != nil {
		return nil, err
	}
	p, err := b.create(ctx, s, ifs)
	if err != nil {
		return nil, err
	}
	p.record.Spec, p.record.Backend, p.record.Created = s, b.name(), time.Now().UTC()
	recs := append(m.Records(), *p.record)
	return m.begin(ctx, b.name(), describe(s), p, recs)
}

// Delete removes an interface VirtoMir created; also confirmed like Create.
func (m *Manager) Delete(ctx context.Context, name string) (*Pending, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending != nil {
		return nil, refusedf("Another network change is waiting for confirmation.")
	}
	recs := m.Records()
	i := slices.IndexFunc(recs, func(r Record) bool { return r.Name == name })
	if i < 0 {
		return nil, refusedf("%s was not created by VirtoMir; change it where it was made.", name)
	}
	for _, it := range m.Interfaces() {
		if it.Master == name {
			if slices.Contains(recs[i].Ports, it.Name) {
				continue
			}
			return nil, refusedf("%s is still in use by %s.", name, it.Name)
		}
	}
	for _, r := range recs {
		if r.Parent == name || slices.Contains(r.Ports, name) {
			return nil, refusedf("Remove %s first; it is built on %s.", r.Name, name)
		}
	}
	b, err := m.backend()
	if err != nil {
		return nil, err
	}
	if b.name() != recs[i].Backend {
		return nil, refusedf("%s was made with %s, but this host now uses %s.", name, recs[i].Backend, b.name())
	}
	p, err := b.remove(ctx, recs[i])
	if err != nil {
		return nil, err
	}
	rest := slices.Delete(slices.Clone(recs), i, i+1)
	return m.begin(ctx, b.name(), "remove "+name, p, rest)
}

func describe(s Spec) string {
	switch s.Kind {
	case "vlan":
		return fmt.Sprintf("VLAN %d on %s as %s", s.VLAN, s.Parent, s.Name)
	case "bond":
		return fmt.Sprintf("bond %s (%s) of %s", s.Name, s.BondMode, strings.Join(s.Ports, ", "))
	}
	d := "bridge " + s.Name
	if len(s.Ports) > 0 {
		d += " on " + strings.Join(s.Ports, ", ")
	}
	if s.TakeAddressFrom != "" {
		d += ", taking over the address of " + s.TakeAddressFrom
	}
	return d
}

// begin snapshots every file the plan touches (and the records file), writes the new
// files, runs the plan and arms the rollback timer. A failure rolls back at once.
func (m *Manager) begin(ctx context.Context, backend, what string, p plan, recs []Record) (*Pending, error) {
	rb, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return nil, err
	}
	p.writes[m.recordsFile()] = rb
	p.modes[m.recordsFile()] = 0o600
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	now := time.Now().UTC()
	pend := &Pending{ID: hex.EncodeToString(id), What: what, Backend: backend, Started: now, Deadline: now.Add(m.Window),
		Undo: p.undo, Reapply: p.reapply}
	paths := make([]string, 0, len(p.writes))
	for path := range p.writes {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	for _, path := range paths {
		snap := fileSnap{Path: path}
		if fi, err := os.Stat(path); err == nil {
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			snap.Existed, snap.Data, snap.Mode = true, data, fi.Mode().Perm()
		}
		pend.Files = append(pend.Files, snap)
	}
	// Saved before anything changes: a crash from here on rolls back at the next start.
	if err := m.savePending(pend); err != nil {
		return nil, fmt.Errorf("save the pending change: %w", err)
	}
	m.pending = pend
	fail := func(err error) (*Pending, error) {
		m.rollbackLocked("the change failed")
		return nil, err
	}
	for _, path := range paths {
		data := p.writes[path]
		if data == nil {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return fail(err)
			}
			continue
		}
		mode := p.modes[path]
		if mode == 0 {
			mode = 0o644
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fail(err)
		}
		if err := os.WriteFile(path, data, mode); err != nil {
			return fail(err)
		}
		_ = os.Chmod(path, mode)
	}
	for _, c := range p.apply {
		if _, err := m.Run(ctx, c.Args...); err != nil && !c.Optional {
			return fail(refusedf("%v — the previous configuration was restored", err))
		}
	}
	m.timer = time.AfterFunc(time.Until(pend.Deadline), func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.pending != nil && m.pending.ID == pend.ID {
			m.rollbackLocked("not confirmed in time")
		}
	})
	m.Log.Info("host network change pending", "what", what, "backend", backend, "deadline", pend.Deadline)
	out := *pend
	out.Files, out.Undo, out.Reapply = nil, nil, nil
	return &out, nil
}

// Confirm keeps a pending change.
func (m *Manager) Confirm(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending == nil || m.pending.ID != id {
		return refusedf("No such change is waiting; it may have been rolled back already.")
	}
	if m.timer != nil {
		m.timer.Stop()
	}
	m.Log.Info("host network change confirmed", "what", m.pending.What)
	m.pending, m.timer = nil, nil
	return os.Remove(m.pendingFile())
}

// Rollback undoes a pending change now.
func (m *Manager) Rollback(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending == nil || m.pending.ID != id {
		return refusedf("No such change is waiting.")
	}
	m.rollbackLocked("undone by the user")
	return nil
}

// Recover rolls back a change a previous run left unconfirmed.
func (m *Manager) Recover() {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, err := os.ReadFile(m.pendingFile())
	if err != nil {
		return
	}
	var p Pending
	if json.Unmarshal(b, &p) != nil {
		return
	}
	m.pending = &p
	m.rollbackLocked("VirtoMir restarted before the change was confirmed")
}

func (m *Manager) rollbackLocked(why string) {
	p := m.pending
	if p == nil {
		return
	}
	if m.timer != nil {
		m.timer.Stop()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for _, f := range p.Files {
		var err error
		if f.Existed {
			err = os.WriteFile(f.Path, f.Data, f.Mode)
			_ = os.Chmod(f.Path, f.Mode)
		} else if err = os.Remove(f.Path); os.IsNotExist(err) {
			err = nil
		}
		if err != nil {
			m.Log.Error("host network rollback: restore file", "path", f.Path, "err", err)
		}
	}
	for _, c := range append(slices.Clone(p.Undo), p.Reapply...) {
		if _, err := m.Run(ctx, c.Args...); err != nil && !c.Optional {
			m.Log.Error("host network rollback", "err", err)
		}
	}
	m.Log.Warn("host network change rolled back", "what", p.What, "why", why)
	m.pending, m.timer = nil, nil
	_ = os.Remove(m.pendingFile())
}

func (m *Manager) savePending(p *Pending) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(m.StateDir, 0o755); err != nil {
		return err
	}
	tmp := m.pendingFile() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.pendingFile())
}

type backend interface {
	name() string
	create(ctx context.Context, s Spec, ifs map[string]hostinfo.Interface) (plan, error)
	remove(ctx context.Context, r Record) (plan, error)
}
