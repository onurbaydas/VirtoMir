// Package reports implements Automated Scheduled Audits & Compliance Reports (Roadmap Phase C.11).
package reports

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type ReportType string

const (
	TypeCapacity           ReportType = "capacity"
	TypeInventory          ReportType = "inventory"
	TypeBackupSLA          ReportType = "backup_sla"
	TypeAuditActivity      ReportType = "audit_activity"
	TypeSecurityCompliance ReportType = "security_compliance"
)

type Format string

const (
	FormatHTML Format = "html"
	FormatCSV  Format = "csv"
	FormatJSON Format = "json"
)

// ReportMetadata represents one generated report.
type ReportMetadata struct {
	ID          string     `json:"id"`
	Type        ReportType `json:"type"`
	Title       string     `json:"title"`
	Format      Format     `json:"format"`
	GeneratedAt time.Time  `json:"generatedAt"`
	TimeRange   string     `json:"timeRange"`
	SizeBytes   int64      `json:"sizeBytes"`
	Summary     string     `json:"summary"`
}

// Schedule defines an automated report cadence.
type Schedule struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Type            ReportType `json:"type"`
	Format          Format     `json:"format"`
	Cadence         string     `json:"cadence"` // "daily", "weekly", "monthly"
	Enabled         bool       `json:"enabled"`
	DeliveryWebhook string     `json:"deliveryWebhook,omitempty"`
	DeliveryEmail   string     `json:"deliveryEmail,omitempty"`
	KeepDays        int        `json:"keepDays"`
	LastRun         time.Time  `json:"lastRun,omitempty"`
}

// Data models for reports
type CapacityData struct {
	ClusterName      string  `json:"clusterName"`
	TotalHosts       int     `json:"totalHosts"`
	TotalVCPUs       int     `json:"totalVcpus"`
	AllocatedVCPUs   int     `json:"allocatedVcpus"`
	TotalMemoryGiB   float64 `json:"totalMemoryGiB"`
	AllocatedMemGiB  float64 `json:"allocatedMemGiB"`
	TotalStorageGiB  float64 `json:"totalStorageGiB"`
	AllocatedDiskGiB float64 `json:"allocatedDiskGiB"`
	RunningVMs       int     `json:"runningVms"`
	TotalVMs         int     `json:"totalVms"`
	ForecastDays     int     `json:"forecastDays"`
}

type InventoryItem struct {
	UUID        string `json:"uuid"`
	Name        string `json:"name"`
	State       string `json:"state"`
	Host        string `json:"host"`
	VCPUs       int    `json:"vcpus"`
	MemoryMiB   uint64 `json:"memoryMiB"`
	DisksGiB    int    `json:"disksGiB"`
	IPAddresses string `json:"ipAddresses"`
	OS          string `json:"os"`
}

type BackupSLAItem struct {
	VMName       string    `json:"vmName"`
	LastBackup   time.Time `json:"lastBackup"`
	Status       string    `json:"status"` // "compliant", "warning", "overdue", "no_backup"
	BackupSizeMB int64     `json:"backupSizeMb"`
	Target       string    `json:"target"`
}

type Store struct {
	dir       string
	metaPath  string
	schedPath string
	mu        sync.Mutex
	reports   map[string]ReportMetadata
	schedules map[string]Schedule
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	s := &Store{
		dir:       dir,
		metaPath:  filepath.Join(dir, "reports_meta.json"),
		schedPath: filepath.Join(dir, "report_schedules.json"),
		reports:   make(map[string]ReportMetadata),
		schedules: make(map[string]Schedule),
	}

	// Load metadata
	if raw, err := os.ReadFile(s.metaPath); err == nil {
		var list []ReportMetadata
		if json.Unmarshal(raw, &list) == nil {
			for _, m := range list {
				s.reports[m.ID] = m
			}
		}
	}

	// Load schedules
	if raw, err := os.ReadFile(s.schedPath); err == nil {
		var list []Schedule
		if json.Unmarshal(raw, &list) == nil {
			for _, sc := range list {
				s.schedules[sc.ID] = sc
			}
		}
	} else if errors.Is(err, fs.ErrNotExist) {
		// Provide default schedules
		defaultSched := Schedule{
			ID:       "sched-weekly-capacity",
			Name:     "Weekly Cluster Capacity & Runway Audit",
			Type:     TypeCapacity,
			Format:   FormatHTML,
			Cadence:  "weekly",
			Enabled:  true,
			KeepDays: 30,
		}
		s.schedules[defaultSched.ID] = defaultSched
		_ = s.saveSchedules()
	}

	return s, nil
}

