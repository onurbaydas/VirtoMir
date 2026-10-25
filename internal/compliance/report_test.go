package compliance

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func sampleCompliance() []HostCompliance {
	now := time.Date(2026, 10, 19, 3, 30, 0, 0, time.UTC)
	return []HostCompliance{
		{
			HostID:       "host-1",
			HostName:     "node1<alpha>",
			EvaluatedAt:  now,
			Score:        75,
			PassedCount:  3,
			FailedCount:  1,
			WarningCount: 0,
			TotalCount:   4,
			Results: []CheckResult{
				{
					ID:          "ssh-root-login",
					Title:       "Disable SSH Root Login <script>alert('xss')</script>",
					Category:    "SSH",
					Severity:    SeverityCritical,
					Status:      StatusPass,
					Detail:      "PermitRootLogin is set to prohibit-password & safe",
					Remediation: "Set PermitRootLogin prohibit-password in /etc/ssh/sshd_config <danger>",
					Benchmark:   "CIS Linux 5.2.5",
				},
				{
					ID:          "host-firewall",
					Title:       "Host Firewall Enabled",
					Category:    "Firewall",
					Severity:    SeverityHigh,
					Status:      StatusFail,
					Detail:      "No active nftables or ufw ruleset found",
					Remediation: "systemctl enable --now nftables",
					Benchmark:   "CIS Linux 3.5.1",
				},
				{
					ID:          "kernel-kptr",
					Title:       "Restrict Kernel Pointer Leaks",
					Category:    "Kernel",
					Severity:    SeverityMedium,
					Status:      StatusPass,
					Detail:      "kernel.kptr_restrict = 1",
					Remediation: "sysctl -w kernel.kptr_restrict=1",
					Benchmark:   "CIS Linux 1.5.1",
				},
				{
					ID:          "core-dumps",
					Title:       "Restrict Core Dumps",
					Category:    "System",
					Severity:    SeverityLow,
					Status:      StatusPass,
					Detail:      "fs.suid_dumpable = 0",
					Remediation: "sysctl -w fs.suid_dumpable=0",
					Benchmark:   "CIS Linux 1.5.4",
				},
			},
		},
		{
			HostID:       "host-2",
			HostName:     "node2",
			EvaluatedAt:  now,
			Score:        50,
			PassedCount:  1,
			FailedCount:  1,
			WarningCount: 0,
			TotalCount:   2,
			Results: []CheckResult{
				{
					ID:          "time-sync",
					Title:       "Time Synchronization Active",
					Category:    "System",
					Severity:    SeverityMedium,
					Status:      StatusPass,
					Detail:      "systemd-timesyncd active",
					Remediation: "systemctl enable --now systemd-timesyncd",
					Benchmark:   "CIS Linux 2.2.1",
				},
				{
					ID:          "svirt-isolation",
					Title:       "Libvirt sVirt MAC Isolation",
					Category:    "Virtualization",
					Severity:    SeverityCritical,
					Status:      StatusFail,
					Detail:      "security_driver = none in qemu.conf",
					Remediation: "Set security_driver = \"apparmor\" in /etc/libvirt/qemu.conf",
					Benchmark:   "STIG V-242398",
				},
			},
		},
	}
}

