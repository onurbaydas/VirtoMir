// Package reports writes capacity, inventory and backup-compliance reports from facts the
// API collects on every cluster member, keeps them in -state-dir/reports, and runs report
// schedules (ADR-0030, made real in ADR-0032). Reports only state what was measured: a
// number VirtoMir does not know is left out, never estimated.
package reports

import (
	"bytes"
	"crypto/rand"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Kind string

const (
	Capacity  Kind = "capacity"
	Inventory Kind = "inventory"
	BackupSLA Kind = "backup_sla"
)

type Format string

const (
	HTML Format = "html"
	CSV  Format = "csv"
	JSON Format = "json"
)

// Kinds and formats a request may name.
var (
	kindTitles = map[Kind]string{Capacity: "Capacity", Inventory: "Machine inventory", BackupSLA: "Backup compliance"}
	formats    = map[Format]bool{HTML: true, CSV: true, JSON: true}
)

// Meta describes one stored report.
type Meta struct {
	ID          string    `json:"id"`
	Kind        Kind      `json:"kind"`
	Title       string    `json:"title"`
	Format      Format    `json:"format"`
	GeneratedAt time.Time `json:"generatedAt"`
	SizeBytes   int64     `json:"sizeBytes"`
	Summary     string    `json:"summary"`
	// By is who asked for it, or "schedule: <name>".
	By         string `json:"by"`
	ScheduleID string `json:"scheduleId,omitempty"`
}

// Schedule makes a report regularly.
type Schedule struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Kind    Kind   `json:"kind"`
	Format  Format `json:"format"`
	Enabled bool   `json:"enabled"`
	// Every: "daily", "weekly" (Mondays) or "monthly" (on the 1st), at At ("HH:MM", local).
	Every string `json:"every"`
	At    string `json:"at"`
	// KeepDays deletes this schedule's reports older than that (0: keep all).
	KeepDays int `json:"keepDays"`
	// Notify sends the summary through the alert channels (webhook, email) when done.
	Notify    bool       `json:"notify"`
	LastRun   *time.Time `json:"lastRun,omitempty"`
	LastError string     `json:"lastError,omitempty"`
	NextRun   *time.Time `json:"nextRun,omitempty"` // computed
}

func (sc Schedule) validate() error {
	if strings.TrimSpace(sc.Name) == "" {
		return errors.New("the schedule needs a name")
	}
	if _, ok := kindTitles[sc.Kind]; !ok {
		return fmt.Errorf("unknown report %q (capacity, inventory or backup_sla)", sc.Kind)
	}
	if !formats[sc.Format] {
		return fmt.Errorf("unknown format %q (html, csv or json)", sc.Format)
	}
	switch sc.Every {
	case "daily", "weekly", "monthly":
	default:
		return errors.New(`"every" must be daily, weekly or monthly`)
	}
	if _, err := time.Parse("15:04", sc.At); err != nil {
		return errors.New(`"at" must be a time like 06:30`)
	}
	if sc.KeepDays < 0 || sc.KeepDays > 3650 {
		return errors.New("keep between 0 (forever) and 3650 days")
	}
	return nil
}

// Next is the first run after t.
func (sc Schedule) Next(t time.Time) time.Time {
	at, _ := time.Parse("15:04", sc.At)
	n := time.Date(t.Year(), t.Month(), t.Day(), at.Hour(), at.Minute(), 0, 0, t.Location())
	for !n.After(t) || (sc.Every == "weekly" && n.Weekday() != time.Monday) || (sc.Every == "monthly" && n.Day() != 1) {
		n = n.AddDate(0, 0, 1)
	}
	return n
}

// ---- Facts --------------------------------------------------------------------------------

// Facts is what the reports are made of: one entry per cluster member that answered.
type Facts struct {
	Hosts []HostFacts `json:"hosts"`
	// Missing names members that did not answer; their machines are not in the report.
	Missing []string `json:"missing,omitempty"`
}

type HostFacts struct {
	Name      string           `json:"name"`
	CPUs      int              `json:"cpus"`
	MemoryKiB uint64           `json:"memoryKiB"`
	Machines  []Machine        `json:"machines"`
	Pools     []Pool           `json:"pools"`
	Backups   []Backup         `json:"backups"`
	Schedules []BackupSchedule `json:"schedules"`
}