func (s *Store) saveMeta() error {
	var list []ReportMetadata
	for _, m := range s.reports {
		list = append(list, m)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].GeneratedAt.After(list[j].GeneratedAt)
	})
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.metaPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.metaPath)
}

func (s *Store) saveSchedules() error {
	var list []Schedule
	for _, sc := range s.schedules {
		list = append(list, sc)
	}
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.schedPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.schedPath)
}

func (s *Store) List() []ReportMetadata {
	s.mu.Lock()
	defer s.mu.Unlock()

	var list []ReportMetadata
	for _, m := range s.reports {
		list = append(list, m)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].GeneratedAt.After(list[j].GeneratedAt)
	})
	return list
}

func (s *Store) GetContent(id string) ([]byte, ReportMetadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	m, ok := s.reports[id]
	if !ok {
		return nil, ReportMetadata{}, fmt.Errorf("report %q not found", id)
	}
	path := filepath.Join(s.dir, id+"."+string(m.Format))
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, m, err
	}
	return data, m, nil
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	m, ok := s.reports[id]
	if !ok {
		return fmt.Errorf("report %q not found", id)
	}
	path := filepath.Join(s.dir, id+"."+string(m.Format))
	_ = os.Remove(path)
	delete(s.reports, id)
	return s.saveMeta()
}

func (s *Store) ListSchedules() []Schedule {
	s.mu.Lock()
	defer s.mu.Unlock()

	var list []Schedule
	for _, sc := range s.schedules {
		list = append(list, sc)
	}
	return list
}

func (s *Store) SaveSchedule(sc Schedule) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if sc.ID == "" {
		sc.ID = fmt.Sprintf("sched-%d", time.Now().UnixNano())
	}
	if sc.KeepDays <= 0 {
		sc.KeepDays = 30
	}
	s.schedules[sc.ID] = sc
	return s.saveSchedules()
}