func TestRenderHTML(t *testing.T) {
	comp := sampleCompliance()
	now := time.Date(2026, 10, 19, 3, 45, 0, 0, time.UTC)
	by := "operator<test>"

	out := RenderHTML(comp, now, by)
	htmlStr := string(out)

	// Asserts valid HTML document structure
	if !strings.HasPrefix(htmlStr, "<!DOCTYPE html>") {
		t.Error("missing DOCTYPE declaration")
	}
	if !strings.Contains(htmlStr, "Host Hardening &amp; Security Compliance Report") {
		t.Error("missing report header title")
	}

	// Asserts check titles
	if !strings.Contains(htmlStr, "Disable SSH Root Login &lt;script&gt;alert(&#39;xss&#39;)&lt;/script&gt;") {
		t.Error("escaped check title not found")
	}
	if strings.Contains(htmlStr, "<script>alert('xss')</script>") {
		t.Error("unescaped script tag found in HTML output!")
	}

	// Asserts host name escaping
	if !strings.Contains(htmlStr, "node1&lt;alpha&gt;") {
		t.Error("escaped host name not found")
	}
	if strings.Contains(htmlStr, "node1<alpha>") {
		t.Error("unescaped host name found in HTML output")
	}

	// Asserts author escaping
	if !strings.Contains(htmlStr, "operator&lt;test&gt;") {
		t.Error("escaped author not found")
	}
	if strings.Contains(htmlStr, "operator<test>") {
		t.Error("unescaped author tag found in HTML output")
	}

	// Asserts severity classes
	for _, sevCls := range []string{"sev-critical", "sev-high", "sev-medium", "sev-low"} {
		if !strings.Contains(htmlStr, sevCls) {
			t.Errorf("missing severity class %q in HTML output", sevCls)
		}
	}

	// Asserts remediation texts
	if !strings.Contains(htmlStr, "class=\"remediation-box\"") {
		t.Error("missing remediation-box class")
	}
	if !strings.Contains(htmlStr, "Set PermitRootLogin prohibit-password in /etc/ssh/sshd_config &lt;danger&gt;") {
		t.Error("escaped remediation text not found")
	}
	if strings.Contains(htmlStr, "<danger>") {
		t.Error("unescaped remediation tag found in HTML output")
	}

	// Asserts print styles
	if !strings.Contains(htmlStr, "@media print") {
		t.Error("missing embedded @media print style")
	}

	// Asserts cluster score card
	// Total checks: 4 + 2 = 6, Passed: 3 + 1 = 4 -> score: (4 * 100) / 6 = 66%
	if !strings.Contains(htmlStr, "Cluster Compliance Score") {
		t.Error("missing Cluster Compliance Score label")
	}
	if !strings.Contains(htmlStr, "66%") {
		t.Errorf("expected 66%% cluster score, output: %s", htmlStr)
	}
}

func TestRenderCSV(t *testing.T) {
	comp := sampleCompliance()
	out := RenderCSV(comp)

	r := csv.NewReader(bytes.NewReader(out))
	records, err := r.ReadAll()
	if err != nil {
		t.Fatalf("failed to parse CSV output: %v", err)
	}

	if len(records) < 1 {
		t.Fatal("empty CSV output")
	}

	// Validate header row
	expectedHeader := []string{
		"HostName", "HostID", "CheckID", "Title", "Category", "Benchmark", "Severity", "Status", "Detail", "Remediation",
	}
	header := records[0]
	if len(header) != len(expectedHeader) {
		t.Fatalf("header length = %d, want %d", len(header), len(expectedHeader))
	}
	for i, want := range expectedHeader {
		if header[i] != want {
			t.Errorf("header[%d] = %q, want %q", i, header[i], want)
		}
	}

	// Validate data rows (4 from host-1, 2 from host-2 = 6 data rows)
	if len(records) != 7 {
		t.Fatalf("expected 7 CSV rows (1 header + 6 data), got %d", len(records))
	}

	firstDataRow := records[1]
	if firstDataRow[0] != "node1<alpha>" {
		t.Errorf("expected HostName node1<alpha>, got %q", firstDataRow[0])
	}
	if firstDataRow[1] != "host-1" {
		t.Errorf("expected HostID host-1, got %q", firstDataRow[1])
	}
	if firstDataRow[2] != "ssh-root-login" {
		t.Errorf("expected CheckID ssh-root-login, got %q", firstDataRow[2])
	}
	if firstDataRow[6] != "critical" {
		t.Errorf("expected Severity critical, got %q", firstDataRow[6])
	}
	if firstDataRow[7] != "pass" {
		t.Errorf("expected Status pass, got %q", firstDataRow[7])
	}
}