type Machine struct {
	UUID      string   `json:"uuid"`
	Name      string   `json:"name"`
	State     string   `json:"state"`
	Owner     string   `json:"owner,omitempty"`
	VCPUs     int      `json:"vcpus"`
	MemoryMiB uint64   `json:"memoryMiB"`
	DiskBytes uint64   `json:"diskBytes"` // capacity of its disks (CD-ROMs not counted)
	Disks     int      `json:"disks"`
	Addresses []string `json:"addresses,omitempty"`
	Autostart bool     `json:"autostart"`
	Template  bool     `json:"template"`
}

type Pool struct {
	Name            string `json:"name"`
	Type            string `json:"type"`
	Path            string `json:"path,omitempty"`
	Active          bool   `json:"active"`
	CapacityBytes   uint64 `json:"capacityBytes"`
	AllocationBytes uint64 `json:"allocationBytes"`
	AvailableBytes  uint64 `json:"availableBytes"`
}

// Shared pools (NFS, Ceph, iSCSI, Gluster) appear on several members but hold one capacity.
func (p Pool) shared() bool {
	switch p.Type {
	case "netfs", "rbd", "iscsi", "iscsi-direct", "gluster":
		return true
	}
	return false
}

type Backup struct {
	DomainUUID string    `json:"domainUuid"`
	Created    time.Time `json:"created"`
	SizeBytes  uint64    `json:"sizeBytes"`
}

// BackupSchedule is how often a machine is backed up automatically.
type BackupSchedule struct {
	DomainUUID string        `json:"domainUuid"`
	Interval   time.Duration `json:"interval"`
}

// ---- Store --------------------------------------------------------------------------------

type Store struct {
	dir string
	mu  sync.Mutex
	// meta is newest first.
	meta      []Meta
	schedules []Schedule
	now       func() time.Time
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, now: time.Now}
	for name, v := range map[string]any{"reports.json": &s.meta, "schedules.json": &s.schedules} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, v); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
	}
	return s, nil
}

func (s *Store) write(name string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	p := filepath.Join(s.dir, name)
	if err := os.WriteFile(p+".tmp", b, 0o600); err != nil {
		return err
	}
	return os.Rename(p+".tmp", p)
}

// List is every stored report, newest first.
func (s *Store) List() []Meta {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Meta{}, s.meta...)
}

var validID = regexp.MustCompile(`^[a-z0-9-]+$`)

func (s *Store) file(m Meta) string { return filepath.Join(s.dir, m.ID+"."+string(m.Format)) }

// Get returns a report and its content.
func (s *Store) Get(id string) (Meta, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.meta {
		if m.ID == id && validID.MatchString(id) {
			b, err := os.ReadFile(s.file(m))
			return m, b, err
		}
	}
	return Meta{}, nil, os.ErrNotExist
}

// Delete removes a report.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, m := range s.meta {
		if m.ID == id {
			_ = os.Remove(s.file(m))
			s.meta = append(s.meta[:i], s.meta[i+1:]...)
			return s.write("reports.json", s.meta)
		}
	}
	return os.ErrNotExist
}

// Generate writes a report from facts and stores it.
func (s *Store) Generate(kind Kind, format Format, f Facts, by, scheduleID string) (Meta, error) {
	if _, ok := kindTitles[kind]; !ok {
		return Meta{}, fmt.Errorf("unknown report %q", kind)
	}
	if !formats[format] {
		return Meta{}, fmt.Errorf("unknown format %q", format)
	}
	now := s.now()
	m := Meta{Kind: kind, Format: format, GeneratedAt: now.UTC(), By: by, ScheduleID: scheduleID,
		ID: fmt.Sprintf("%s-%s-%s", strings.ReplaceAll(string(kind), "_", "-"), now.Format("20060102-150405"), randomHex(3))}
	m.Title = fmt.Sprintf("%s — %s", kindTitles[kind], now.Format("2006-01-02 15:04"))
	var body []byte
	var err error
	switch kind {
	case Capacity:
		m.Summary, body, err = capacity(f, format, m.Title)
	case Inventory:
		m.Summary, body, err = inventory(f, format, m.Title)
	case BackupSLA:
		m.Summary, body, err = backupSLA(f, format, m.Title, now)
	}
	if err != nil {
		return Meta{}, err
	}
	if len(f.Missing) > 0 {
		m.Summary += " · not reachable: " + strings.Join(f.Missing, ", ")
	}
	m.SizeBytes = int64(len(body))
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.WriteFile(s.file(m), body, 0o600); err != nil {
		return Meta{}, err
	}
	s.meta = append([]Meta{m}, s.meta...)
	return m, s.write("reports.json", s.meta)
}