// GenerateCapacityReport generates a cluster capacity audit report.
func (s *Store) GenerateCapacityReport(data CapacityData, fmtChoice Format) (ReportMetadata, error) {
	id := fmt.Sprintf("report-cap-%d", time.Now().UnixNano())
	title := fmt.Sprintf("Cluster Capacity & Runway Audit — %s", time.Now().Format("2006-01-02"))
	summary := fmt.Sprintf("%d VMs (%d running) · CPU: %d/%d vCPUs · RAM: %.1f/%.1f GiB",
		data.TotalVMs, data.RunningVMs, data.AllocatedVCPUs, data.TotalVCPUs, data.AllocatedMemGiB, data.TotalMemoryGiB)

	var payload []byte
	switch fmtChoice {
	case FormatCSV:
		var buf bytes.Buffer
		w := csv.NewWriter(&buf)
		_ = w.Write([]string{"Metric", "Value"})
		_ = w.Write([]string{"Cluster Name", data.ClusterName})
		_ = w.Write([]string{"Total Hosts", fmt.Sprintf("%d", data.TotalHosts)})
		_ = w.Write([]string{"Total VMs", fmt.Sprintf("%d", data.TotalVMs)})
		_ = w.Write([]string{"Running VMs", fmt.Sprintf("%d", data.RunningVMs)})
		_ = w.Write([]string{"Total vCPUs", fmt.Sprintf("%d", data.TotalVCPUs)})
		_ = w.Write([]string{"Allocated vCPUs", fmt.Sprintf("%d", data.AllocatedVCPUs)})
		_ = w.Write([]string{"Total Memory GiB", fmt.Sprintf("%.2f", data.TotalMemoryGiB)})
		_ = w.Write([]string{"Allocated Memory GiB", fmt.Sprintf("%.2f", data.AllocatedMemGiB)})
		_ = w.Write([]string{"Total Storage GiB", fmt.Sprintf("%.2f", data.TotalStorageGiB)})
		_ = w.Write([]string{"Allocated Disk GiB", fmt.Sprintf("%.2f", data.AllocatedDiskGiB)})
		_ = w.Write([]string{"Estimated Capacity Runway (Days)", fmt.Sprintf("%d", data.ForecastDays)})
		w.Flush()
		payload = buf.Bytes()

	case FormatJSON:
		payload, _ = json.MarshalIndent(data, "", "  ")

	case FormatHTML:
		payload = []byte(renderSwissHTML(title, summary, fmt.Sprintf(`
			<div class="stat-grid">
				<div class="stat-box"><div class="label">TOTAL VMS</div><div class="val">%d</div><div class="sub">%d active</div></div>
				<div class="stat-box"><div class="label">VCPU COMMITMENT</div><div class="val">%d / %d</div><div class="sub">%.1f%% allocated</div></div>
				<div class="stat-box"><div class="label">RAM ALLOCATION</div><div class="val">%.1f / %.1f GiB</div><div class="sub">%.1f%% allocated</div></div>
				<div class="stat-box"><div class="label">STORAGE ALLOCATION</div><div class="val">%.1f / %.1f GiB</div><div class="sub">%.1f%% allocated</div></div>
			</div>
			<div class="panel">
				<h3>Capacity Runway Forecast</h3>
				<p>Based on linear workload expansion rates across the last 30 days, hardware saturation is estimated at approximately <strong>%d days</strong> under current growth trends.</p>
			</div>
		`, data.TotalVMs, data.RunningVMs,
			data.AllocatedVCPUs, data.TotalVCPUs, float64(data.AllocatedVCPUs)/float64(max(1, data.TotalVCPUs))*100,
			data.AllocatedMemGiB, data.TotalMemoryGiB, data.AllocatedMemGiB/maxFloat(1.0, data.TotalMemoryGiB)*100,
			data.AllocatedDiskGiB, data.TotalStorageGiB, data.AllocatedDiskGiB/maxFloat(1.0, data.TotalStorageGiB)*100,
			data.ForecastDays)))
	}

	return s.saveReport(id, TypeCapacity, title, fmtChoice, summary, payload)
}

// GenerateInventoryReport generates a full workload inventory report.
func (s *Store) GenerateInventoryReport(items []InventoryItem, fmtChoice Format) (ReportMetadata, error) {
	id := fmt.Sprintf("report-inv-%d", time.Now().UnixNano())
	title := fmt.Sprintf("Virtual Machine Fleet Inventory — %s", time.Now().Format("2006-01-02"))
	summary := fmt.Sprintf("%d guest virtual machines cataloged across cluster", len(items))

	var payload []byte
	switch fmtChoice {
	case FormatCSV:
		var buf bytes.Buffer
		w := csv.NewWriter(&buf)
		_ = w.Write([]string{"Name", "State", "Host", "vCPUs", "Memory (MiB)", "Disks (GiB)", "IPs", "OS", "UUID"})
		for _, item := range items {
			_ = w.Write([]string{
				item.Name, item.State, item.Host,
				fmt.Sprintf("%d", item.VCPUs),
				fmt.Sprintf("%d", item.MemoryMiB),
				fmt.Sprintf("%d", item.DisksGiB),
				item.IPAddresses, item.OS, item.UUID,
			})
		}
		w.Flush()
		payload = buf.Bytes()

	case FormatJSON:
		payload, _ = json.MarshalIndent(items, "", "  ")

	case FormatHTML:
		var rows strings.Builder
		for _, item := range items {
			rows.WriteString(fmt.Sprintf(`<tr>
				<td><strong>%s</strong><br><small class="mono text-dim">%s</small></td>
				<td><span class="badge %s">%s</span></td>
				<td>%s</td>
				<td class="mono">%d</td>
				<td class="mono">%d MiB</td>
				<td class="mono">%d GiB</td>
				<td class="mono">%s</td>
			</tr>`,
				html.EscapeString(item.Name), html.EscapeString(item.UUID),
				item.State, strings.ToUpper(item.State),
				html.EscapeString(item.Host),
				item.VCPUs, item.MemoryMiB, item.DisksGiB,
				html.EscapeString(item.IPAddresses)))
		}
		tableHTML := fmt.Sprintf(`
			<table class="report-table">
				<thead>
					<tr><th>VM NAME & UUID</th><th>STATE</th><th>NODE</th><th>VCPUS</th><th>MEMORY</th><th>STORAGE</th><th>IP ADDRESSES</th></tr>
				</thead>
				<tbody>%s</tbody>
			</table>
		`, rows.String())
		payload = []byte(renderSwissHTML(title, summary, tableHTML))
	}

	return s.saveReport(id, TypeInventory, title, fmtChoice, summary, payload)
}