func TestRenderJSON(t *testing.T) {
	comp := sampleCompliance()
	now := time.Now().UTC()

	out := RenderJSON(comp, now)
	if len(out) == 0 {
		t.Fatal("empty JSON output")
	}

	var roundtripped []HostCompliance
	if err := json.Unmarshal(out, &roundtripped); err != nil {
		t.Fatalf("JSON round-trip unmarshal failed: %v", err)
	}

	if len(roundtripped) != len(comp) {
		t.Fatalf("roundtripped length = %d, want %d", len(roundtripped), len(comp))
	}

	for i := range comp {
		if roundtripped[i].HostID != comp[i].HostID {
			t.Errorf("[%d] HostID = %q, want %q", i, roundtripped[i].HostID, comp[i].HostID)
		}
		if roundtripped[i].Score != comp[i].Score {
			t.Errorf("[%d] Score = %d, want %d", i, roundtripped[i].Score, comp[i].Score)
		}
		if len(roundtripped[i].Results) != len(comp[i].Results) {
			t.Fatalf("[%d] Results len = %d, want %d", i, len(roundtripped[i].Results), len(comp[i].Results))
		}
		for j := range comp[i].Results {
			if roundtripped[i].Results[j].ID != comp[i].Results[j].ID {
				t.Errorf("[%d][%d] Check ID = %q, want %q", i, j, roundtripped[i].Results[j].ID, comp[i].Results[j].ID)
			}
			if roundtripped[i].Results[j].Severity != comp[i].Results[j].Severity {
				t.Errorf("[%d][%d] Severity = %q, want %q", i, j, roundtripped[i].Results[j].Severity, comp[i].Results[j].Severity)
			}
		}
	}
}

func TestSummarize(t *testing.T) {
	// Empty case
	emptySummary := Summarize(nil)
	if emptySummary.TotalHosts != 0 || emptySummary.Score != 0 || emptySummary.HasFailures() {
		t.Errorf("empty summary mismatch: %+v", emptySummary)
	}
	if got := emptySummary.String(); got != "0 hosts · 0% compliance score · 0 checks: 0 passed, 0 failed, 0 warnings" {
		t.Errorf("empty string = %q", got)
	}

	// 1 host case
	single := sampleCompliance()[:1]
	singleSummary := Summarize(single)
	if singleSummary.TotalHosts != 1 || singleSummary.TotalChecks != 4 || singleSummary.PassedChecks != 3 || singleSummary.FailedChecks != 1 || singleSummary.Score != 75 {
		t.Errorf("single summary mismatch: %+v", singleSummary)
	}
	if !singleSummary.HasFailures() {
		t.Error("expected HasFailures() = true")
	}
	if got := singleSummary.String(); got != "1 host · 75% compliance score · 4 checks: 3 passed, 1 failed, 0 warnings" {
		t.Errorf("single string = %q", got)
	}

	// 2 hosts case
	comp := sampleCompliance()
	summary := Summarize(comp)
	if summary.TotalHosts != 2 || summary.TotalChecks != 6 || summary.PassedChecks != 4 || summary.FailedChecks != 2 || summary.Score != 66 {
		t.Errorf("summary mismatch: %+v", summary)
	}
	if !summary.HasFailures() {
		t.Error("expected HasFailures() = true")
	}
	if got := summary.String(); got != "2 hosts · 66% compliance score · 6 checks: 4 passed, 2 failed, 0 warnings" {
		t.Errorf("summary string = %q", got)
	}
}

func TestRenderJSON_GeneratedAt(t *testing.T) {
	ts := time.Date(2026, 10, 19, 4, 0, 0, 0, time.UTC)
	comp := []HostCompliance{
		{
			HostID:      "h-1",
			HostName:    "node-1",
			Score:       100,
			PassedCount: 1,
			TotalCount:  1,
		},
	}
	out := RenderJSON(comp, ts)
	var roundtripped []HostCompliance
	if err := json.Unmarshal(out, &roundtripped); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if len(roundtripped) != 1 {
		t.Fatalf("len = %d, want 1", len(roundtripped))
	}
	if !roundtripped[0].EvaluatedAt.Equal(ts) {
		t.Errorf("EvaluatedAt = %v, want %v", roundtripped[0].EvaluatedAt, ts)
	}
}