// ---- Schedules ----------------------------------------------------------------------------

// Schedules lists the report schedules with their next run.
func (s *Store) Schedules() []Schedule {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]Schedule{}, s.schedules...)
	for i := range out {
		if out[i].Enabled {
			n := out[i].Next(s.now())
			out[i].NextRun = &n
		}
	}
	return out
}

// PutSchedule adds (no ID) or replaces a schedule.
func (s *Store) PutSchedule(sc Schedule) (Schedule, error) {
	if err := sc.validate(); err != nil {
		return Schedule{}, err
	}
	sc.NextRun = nil
	s.mu.Lock()
	defer s.mu.Unlock()
	if sc.ID == "" {
		sc.ID = fmt.Sprintf("rs-%d", s.now().UnixNano())
		s.schedules = append(s.schedules, sc)
	} else {
		found := false
		for i := range s.schedules {
			if s.schedules[i].ID == sc.ID {
				sc.LastRun, sc.LastError = s.schedules[i].LastRun, s.schedules[i].LastError
				s.schedules[i], found = sc, true
			}
		}
		if !found {
			return Schedule{}, os.ErrNotExist
		}
	}
	return sc, s.write("schedules.json", s.schedules)
}

func (s *Store) DeleteSchedule(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.schedules {
		if s.schedules[i].ID == id {
			s.schedules = append(s.schedules[:i], s.schedules[i+1:]...)
			return s.write("schedules.json", s.schedules)
		}
	}
	return os.ErrNotExist
}

// Due returns the enabled schedules whose next run after their last run (or after since,
// when they never ran) has come.
func (s *Store) Due(since time.Time) []Schedule {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	var out []Schedule
	for _, sc := range s.schedules {
		from := since
		if sc.LastRun != nil {
			from = *sc.LastRun
		}
		if sc.Enabled && !sc.Next(from).After(now) {
			out = append(out, sc)
		}
	}
	return out
}

// Ran records a schedule's run and deletes its reports older than KeepDays.
func (s *Store) Ran(id string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for i := range s.schedules {
		sc := &s.schedules[i]
		if sc.ID != id {
			continue
		}
		sc.LastRun, sc.LastError = &now, ""
		if err != nil {
			sc.LastError = err.Error()
		}
		if sc.KeepDays > 0 {
			cut := now.AddDate(0, 0, -sc.KeepDays)
			kept := s.meta[:0]
			for _, m := range s.meta {
				if m.ScheduleID == id && m.GeneratedAt.Before(cut) {
					_ = os.Remove(s.file(m))
					continue
				}
				kept = append(kept, m)
			}
			s.meta = kept
			_ = s.write("reports.json", s.meta)
		}
	}
	_ = s.write("schedules.json", s.schedules)
}

// ---- Report bodies ------------------------------------------------------------------------

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func gib(b uint64) string { return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30)) }

func pct(a, b float64) string {
	if b <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", a/b*100)
}

type capRow struct {
	Host                                    string
	CPUs, VCPUs, VCPUsRunning, Machines, On int
	MemKiB, MemAllocKiB, MemRunningKiB      uint64
}

// CapacityData is the capacity report's JSON form.
type CapacityData struct {
	Hosts []capRow `json:"hosts"`
	Total capRow   `json:"total"`
	Pools []Pool   `json:"pools"`
}