// GenerateBackupSLAReport generates a backup compliance report.
func (s *Store) GenerateBackupSLAReport(items []BackupSLAItem, fmtChoice Format) (ReportMetadata, error) {
	id := fmt.Sprintf("report-sla-%d", time.Now().UnixNano())
	title := fmt.Sprintf("Backup SLA & Disaster Recovery Compliance — %s", time.Now().Format("2006-01-02"))

	compliant := 0
	for _, it := range items {
		if it.Status == "compliant" {
			compliant++
		}
	}
	summary := fmt.Sprintf("%d/%d VMs compliant with backup retention SLAs (%.1f%%)",
		compliant, len(items), float64(compliant)/float64(max(1, len(items)))*100)

	var payload []byte
	switch fmtChoice {
	case FormatCSV:
		var buf bytes.Buffer
		w := csv.NewWriter(&buf)
		_ = w.Write([]string{"VM Name", "Status", "Last Backup", "Size (MB)", "Target Storage"})
		for _, item := range items {
			_ = w.Write([]string{
				item.VMName, item.Status, item.LastBackup.Format(time.RFC3339),
				fmt.Sprintf("%d", item.BackupSizeMB), item.Target,
			})
		}
		w.Flush()
		payload = buf.Bytes()

	case FormatJSON:
		payload, _ = json.MarshalIndent(items, "", "  ")

	case FormatHTML:
		var rows strings.Builder
		for _, item := range items {
			badgeClass := "ok"
			if item.Status == "warning" {
				badgeClass = "warn"
			} else if item.Status == "overdue" || item.Status == "no_backup" {
				badgeClass = "danger"
			}
			rows.WriteString(fmt.Sprintf(`<tr>
				<td><strong>%s</strong></td>
				<td><span class="badge %s">%s</span></td>
				<td class="mono">%s</td>
				<td class="mono">%d MB</td>
				<td>%s</td>
			</tr>`,
				html.EscapeString(item.VMName), badgeClass, strings.ToUpper(item.Status),
				item.LastBackup.Format("2006-01-02 15:04"),
				item.BackupSizeMB, html.EscapeString(item.Target)))
		}
		tableHTML := fmt.Sprintf(`
			<table class="report-table">
				<thead>
					<tr><th>VM WORKLOAD</th><th>SLA STATUS</th><th>LAST BACKUP</th><th>IMAGE SIZE</th><th>STORAGE TARGET</th></tr>
				</thead>
				<tbody>%s</tbody>
			</table>
		`, rows.String())
		payload = []byte(renderSwissHTML(title, summary, tableHTML))
	}

	return s.saveReport(id, TypeBackupSLA, title, fmtChoice, summary, payload)
}

