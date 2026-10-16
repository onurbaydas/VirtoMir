package reports

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func facts(now time.Time) Facts {
	return Facts{
		Hosts: []HostFacts{
			{Name: "hv1", CPUs: 16, MemoryKiB: 64 << 20,
				Machines: []Machine{
					{UUID: "u-db", Name: "db", State: "running", VCPUs: 8, MemoryMiB: 16384, DiskBytes: 100 << 30, Disks: 1},
					{UUID: "u-web", Name: "web<script>", State: "shutoff", VCPUs: 2, MemoryMiB: 2048, DiskBytes: 20 << 30, Disks: 1},
					{UUID: "u-tpl", Name: "debian-tpl", State: "shutoff", VCPUs: 2, MemoryMiB: 2048, Template: true},
				},
				Pools: []Pool{
					{Name: "default", Type: "dir", Active: true, CapacityBytes: 500 << 30, AllocationBytes: 120 << 30, AvailableBytes: 380 << 30},
					{Name: "nfs", Type: "netfs", Active: true, CapacityBytes: 1 << 40, AllocationBytes: 1 << 39, AvailableBytes: 1 << 39},
				},
				Backups: []Backup{
					{DomainUUID: "u-db", Created: now.Add(-20 * time.Hour), SizeBytes: 10 << 30},
					{DomainUUID: "u-db", Created: now.Add(-44 * time.Hour), SizeBytes: 10 << 30},
				},
				Schedules: []BackupSchedule{{DomainUUID: "u-db", Interval: 24 * time.Hour}, {DomainUUID: "u-web", Interval: 24 * time.Hour}},
			},
			{Name: "hv2", CPUs: 8, MemoryKiB: 32 << 20,
				Machines:  []Machine{{UUID: "u-app", Name: "app", State: "running", VCPUs: 4, MemoryMiB: 8192}},
				Pools:     []Pool{{Name: "nfs", Type: "netfs", Active: true, CapacityBytes: 1 << 40, AllocationBytes: 1 << 39, AvailableBytes: 1 << 39}},
				Backups:   []Backup{{DomainUUID: "u-app", Created: now.Add(-100 * time.Hour)}},
				Schedules: []BackupSchedule{{DomainUUID: "u-app", Interval: 24 * time.Hour}},
			},
		},
		Missing: []string{"hv3"},
	}
}

func open(t *testing.T) (*Store, time.Time) {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	return s, now
}

func TestCapacityCountsSharedPoolsOnce(t *testing.T) {
	s, now := open(t)
	m, err := s.Generate(Capacity, JSON, facts(now), "onur", "")
	if err != nil {
		t.Fatal(err)
	}
	_, body, err := s.Get(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	var d CapacityData
	if err := json.Unmarshal(body, &d); err != nil {
		t.Fatal(err)
	}
	if d.Total.CPUs != 24 || d.Total.VCPUs != 14 || d.Total.VCPUsRunning != 12 || d.Total.Machines != 3 || d.Total.On != 2 {
		t.Errorf("total = %+v (templates must not count)", d.Total)
	}
	if len(d.Pools) != 2 {
		t.Errorf("pools = %+v (the NFS pool of both hosts is one)", d.Pools)
	}
	if !strings.Contains(m.Summary, "3 machines (2 on)") || !strings.Contains(m.Summary, "not reachable: hv3") {
		t.Errorf("summary = %q", m.Summary)
	}
}

func TestInventoryHTMLEscapes(t *testing.T) {
	s, now := open(t)
	m, err := s.Generate(Inventory, HTML, facts(now), "onur", "")
	if err != nil {
		t.Fatal(err)
	}
	_, body, _ := s.Get(m.ID)
	if strings.Contains(string(body), "<script>") || !strings.Contains(string(body), "web&lt;script&gt;") {
		t.Error("machine name not escaped")
	}
	if !strings.Contains(m.Summary, "4 machines on 2 hosts, 2 running") {
		t.Errorf("summary = %q", m.Summary)
	}
}

func TestBackupSLA(t *testing.T) {
	s, now := open(t)
	m, err := s.Generate(BackupSLA, JSON, facts(now), "onur", "")
	if err != nil {
		t.Fatal(err)
	}
	_, body, _ := s.Get(m.ID)
	var items []SLAItem
	if err := json.Unmarshal(body, &items); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, it := range items {
		got[it.Machine] = it.Status
	}
	want := map[string]string{"db": "ok", "web<script>": "never", "app": "overdue"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
	if _, ok := got["debian-tpl"]; ok {
		t.Error("template listed")
	}
	if slaStatus(24*time.Hour, ptr(now.Add(-50*time.Hour)), now) != "late" || slaStatus(0, nil, now) != "unscheduled" {
		t.Error("slaStatus thresholds")
	}
}

func ptr(t time.Time) *time.Time { return &t }

func TestSchedulesDueAndRetention(t *testing.T) {
	s, now := open(t)
	if _, err := s.PutSchedule(Schedule{Name: "x", Kind: "nope", Format: HTML, Every: "daily", At: "06:00"}); err == nil {
		t.Error("bad kind accepted")
	}
	sc, err := s.PutSchedule(Schedule{Name: "Weekly capacity", Kind: Capacity, Format: HTML, Every: "weekly", At: "06:00", Enabled: true, KeepDays: 30})
	if err != nil {
		t.Fatal(err)
	}
	// 2026-10-16 is a Friday: the next Monday 06:00 is the 19th.
	if n := sc.Next(now); n.Day() != 19 || n.Hour() != 6 || n.Weekday() != time.Monday {
		t.Errorf("next = %v", n)
	}
	if len(s.Due(now.Add(-time.Hour))) != 0 {
		t.Error("due before its time")
	}
	if due := s.Due(now.AddDate(0, 0, -10)); len(due) != 1 {
		t.Fatalf("not due after it was missed: %v", due)
	}
	// An old report of this schedule is deleted after the next run, a manual one stays.
	old, _ := s.Generate(Capacity, HTML, facts(now), "schedule: x", sc.ID)
	manual, _ := s.Generate(Capacity, HTML, facts(now), "onur", "")
	later := now.AddDate(0, 0, 40)
	s.now = func() time.Time { return later }
	s.Ran(sc.ID, nil)
	if _, _, err := s.Get(old.ID); err == nil {
		t.Error("expired scheduled report kept")
	}
	if _, _, err := s.Get(manual.ID); err != nil {
		t.Error("manual report deleted by retention")
	}
	if len(s.Due(now)) != 0 {
		t.Error("due again right after running")
	}
	if n := (Schedule{Every: "monthly", At: "01:00"}).Next(now); n.Day() != 1 || n.Month() != time.November {
		t.Errorf("monthly next = %v", n)
	}
}