func capacity(f Facts, format Format, title string) (string, []byte, error) {
	var d CapacityData
	d.Total.Host = "Total"
	seenShared := map[string]bool{}
	for _, h := range f.Hosts {
		r := capRow{Host: h.Name, CPUs: h.CPUs, MemKiB: h.MemoryKiB}
		for _, m := range h.Machines {
			if m.Template {
				continue
			}
			r.Machines++
			r.VCPUs += m.VCPUs
			r.MemAllocKiB += m.MemoryMiB << 10
			if m.State == "running" || m.State == "paused" || m.State == "blocked" {
				r.On++
				r.VCPUsRunning += m.VCPUs
				r.MemRunningKiB += m.MemoryMiB << 10
			}
		}
		d.Hosts = append(d.Hosts, r)
		t := &d.Total
		t.CPUs, t.VCPUs, t.VCPUsRunning, t.Machines, t.On = t.CPUs+r.CPUs, t.VCPUs+r.VCPUs, t.VCPUsRunning+r.VCPUsRunning, t.Machines+r.Machines, t.On+r.On
		t.MemKiB, t.MemAllocKiB, t.MemRunningKiB = t.MemKiB+r.MemKiB, t.MemAllocKiB+r.MemAllocKiB, t.MemRunningKiB+r.MemRunningKiB
		for _, p := range h.Pools {
			if !p.Active {
				continue
			}
			key := h.Name + "/" + p.Name
			if p.shared() {
				key = p.Type + ":" + p.Name + ":" + strconv.FormatUint(p.CapacityBytes, 10)
				if seenShared[key] {
					continue
				}
				seenShared[key] = true
			} else {
				p.Name = h.Name + " / " + p.Name
			}
			d.Pools = append(d.Pools, p)
		}
	}
	var poolCap, poolUsed uint64
	for _, p := range d.Pools {
		poolCap += p.CapacityBytes
		poolUsed += p.AllocationBytes
	}
	t := d.Total
	summary := fmt.Sprintf("%d hosts · %d machines (%d on) · vCPU %d on %d CPUs · memory %s of %s allocated · storage %s of %s used",
		len(d.Hosts), t.Machines, t.On, t.VCPUs, t.CPUs, gib(t.MemAllocKiB<<10), gib(t.MemKiB<<10), gib(poolUsed), gib(poolCap))
	switch format {
	case JSON:
		b, err := json.MarshalIndent(d, "", "  ")
		return summary, b, err
	case CSV:
		var buf bytes.Buffer
		w := csv.NewWriter(&buf)
		_ = w.Write([]string{"host", "cpus", "vcpus_allocated", "vcpus_running", "memory_gib", "memory_allocated_gib", "memory_running_gib", "machines", "machines_on"})
		for _, r := range append(d.Hosts, d.Total) {
			_ = w.Write([]string{r.Host, strconv.Itoa(r.CPUs), strconv.Itoa(r.VCPUs), strconv.Itoa(r.VCPUsRunning),
				fmt.Sprintf("%.1f", float64(r.MemKiB)/(1<<20)), fmt.Sprintf("%.1f", float64(r.MemAllocKiB)/(1<<20)),
				fmt.Sprintf("%.1f", float64(r.MemRunningKiB)/(1<<20)), strconv.Itoa(r.Machines), strconv.Itoa(r.On)})
		}
		_ = w.Write(nil)
		_ = w.Write([]string{"storage_pool", "type", "capacity_gib", "used_gib", "free_gib"})
		for _, p := range d.Pools {
			_ = w.Write([]string{p.Name, p.Type, fmt.Sprintf("%.1f", float64(p.CapacityBytes)/(1<<30)),
				fmt.Sprintf("%.1f", float64(p.AllocationBytes)/(1<<30)), fmt.Sprintf("%.1f", float64(p.AvailableBytes)/(1<<30))})
		}
		w.Flush()
		return summary, buf.Bytes(), w.Error()
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<div class="stat-grid">
<div class="stat-box"><div class="label">Machines</div><div class="val">%d</div><div class="sub">%d on</div></div>
<div class="stat-box"><div class="label">vCPU : CPU</div><div class="val">%d : %d</div><div class="sub">%s overcommit · %d on running machines</div></div>
<div class="stat-box"><div class="label">Memory allocated</div><div class="val">%s</div><div class="sub">of %s · %s on running machines</div></div>
<div class="stat-box"><div class="label">Storage used</div><div class="val">%s</div><div class="sub">of %s (%s)</div></div>
</div>`, t.Machines, t.On, t.VCPUs, t.CPUs, ratio(t.VCPUs, t.CPUs), t.VCPUsRunning,
		gib(t.MemAllocKiB<<10), gib(t.MemKiB<<10), gib(t.MemRunningKiB<<10), gib(poolUsed), gib(poolCap), pct(float64(poolUsed), float64(poolCap)))
	b.WriteString(`<h3>Hosts</h3><table class="report-table"><thead><tr><th>Host</th><th>CPUs</th><th>vCPUs (running)</th><th>Memory</th><th>Allocated (running)</th><th>Machines (on)</th></tr></thead><tbody>`)
	for _, r := range d.Hosts {
		fmt.Fprintf(&b, `<tr><td><strong>%s</strong></td><td class="mono">%d</td><td class="mono">%d (%d) · %s</td><td class="mono">%s</td><td class="mono">%s (%s) · %s</td><td class="mono">%d (%d)</td></tr>`,
			html.EscapeString(r.Host), r.CPUs, r.VCPUs, r.VCPUsRunning, ratio(r.VCPUs, r.CPUs), gib(r.MemKiB<<10),
			gib(r.MemAllocKiB<<10), gib(r.MemRunningKiB<<10), pct(float64(r.MemAllocKiB), float64(r.MemKiB)), r.Machines, r.On)
	}
	b.WriteString(`</tbody></table><h3>Storage pools</h3><table class="report-table"><thead><tr><th>Pool</th><th>Type</th><th>Capacity</th><th>Used</th><th>Free</th></tr></thead><tbody>`)
	for _, p := range d.Pools {
		cls := ""
		if p.CapacityBytes > 0 && float64(p.AllocationBytes)/float64(p.CapacityBytes) > 0.85 {
			cls = ` class="warn"`
		}
		fmt.Fprintf(&b, `<tr%s><td><strong>%s</strong></td><td>%s</td><td class="mono">%s</td><td class="mono">%s (%s)</td><td class="mono">%s</td></tr>`,
			cls, html.EscapeString(p.Name), html.EscapeString(p.Type), gib(p.CapacityBytes), gib(p.AllocationBytes),
			pct(float64(p.AllocationBytes), float64(p.CapacityBytes)), gib(p.AvailableBytes))
	}
	b.WriteString(`</tbody></table>`)
	b.WriteString(missingNote(f))
	return summary, []byte(page(title, summary, b.String())), nil
}

func ratio(v, c int) string {
	if c == 0 {
		return "—"
	}
	return fmt.Sprintf("%.1f:1", float64(v)/float64(c))
}

func missingNote(f Facts) string {
	if len(f.Missing) == 0 {
		return ""
	}
	return `<p class="note">Not included (did not answer): ` + html.EscapeString(strings.Join(f.Missing, ", ")) + `</p>`
}

// InventoryItem is one machine of the inventory report.
type InventoryItem struct {
	Machine
	Host string `json:"host"`
}

func inventory(f Facts, format Format, title string) (string, []byte, error) {
	var items []InventoryItem
	for _, h := range f.Hosts {
		for _, m := range h.Machines {
			items = append(items, InventoryItem{Machine: m, Host: h.Name})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Name != items[j].Name {
			return items[i].Name < items[j].Name
		}
		return items[i].Host < items[j].Host
	})
	on := 0
	for _, it := range items {
		if it.State == "running" {
			on++
		}
	}
	summary := fmt.Sprintf("%d machines on %d hosts, %d running", len(items), len(f.Hosts), on)
	switch format {
	case JSON:
		b, err := json.MarshalIndent(items, "", "  ")
		return summary, b, err
	case CSV:
		var buf bytes.Buffer
		w := csv.NewWriter(&buf)
		_ = w.Write([]string{"name", "uuid", "host", "state", "vcpus", "memory_mib", "disk_gib", "disks", "addresses", "owner", "autostart", "template"})
		for _, it := range items {
			_ = w.Write([]string{it.Name, it.UUID, it.Host, it.State, strconv.Itoa(it.VCPUs), strconv.FormatUint(it.MemoryMiB, 10),
				fmt.Sprintf("%.1f", float64(it.DiskBytes)/(1<<30)), strconv.Itoa(it.Disks), strings.Join(it.Addresses, " "),
				it.Owner, strconv.FormatBool(it.Autostart), strconv.FormatBool(it.Template)})
		}
		w.Flush()
		return summary, buf.Bytes(), w.Error()
	}
	var b strings.Builder
	b.WriteString(`<table class="report-table"><thead><tr><th>Machine</th><th>State</th><th>Host</th><th>vCPUs</th><th>Memory</th><th>Disks</th><th>Addresses</th><th>Owner</th></tr></thead><tbody>`)
	for _, it := range items {
		name := html.EscapeString(it.Name)
		if it.Template {
			name += ` <span class="badge">TEMPLATE</span>`
		}
		fmt.Fprintf(&b, `<tr><td><strong>%s</strong><br><small class="mono text-dim">%s</small></td><td><span class="badge %s">%s</span></td><td>%s</td><td class="mono">%d</td><td class="mono">%d MiB</td><td class="mono">%s (%d)</td><td class="mono">%s</td><td>%s</td></tr>`,
			name, html.EscapeString(it.UUID), stateClass(it.State), html.EscapeString(strings.ToUpper(it.State)), html.EscapeString(it.Host),
			it.VCPUs, it.MemoryMiB, gib(it.DiskBytes), it.Disks, html.EscapeString(strings.Join(it.Addresses, ", ")), html.EscapeString(it.Owner))
	}
	b.WriteString(`</tbody></table>`)
	b.WriteString(missingNote(f))
	return summary, []byte(page(title, summary, b.String())), nil
}

func stateClass(s string) string {
	switch s {
	case "running":
		return "ok"
	case "paused", "pmsuspended":
		return "warn"
	case "crashed":
		return "danger"
	}
	return ""
}

// SLAItem is one machine of the backup compliance report.
type SLAItem struct {
	Machine    string     `json:"machine"`
	UUID       string     `json:"uuid"`
	Host       string     `json:"host"`
	Status     string     `json:"status"` // ok, late, overdue, never, unscheduled
	LastBackup *time.Time `json:"lastBackup,omitempty"`
	AgeHours   float64    `json:"ageHours,omitempty"`
	Backups    int        `json:"backups"`
	SizeBytes  uint64     `json:"sizeBytes"` // all of its backups
	// Interval is its backup schedule ("" without one).
	Interval string `json:"interval,omitempty"`
}

// slaStatus: a machine with a backup schedule is "ok" while its newest backup is younger
// than 1.5 intervals, "late" up to 3, then "overdue"; "never" if it has none at all.
func slaStatus(interval time.Duration, last *time.Time, now time.Time) string {
	switch {
	case interval == 0:
		return "unscheduled"
	case last == nil:
		return "never"
	case now.Sub(*last) <= interval*3/2:
		return "ok"
	case now.Sub(*last) <= interval*3:
		return "late"
	}
	return "overdue"
}

func backupSLA(f Facts, format Format, title string, now time.Time) (string, []byte, error) {
	var items []SLAItem
	counts := map[string]int{}
	for _, h := range f.Hosts {
		every := map[string]time.Duration{}
		for _, sc := range h.Schedules {
			if cur, ok := every[sc.DomainUUID]; !ok || sc.Interval < cur {
				every[sc.DomainUUID] = sc.Interval
			}
		}
		for _, m := range h.Machines {
			if m.Template {
				continue
			}
			it := SLAItem{Machine: m.Name, UUID: m.UUID, Host: h.Name}
			for _, b := range h.Backups {
				if !strings.EqualFold(b.DomainUUID, m.UUID) {
					continue
				}
				it.Backups++
				it.SizeBytes += b.SizeBytes
				if it.LastBackup == nil || b.Created.After(*it.LastBackup) {
					t := b.Created
					it.LastBackup = &t
				}
			}
			if it.LastBackup != nil {
				it.AgeHours = float64(now.Sub(*it.LastBackup).Round(time.Minute)) / float64(time.Hour)
			}
			iv := every[strings.ToLower(m.UUID)]
			if iv > 0 {
				it.Interval = iv.String()
			}
			it.Status = slaStatus(iv, it.LastBackup, now)
			counts[it.Status]++
			items = append(items, it)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Machine < items[j].Machine })
	scheduled := len(items) - counts["unscheduled"]
	summary := fmt.Sprintf("%d of %d scheduled machines within their backup interval · %d late · %d overdue · %d never backed up · %d without a backup schedule",
		counts["ok"], scheduled, counts["late"], counts["overdue"], counts["never"], counts["unscheduled"])
	switch format {
	case JSON:
		b, err := json.MarshalIndent(items, "", "  ")
		return summary, b, err
	case CSV:
		var buf bytes.Buffer
		w := csv.NewWriter(&buf)
		_ = w.Write([]string{"machine", "uuid", "host", "status", "last_backup", "age_hours", "backups", "size_gib", "schedule_interval"})
		for _, it := range items {
			last := ""
			if it.LastBackup != nil {
				last = it.LastBackup.UTC().Format(time.RFC3339)
			}
			_ = w.Write([]string{it.Machine, it.UUID, it.Host, it.Status, last, fmt.Sprintf("%.1f", it.AgeHours),
				strconv.Itoa(it.Backups), fmt.Sprintf("%.2f", float64(it.SizeBytes)/(1<<30)), it.Interval})
		}
		w.Flush()
		return summary, buf.Bytes(), w.Error()
	}
	var b strings.Builder
	b.WriteString(`<table class="report-table"><thead><tr><th>Machine</th><th>Status</th><th>Last backup</th><th>Backups</th><th>Schedule</th><th>Host</th></tr></thead><tbody>`)
	cls := map[string]string{"ok": "ok", "late": "warn", "overdue": "danger", "never": "danger", "unscheduled": ""}
	for _, it := range items {
		last := "—"
		if it.LastBackup != nil {
			last = it.LastBackup.Local().Format("2006-01-02 15:04") + fmt.Sprintf(" (%.0f h ago)", it.AgeHours)
		}
		fmt.Fprintf(&b, `<tr><td><strong>%s</strong></td><td><span class="badge %s">%s</span></td><td class="mono">%s</td><td class="mono">%d · %s</td><td class="mono">%s</td><td>%s</td></tr>`,
			html.EscapeString(it.Machine), cls[it.Status], strings.ToUpper(it.Status), last, it.Backups, gib(it.SizeBytes),
			html.EscapeString(orDash(it.Interval)), html.EscapeString(it.Host))
	}
	b.WriteString(`</tbody></table><p class="note">OK: newest backup younger than 1.5× the machine's backup interval · late: up to 3× · overdue: older.</p>`)
	b.WriteString(missingNote(f))
	return summary, []byte(page(title, summary, b.String())), nil
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// page wraps a report body in a self-contained HTML document that prints well.
func page(title, summary, body string) string {
	return `<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><title>` + html.EscapeString(title) + `</title>
<style>
  body { margin: 0; padding: 32px; font: 13px/1.5 "IBM Plex Sans", system-ui, sans-serif; background: #f7f5f0; color: #18191c; }
  .head { border-bottom: 3px solid #c25e0e; padding-bottom: 14px; margin-bottom: 22px; display: flex; justify-content: space-between; align-items: flex-end; gap: 16px; }
  h1 { margin: 0 0 4px; font-size: 20px; text-transform: uppercase; letter-spacing: .02em; }
  h3 { margin: 24px 0 8px; font-size: 13px; text-transform: uppercase; }
  .summary { color: #4a4d54; }
  .tag { font: 11px ui-monospace, monospace; border: 1px solid #18191c; padding: 3px 8px; white-space: nowrap; }
  .stat-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); gap: 12px; margin-bottom: 8px; }
  .stat-box { border: 1px solid #bdb7ac; background: #fff; padding: 12px 14px; }
  .label { font: 11px ui-monospace, monospace; text-transform: uppercase; color: #4a4d54; }
  .val { font: 700 20px ui-monospace, monospace; margin: 2px 0; }
  .sub { font-size: 11px; color: #4a4d54; }
  table { width: 100%; border-collapse: collapse; background: #fff; border: 1px solid #bdb7ac; }
  th { text-align: left; padding: 8px 10px; font: 11px ui-monospace, monospace; text-transform: uppercase; background: #e5e0d8; border-bottom: 1px solid #bdb7ac; }
  td { padding: 8px 10px; border-bottom: 1px solid #e5e0d8; vertical-align: top; }
  tr.warn td { background: #fdf0e1; }
  .mono { font-family: ui-monospace, monospace; }
  .text-dim { color: #85827a; }
  .badge { display: inline-block; padding: 1px 6px; font: 700 10px ui-monospace, monospace; border: 1px solid currentColor; }
  .badge.ok { color: #1b6e47; } .badge.warn { color: #b35e09; } .badge.danger { color: #a32626; }
  .note { color: #4a4d54; font-size: 12px; margin-top: 12px; }
  @media print { body { padding: 0; background: #fff; } }
</style></head><body>
<div class="head"><div><h1>` + html.EscapeString(title) + `</h1><div class="summary">` + html.EscapeString(summary) + `</div></div><div class="tag">VirtoMir report</div></div>
` + body + `
</body></html>`
}