func (s *Store) saveReport(id string, repType ReportType, title string, fmtChoice Format, summary string, payload []byte) (ReportMetadata, error) {
	path := filepath.Join(s.dir, id+"."+string(fmtChoice))
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		return ReportMetadata{}, err
	}

	meta := ReportMetadata{
		ID:          id,
		Type:        repType,
		Title:       title,
		Format:      fmtChoice,
		GeneratedAt: time.Now(),
		TimeRange:   "Latest Snapshot",
		SizeBytes:   int64(len(payload)),
		Summary:     summary,
	}

	s.mu.Lock()
	s.reports[id] = meta
	err := s.saveMeta()
	s.mu.Unlock()

	return meta, err
}

func renderSwissHTML(title, summary, body string) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>%s</title>
<style>
  :root {
    --bg: #121316;
    --surface: #181A1F;
    --border: #2E333D;
    --text: #E6E4DD;
    --text-dim: #8E95A5;
    --accent: #E55827;
    --ok: #2BBF76;
    --warn: #E5A93C;
    --danger: #E53935;
  }
  @media print {
    body { background: #fff !important; color: #000 !important; }
    .stat-box, .panel, table { border-color: #000 !important; }
  }
  * { box-sizing: border-box; }
  body {
    margin: 0;
    padding: 32px;
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, monospace;
    background: var(--bg);
    color: var(--text);
    font-size: 13px;
    line-height: 1.5;
  }
  .header {
    border-bottom: 2px solid var(--accent);
    padding-bottom: 16px;
    margin-bottom: 24px;
    display: flex;
    justify-content: space-between;
    align-items: flex-end;
  }
  h1 { margin: 0 0 6px 0; font-size: 20px; letter-spacing: -0.02em; font-weight: 700; text-transform: uppercase; }
  .summary { color: var(--text-dim); font-size: 13px; }
  .logo-tag { font-family: monospace; font-size: 11px; background: var(--surface); padding: 4px 8px; border: 1px solid var(--border); }
  .stat-grid {
    display: grid;
    grid-template-columns: repeat(4, 1fr);
    gap: 16px;
    margin-bottom: 24px;
  }
  .stat-box {
    background: var(--surface);
    border: 1px solid var(--border);
    padding: 16px;
  }
  .stat-box .label { font-size: 11px; color: var(--text-dim); text-transform: uppercase; font-family: monospace; }
  .stat-box .val { font-size: 22px; font-weight: 700; margin: 4px 0; font-family: monospace; }
  .stat-box .sub { font-size: 11px; color: var(--text-dim); }
  .panel {
    background: var(--surface);
    border: 1px solid var(--border);
    padding: 18px;
    margin-bottom: 24px;
  }
  .panel h3 { margin: 0 0 8px 0; font-size: 14px; text-transform: uppercase; }
  table.report-table {
    width: 100%%;
    border-collapse: collapse;
    background: var(--surface);
    border: 1px solid var(--border);
  }
  th {
    background: #1e2128;
    text-align: left;
    padding: 10px 14px;
    font-size: 11px;
    text-transform: uppercase;
    font-family: monospace;
    border-bottom: 1px solid var(--border);
  }
  td {
    padding: 10px 14px;
    border-bottom: 1px solid var(--border);
  }
  .mono { font-family: monospace; }
  .text-dim { color: var(--text-dim); }
  .badge {
    display: inline-block;
    padding: 2px 6px;
    font-size: 10px;
    font-weight: 700;
    font-family: monospace;
    border: 1px solid currentColor;
  }
  .badge.running, .badge.ok { color: var(--ok); }
  .badge.warn { color: var(--warn); }
  .badge.danger, .badge.shutoff { color: var(--danger); }
</style>
</head>
<body>
  <div class="header">
    <div>
      <h1>%s</h1>
      <div class="summary">%s</div>
    </div>
    <div class="logo-tag">VIRTOMIR AUDIT SUITE · SECURE REPORT</div>
  </div>
  %s
</body>
</html>`, html.EscapeString(title), html.EscapeString(title), html.EscapeString(summary), body)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
